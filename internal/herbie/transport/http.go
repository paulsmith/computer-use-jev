package transport

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/paulsmith/computer-use-jev/internal/herbie/version"
)

const DefaultMaxBody int64 = 1 << 20
const errorMaxBody int64 = 4096
const maxRetryAfter = 2 * time.Minute
const maxInt64 = 1<<63 - 1

// timeoutError is returned when the connect or idle timeout fires. It wraps
// context.DeadlineExceeded so that errors.Is checks against it still pass,
// but carries a clearer message.
type timeoutError string

func (e timeoutError) Error() string { return string(e) }
func (timeoutError) Unwrap() error   { return context.DeadlineExceeded }

type response struct {
	Status        int
	Body          []byte
	RetryAfter    time.Duration
	StreamFailure bool
}

// Response is the bounded non-streaming HTTP result.
type Response = response
type traceLogger interface {
	Request(string, string, http.Header, []byte)
	Response(int, []byte)
	SSE(string, []byte)
}

var (
	traceMu   sync.RWMutex
	traceSink traceLogger
)

// SetTrace installs the sink used by clients without an explicit trace sink.
func SetTrace(l traceLogger) {
	traceMu.Lock()
	traceSink = l
	traceMu.Unlock()
}

type Client struct {
	HTTP *http.Client
	// ConnectTimeout aborts an SSE request before response headers arrive; zero disables it.
	ConnectTimeout time.Duration
	// IdleTimeout aborts a stream that produces no bytes; zero disables it.
	IdleTimeout time.Duration
	Trace       traceLogger
	// TraceDisabled prevents sensitive protocol exchanges from entering the process-wide trace sink.
	TraceDisabled bool
}

func (c Client) trace() traceLogger {
	if c.TraceDisabled {
		return nil
	}
	if c.Trace != nil {
		return c.Trace
	}
	traceMu.RLock()
	l := traceSink
	traceMu.RUnlock()
	return l
}
func (c Client) traceError(err error) {
	if l, ok := c.trace().(interface{ Error(error) }); ok {
		l.Error(err)
	}
}
func (c Client) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}
func retryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)
	if value != "" {
		seconds := int64(0)
		for _, b := range []byte(value) {
			if b < '0' || b > '9' {
				seconds = -1
				break
			}
			if seconds > int64(maxRetryAfter/time.Second) {
				return maxRetryAfter
			}
			seconds = seconds*10 + int64(b-'0')
		}
		if seconds >= 0 {
			if seconds >= int64(maxRetryAfter/time.Second) {
				return maxRetryAfter
			}
			return time.Duration(seconds) * time.Second
		}
	}
	when, err := http.ParseTime(value)
	if err != nil || !when.After(time.Now()) {
		return 0
	}
	return min(time.Until(when), maxRetryAfter)
}

// prepare copies headers and adds the default User-Agent when the caller set none.
func prepare(headers http.Header) http.Header {
	out := headers.Clone()
	if out == nil {
		out = http.Header{}
	}
	if out.Get("User-Agent") == "" {
		out.Set("User-Agent", "github.com/paulsmith/computer-use-jev/internal/herbie/"+version.String())
	}
	return out
}
func (c Client) request(ctx context.Context, method, url string, headers http.Header, body []byte, max int64) (response, error) {
	h := prepare(headers)
	r, e := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if e != nil {
		return response{}, e
	}
	r.Header = h
	if l := c.trace(); l != nil {
		l.Request(method, url, h, body)
	}
	x, e := c.client().Do(r)
	if e != nil {
		c.traceError(e)
		return response{}, e
	}
	defer func() {
		if err := x.Body.Close(); err != nil {
			panic(err)
		}
	}()
	if max <= 0 {
		max = DefaultMaxBody
	}
	limit := max
	if limit < maxInt64 {
		limit++
	}
	b, e := io.ReadAll(io.LimitReader(x.Body, limit))
	if e != nil {
		if l := c.trace(); l != nil {
			l.Response(x.StatusCode, []byte(e.Error()))
		}
		return response{}, e
	}
	if int64(len(b)) > max {
		e = errors.New("HTTP response exceeds body limit")
		if l := c.trace(); l != nil {
			l.Response(x.StatusCode, []byte(e.Error()))
		}
		return response{}, e
	}
	out := response{Status: x.StatusCode, Body: b, RetryAfter: retryAfter(x.Header.Get("Retry-After"))}
	if l := c.trace(); l != nil {
		l.Response(out.Status, b)
	}
	return out, nil
}
func (c Client) Get(ctx context.Context, url string, headers http.Header, max int64) (response, error) {
	return c.request(ctx, http.MethodGet, url, headers, nil, max)
}
func (c Client) Post(ctx context.Context, url string, headers http.Header, body []byte, max int64) (response, error) {
	return c.request(ctx, http.MethodPost, url, headers, body, max)
}

func (c Client) SSE(ctx context.Context, url string, headers http.Header, body []byte, cb sseCallback, tick func()) (response, error) {
	if tick != nil {
		tick()
	}
	if err := ctx.Err(); err != nil {
		c.traceError(err)
		return response{}, err
	}
	h := prepare(headers)
	if body != nil && h.Get("Content-Type") == "" {
		h.Set("Content-Type", "application/json")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var connect *time.Timer
	var connectDone <-chan time.Time
	if c.ConnectTimeout > 0 {
		connect = time.NewTimer(c.ConnectTimeout)
		connectDone = connect.C
		defer connect.Stop()
	}
	r, e := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if e != nil {
		return response{}, e
	}
	r.Header = h
	if l := c.trace(); l != nil {
		l.Request("POST", url, h, body)
	}
	type result struct {
		response *http.Response
		err      error
	}
	resultCh := make(chan result)
	go func() {
		x, err := c.client().Do(r)
		select {
		case resultCh <- result{x, err}:
		case <-ctx.Done():
			if x != nil {
				if err := x.Body.Close(); err != nil {
					panic(err)
				}
			}
		}
	}()
	out := response{}
	var x *http.Response
	var traceBody []byte
	defer func() {
		if x != nil {
			if err := x.Body.Close(); err != nil {
				panic(err)
			}
		}
	}()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for x == nil {
		select {
		case q := <-resultCh:
			if q.err != nil {
				c.traceError(q.err)
				return response{}, q.err
			}
			x = q.response
			if connect != nil && !connect.Stop() {
				select {
				case <-connect.C:
				default:
				}
			}
			connectDone = nil
		case <-ctx.Done():
			c.traceError(ctx.Err())
			return out, ctx.Err()
		case <-connectDone:
			cancel()
			err := timeoutError("connect timeout")
			c.traceError(err)
			return out, err
		case <-ticker.C:
			if tick != nil {
				tick()
			}
			if err := ctx.Err(); err != nil {
				c.traceError(err)
				return out, err
			}
		}
	}
	defer func() {
		if l := c.trace(); l != nil {
			l.Response(out.Status, traceBody)
		}
	}()
	out.Status = x.StatusCode
	out.RetryAfter = retryAfter(x.Header.Get("Retry-After"))
	if x.StatusCode < 200 || x.StatusCode >= 300 {
		out.Body, e = io.ReadAll(io.LimitReader(x.Body, errorMaxBody+1))
		if int64(len(out.Body)) > errorMaxBody {
			out.Body = out.Body[:errorMaxBody]
		}
		if e != nil {
			traceBody = []byte(e.Error())
		} else {
			traceBody = out.Body
		}
		return out, e
	}
	p := newSSEParser(func(name, data string) error {
		if l := c.trace(); l != nil {
			l.SSE(name, []byte(data))
		}
		return cb(name, data)
	})
	type chunk struct {
		data []byte
		err  error
	}
	chunks := make(chan chunk, 1)
	go func() {
		send := func(q chunk) bool {
			select {
			case chunks <- q:
				return true
			case <-ctx.Done():
				return false
			}
		}
		for {
			b := make([]byte, 4096)
			n, err := x.Body.Read(b)
			if n > 0 && !send(chunk{data: b[:n]}) {
				return
			}
			if err != nil {
				send(chunk{err: err})
				return
			}
		}
	}()
	var timer *time.Timer
	var idle <-chan time.Time
	if c.IdleTimeout > 0 {
		timer = time.NewTimer(c.IdleTimeout)
		defer timer.Stop()
		idle = timer.C
	}
	for {
		select {
		case q := <-chunks:
			if tick != nil {
				tick()
			}
			if err := ctx.Err(); err != nil {
				traceBody = []byte(err.Error())
				return out, err
			}
			if len(q.data) > 0 {
				if timer != nil {
					if !timer.Stop() {
						select {
						case <-timer.C:
						default:
						}
					}
					timer.Reset(c.IdleTimeout)
				}
				if z := p.feed(q.data); z != nil {
					traceBody = []byte(z.Error())
					return out, z
				}
			}
			if q.err == io.EOF {
				if z := p.finalize(); z != nil {
					traceBody = []byte(z.Error())
					return out, z
				}
				return out, nil
			}
			if q.err != nil {
				out.StreamFailure = true
				traceBody = []byte(q.err.Error())
				return out, q.err
			}
		case <-ticker.C:
			if tick != nil {
				tick()
			}
			if err := ctx.Err(); err != nil {
				traceBody = []byte(err.Error())
				return out, err
			}
		case <-ctx.Done():
			traceBody = []byte(ctx.Err().Error())
			return out, ctx.Err()
		case <-idle:
			cancel()
			out.StreamFailure = true
			e := error(timeoutError("stream idle timeout"))
			traceBody = []byte(e.Error())
			return out, e
		}
	}
}
