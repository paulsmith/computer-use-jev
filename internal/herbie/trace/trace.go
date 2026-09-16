// Package trace writes wire diagnostics with credential redaction.
package trace

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
)

type Log struct {
	f           *os.File
	mu          sync.Mutex
	start, last time.Time
}

func Open(path string) (*Log, error) {
	if path == "" {
		return nil, nil
	}
	f, e := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if e != nil {
		return nil, e
	}
	if e = f.Chmod(0600); e != nil {
		_ = f.Close()
		return nil, e
	}
	return &Log{f: f}, nil
}
func OpenAppend(path string) (*Log, error) {
	if path == "" {
		return nil, nil
	}
	f, e := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if e != nil {
		return nil, e
	}
	if e = f.Chmod(0600); e != nil {
		_ = f.Close()
		return nil, e
	}
	return &Log{f: f}, nil
}
func (l *Log) Close() error {
	if l == nil {
		return nil
	}
	return l.f.Close()
}
func (l *Log) stamp() string {
	n := time.Now()
	if l.start.IsZero() {
		l.start = n
		l.last = n
	}
	a, d := n.Sub(l.start), n.Sub(l.last)
	l.last = n
	return fmt.Sprintf("t+%d.%03ds dt+%d.%03ds", a/time.Second, a%time.Second/time.Millisecond, d/time.Second, d%time.Second/time.Millisecond)
}
func fenced(lang string, body []byte) string {
	n, run, max := 3, 0, 0
	for _, c := range body {
		if c == '`' {
			run++
			if run > max {
				max = run
			}
		} else {
			run = 0
		}
	}
	if max+1 > n {
		n = max + 1
	}
	fence := strings.Repeat("`", n)
	return fence + lang + "\n" + string(body) + "\n" + fence + "\n"
}
func pretty(b []byte) string {
	b = redactBytes(b)
	lang, body := "text", b
	var x any
	if json.Unmarshal(b, &x) == nil {
		lang = "json"
		body, _ = json.MarshalIndent(x, "", "  ")
	}
	return fenced(lang, body)
}
func isSecret(name string) bool {
	return strings.EqualFold(name, "authorization") || strings.EqualFold(name, "x-api-key") || strings.EqualFold(name, "api-key")
}
func (l *Log) Request(method, url string, headers http.Header, body []byte) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, err := fmt.Fprintf(l.f, "\n## %s %s  (%s)\n\n", method, Redact(url), l.stamp()); err != nil {
		panic(err)
	}
	if len(headers) > 0 {
		var out strings.Builder
		names := slices.Sorted(maps.Keys(headers))
		for _, k := range names {
			for _, v := range headers[k] {
				if isSecret(k) || Redact(v) != v {
					v = "<redacted>"
				} else {
					v = Redact(v)
				}
				fmt.Fprintf(&out, "%s: %s\n", k, v)
			}
		}
		if _, err := fmt.Fprint(l.f, fenced("http", []byte(out.String()))); err != nil {
			panic(err)
		}
	}
	if len(body) > 0 {
		if _, err := fmt.Fprint(l.f, pretty(redactBytes(body))); err != nil {
			panic(err)
		}
	}
}
func (l *Log) Response(status int, body []byte) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, err := fmt.Fprintf(l.f, "\n**HTTP %d**  (%s)\n", status, l.stamp()); err != nil {
		panic(err)
	}
	if len(body) > 0 {
		if _, err := fmt.Fprintln(l.f); err != nil {
			panic(err)
		}
		if _, err := fmt.Fprint(l.f, pretty(redactBytes(body))); err != nil {
			panic(err)
		}
	}
}
func (l *Log) Error(err error) {
	if err != nil {
		l.Response(0, []byte(Redact(err.Error())))
	}
}
func (l *Log) SSE(name string, data []byte) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if name == "" {
		name = "(unnamed)"
	}
	if _, err := fmt.Fprintf(l.f, "\n### event: %s  (%s)\n\n", name, l.stamp()); err != nil {
		panic(err)
	}
	if len(data) > 0 {
		if _, err := fmt.Fprint(l.f, pretty(redactBytes(data))); err != nil {
			panic(err)
		}
	}
}
