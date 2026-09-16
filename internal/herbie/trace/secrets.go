package trace

import (
	"sort"
	"strconv"
	"strings"
	"sync"
)

var secretRegistry struct {
	sync.RWMutex
	values map[string]struct{}
}

// RegisterSecret adds a value to the process-wide trace redaction registry. Values remain registered
// so an old access or refresh token cannot leak from a later trace entry after rotation.
func RegisterSecret(value string) {
	if value == "" {
		return
	}
	secretRegistry.Lock()
	if secretRegistry.values == nil {
		secretRegistry.values = map[string]struct{}{}
	}
	secretRegistry.values[value] = struct{}{}
	secretRegistry.Unlock()
}

// Redact replaces every registered secret in value with <redacted>.
func Redact(value string) string {
	secretRegistry.RLock()
	values := make([]string, 0, len(secretRegistry.values))
	for secret := range secretRegistry.values {
		values = append(values, secret)
	}
	secretRegistry.RUnlock()
	sort.Slice(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	for _, secret := range values {
		value = strings.ReplaceAll(value, secret, "<redacted>")
		escaped := strconv.Quote(secret)
		value = strings.ReplaceAll(value, escaped[1:len(escaped)-1], "<redacted>")
	}
	return value
}

func redactBytes(value []byte) []byte {
	if len(value) == 0 {
		return value
	}
	return []byte(Redact(string(value)))
}
