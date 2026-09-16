// Package providerconfig resolves provider-scoped passthrough settings.
package providerconfig

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/textproto"
	"os"
	"slices"
	"strings"
	"sync"

	"crypto/rand"
	"encoding/hex"

	"github.com/paulsmith/computeruser/internal/herbie/config"
	"github.com/paulsmith/computeruser/internal/herbie/trace"
	"github.com/paulsmith/computeruser/internal/herbie/transport"
)

var reservedBodyFields = map[string]struct{}{
	"model":          {},
	"stream":         {},
	"messages":       {},
	"input":          {},
	"include":        {},
	"n":              {},
	"system":         {},
	"tools":          {},
	"stream_options": {},
	"instructions":   {},
}

// APIKey resolves an inline provider api_key, its api_key_env setting, or fallbackEnv.
// A leading $ reads an environment variable and $$ escapes a literal dollar sign.
func APIKey(c *config.Config, prefix, fallbackEnv string) string {
	if c == nil {
		return ""
	}
	if value := resolveSecret(c.AnyString(prefix + "api_key")); value != "" {
		return value
	}
	if env := c.AnyString(prefix + "api_key_env"); env != "" {
		if value := os.Getenv(env); value != "" {
			trace.RegisterSecret(value)
			return value
		}
	}
	if fallbackEnv != "" {
		if value := os.Getenv(fallbackEnv); value != "" {
			trace.RegisterSecret(value)
			return value
		}
	}
	return ""
}

func resolveSecret(value string) string {
	if value == "" {
		return ""
	}
	if strings.HasPrefix(value, "$$") {
		value = value[1:]
	} else if strings.HasPrefix(value, "$") {
		resolved, ok := os.LookupEnv(value[1:])
		if !ok || resolved == "" {
			return ""
		}
		value = resolved
	}
	trace.RegisterSecret(value)
	return value
}

// ExtraBody returns a deep-copied provider-scoped JSON object with protocol-owned
// top-level fields removed.
func ExtraBody(c *config.Config, prefix string) map[string]any {
	if c == nil {
		return nil
	}
	node, ok := c.Node(prefix + "extra_body").(map[string]any)
	if !ok {
		if c.Node(prefix+"extra_body") != nil {
			warn("%sextra_body must be a JSON object; ignoring it", prefix)
		}
		return nil
	}
	for key := range reservedBodyFields {
		if _, exists := node[key]; exists {
			warn("%sextra_body: %q is protocol-owned; ignoring it", prefix, key)
			delete(node, key)
		}
	}
	return node
}

// ApplyExtraBody recursively merges extra into body. Scalar and array members
// replace generated values; object members extend generated objects.
func ApplyExtraBody(body, extra map[string]any) {
	if extra == nil {
		return
	}
	mergeObject(body, extra)
}

func mergeObject(body, extra map[string]any) {
	for key, value := range extra {
		current, currentObject := body[key].(map[string]any)
		additional, additionalObject := value.(map[string]any)
		if currentObject && additionalObject {
			mergeObject(current, additional)
		} else {
			body[key] = value
		}
	}
}

// ExtraHeaders resolves a provider-scoped JSON object into HTTP headers.
// Empty configured values are omitted so standalone consumers never see removal markers.
func ExtraHeaders(c *config.Config, prefix string) http.Header {
	return parseExtraHeaders(c, prefix, false)
}

// parseExtraHeaders decodes a provider-scoped extra_headers object and validates it with
// headersFromMap. keepEmpty lets DefaultHeaders see configured empty values as removal markers.
func parseExtraHeaders(c *config.Config, prefix string, keepEmpty bool) http.Header {
	if c == nil {
		return nil
	}
	node := c.Node(prefix + "extra_headers")
	object, ok := node.(map[string]any)
	if !ok {
		if node != nil {
			warn("%sextra_headers must be a JSON object of name/value members; ignoring it", prefix)
		}
		return nil
	}
	return headersFromMap(object, prefix+"extra_headers", keepEmpty)
}

// headersFromMap validates an extra-headers object into HTTP headers. Defaults keep empty
// values as removal markers; user config drops them so standalone consumers never see one.
func headersFromMap(object map[string]any, label string, keepEmpty bool) http.Header {
	out := make(http.Header)
	for name, raw := range object {
		value, ok := raw.(string)
		if !ok {
			warn("%s: header %q needs a string value; ignoring it", label, name)
			continue
		}
		if !validHeaderName(name) {
			warn("%s: invalid header name %q; ignoring it", label, name)
			continue
		}
		resolved, ok := resolveHeaderValue(value)
		if !ok {
			warn("%s: header %q dropped; environment variable is unset or empty", label, name)
			continue
		}
		if resolved == "" {
			if !keepEmpty {
				continue
			}
			// curl's spelling for suppressing a header; removal markers never reach a request.
			out[http.CanonicalHeaderKey(name)] = []string{""}
			continue
		}
		if !validHeaderValue(resolved) {
			warn("%s: header %q needs a control-character-free value; ignoring it", label, name)
			continue
		}
		out[http.CanonicalHeaderKey(name)] = append(out[http.CanonicalHeaderKey(name)], resolved)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func resolveHeaderValue(value string) (string, bool) {
	if strings.HasPrefix(value, "$$") {
		return value[1:], true
	}
	if strings.HasPrefix(value, "$") {
		resolved, ok := os.LookupEnv(value[1:])
		if !ok || resolved == "" {
			return "", false
		}
		trace.RegisterSecret(resolved)
		return resolved, true
	}
	return value, true
}

// ProcessSessionID returns a per-process UUID used for session headers outside any conversation.
func ProcessSessionID() string {
	processSessionID.Do(func() { processSessionID.Value = uuid() })
	return processSessionID.Value
}

var processSessionID struct {
	sync.Once
	Value string
}

// SessionPlaceholder is the header-value token replaced by the conversation id.
const SessionPlaceholder = "{session_id}"

// ExpandSessionHeaders copies templates, filling SessionPlaceholder in every value.
func ExpandSessionHeaders(headers http.Header, sessionID string) http.Header {
	if headers == nil {
		return nil
	}
	out := make(http.Header, len(headers))
	for name, values := range headers {
		expanded := make([]string, len(values))
		for i, value := range values {
			expanded[i] = strings.ReplaceAll(value, SessionPlaceholder, sessionID)
		}
		out[name] = expanded
	}
	return out
}

// MergeHeaders applies overrides over defaults: a non-empty value replaces every
// case-insensitively matching default, and an empty value removes it without being sent.
func MergeHeaders(defaults, overrides http.Header) http.Header {
	if len(defaults) == 0 && len(overrides) == 0 {
		return nil
	}
	removed := make(map[string]struct{}, len(overrides))
	for name, values := range overrides {
		if len(values) > 0 && values[0] == "" {
			removed[textproto.CanonicalMIMEHeaderKey(name)] = struct{}{}
		}
	}
	out := make(http.Header)
	for name, values := range defaults {
		if _, ok := removed[textproto.CanonicalMIMEHeaderKey(name)]; ok {
			continue
		}
		out[textproto.CanonicalMIMEHeaderKey(name)] = slices.Clone(values)
	}
	for name, values := range overrides {
		if _, ok := removed[textproto.CanonicalMIMEHeaderKey(name)]; ok {
			continue
		}
		out[textproto.CanonicalMIMEHeaderKey(name)] = slices.Clone(values)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// DefaultHeaders parses a definition-declared extra-headers object, merged under the user's
// configured extra_headers: a configured value replaces a same-named default, an empty
// configured value removes it. Malformed defaults warn and drop like config ones.
func DefaultHeaders(c *config.Config, prefix, defaults string) http.Header {
	merged := MergeHeaders(headersFromObject(defaults, "provider "+strings.TrimSuffix(prefix, ".")+" default extra_headers"), parseExtraHeaders(c, prefix, true))
	if len(merged) == 0 {
		return nil
	}
	return merged
}

// headersFromObject resolves an extra_headers JSON object into HTTP headers with the same
// validation as ExtraHeaders.
func headersFromObject(text, label string) http.Header {
	if text == "" {
		return nil
	}
	var object map[string]any
	if err := json.Unmarshal([]byte(text), &object); err != nil {
		warn("%s must be a JSON object of name/value members; ignoring it", label)
		return nil
	}
	return headersFromMap(object, label, true)
}

func uuid() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	x := hex.EncodeToString(b[:])
	return x[:8] + "-" + x[8:12] + "-" + x[12:16] + "-" + x[16:20] + "-" + x[20:]
}

func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for _, b := range []byte(name) {
		if (b < 'a' || b > 'z') && (b < 'A' || b > 'Z') && (b < '0' || b > '9') && !strings.ContainsRune("!#$%&'*+-.^_`|~", rune(b)) {
			return false
		}
	}
	return true
}

func validHeaderValue(value string) bool {
	for _, b := range []byte(value) {
		if (b < ' ' && b != '\t') || b == 0x7f {
			return false
		}
	}
	return true
}

func warn(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) }

// Retry builds a RetryPolicy from the global http.* settings shared by every provider.
func Retry(c *config.Config) transport.RetryPolicy {
	p := transport.DefaultRetryPolicy()
	p.MaxAttempts = c.Int("http.max_retries") + 1
	p.BaseDelay = c.Duration("http.retry_base")
	p.IdleTimeout = c.Duration("http.idle_timeout")
	return p
}

// CacheTTL resolves a provider's cache_ttl setting, defaulting to "1h".
func CacheTTL(c *config.Config, prefix string) string {
	ttl := c.AnyString(prefix + "cache_ttl")
	if ttl == "" {
		return "1h"
	}
	return ttl
}
