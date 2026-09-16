package transport

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

var scripts = regexp.MustCompile(`(?is)<script[^>]*>.*?</script\s*>|<style[^>]*>.*?</style\s*>`)
var tags = regexp.MustCompile(`</?[A-Za-z][^>]*>`)

func errorText(b []byte) string {
	var x struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
	}
	_ = json.Unmarshal(b, &x)
	if len(x.Error) > 0 {
		var s string
		if json.Unmarshal(x.Error, &s) == nil {
			return s
		}
		var e struct{ Message string }
		if json.Unmarshal(x.Error, &e) == nil {
			return e.Message
		}
	}
	return x.Message
}
func unwrapSSE(body []byte) []byte {
	var got, first []byte
	p := newSSEParser(func(_ string, data string) error {
		if data != "" && first == nil {
			first = []byte(data)
		}
		if data != "" && errorText([]byte(data)) != "" {
			got = []byte(data)
			return fmt.Errorf("found")
		}
		return nil
	})
	_ = p.feed(body)
	_ = p.finalize()
	if got == nil {
		return first
	}
	return got
}
func clean(s string) string {
	s = scripts.ReplaceAllString(s, " ")
	s = tags.ReplaceAllString(s, " ")
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 200 {
		n := 200
		for n > 0 && !utf8.ValidString(s[:n]) {
			n--
		}
		for n > 0 && (s[n]&0xc0) == 0x80 {
			n--
		}
		s = s[:n] + "..."
	}
	return s
}
func APIError(status int, body []byte) string {
	if b := unwrapSSE(body); len(b) > 0 {
		body = b
	}
	msg := errorText(body)
	if msg != "" {
		msg = clean(msg)
	}
	if msg == "" {
		msg = clean(string(body))
	}
	if status > 0 {
		if msg != "" {
			return fmt.Sprintf("HTTP %d: %s", status, msg)
		}
		return fmt.Sprintf("HTTP %d", status)
	}
	if msg != "" {
		return msg
	}
	return "request failed"
}
func ModelsError(name, base string, hasKey bool, status int) string {
	if name == "" {
		name = "provider"
	}
	if status == 401 || status == 403 {
		if hasKey {
			return fmt.Sprintf("%s rejected the API key (HTTP %d) — check it and retry", name, status)
		}
		return fmt.Sprintf("%s requires an API key (HTTP %d) — none is configured", name, status)
	}
	if status >= 200 && status < 300 {
		return fmt.Sprintf("%s sent an empty or truncated /models response", name)
	}
	if status == 0 {
		return fmt.Sprintf("could not reach %s at %s", name, base)
	}
	return fmt.Sprintf("listing %s models failed (HTTP %d)", name, status)
}
