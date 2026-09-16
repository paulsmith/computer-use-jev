// Package transport implements HTTP streaming primitives.
package transport

import (
	"bytes"
	"errors"
	"strings"
)

const sseBufferLimit = 1 << 20

var (
	errSSELineTooLarge  = errors.New("SSE line exceeds 1 MiB")
	errSSEEventTooLarge = errors.New("SSE event exceeds 1 MiB")
)

type sseCallback func(event, data string) error

type sseParser struct {
	line, event string
	data        []string
	eventBytes  int
	cb          sseCallback
	aborted     bool
}

func newSSEParser(cb sseCallback) *sseParser { return &sseParser{cb: cb} }
func (p *sseParser) emit() error {
	if p.event == "" && len(p.data) == 0 {
		p.eventBytes = 0
		return nil
	}
	e, d := p.event, strings.Join(p.data, "\n")
	p.event = ""
	p.data = nil
	p.eventBytes = 0
	if !p.aborted && p.cb != nil {
		if err := p.cb(e, d); err != nil {
			p.aborted = true
			return err
		}
	}
	return nil
}
func (p *sseParser) lineIn(raw string, lf bool) error {
	s := strings.TrimSuffix(raw, "\r")
	if s == "" {
		return p.emit()
	}
	if strings.HasPrefix(s, ":") {
		return nil
	}
	k, v, ok := strings.Cut(s, ":")
	if !ok {
		v = ""
	}
	v = strings.TrimPrefix(v, " ")
	switch k {
	case "event", "data":
		n := len(raw)
		if lf {
			n++
		}
		if p.eventBytes+n > sseBufferLimit {
			return p.abort(errSSEEventTooLarge)
		}
		p.eventBytes += n
		if k == "event" {
			p.event = v
		} else {
			p.data = append(p.data, v)
		}
	}
	return nil
}
func (p *sseParser) abort(err error) error {
	p.aborted = true
	p.line = ""
	p.event = ""
	p.data = nil
	p.eventBytes = 0
	return err
}
func (p *sseParser) feed(b []byte) error {
	if p.aborted {
		return nil
	}
	for {
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			if len(p.line)+len(b) > sseBufferLimit {
				return p.abort(errSSELineTooLarge)
			}
			p.line += string(b)
			return nil
		}
		if len(p.line)+i > sseBufferLimit {
			return p.abort(errSSELineTooLarge)
		}
		line := p.line + string(b[:i])
		p.line = ""
		if err := p.lineIn(line, true); err != nil {
			return err
		}
		b = b[i+1:]
	}
}
func (p *sseParser) finalize() error {
	if p.aborted {
		return nil
	}
	if p.line != "" {
		line := p.line
		p.line = ""
		if err := p.lineIn(line, false); err != nil {
			return err
		}
	}
	return p.emit()
}
