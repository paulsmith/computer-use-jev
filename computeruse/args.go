package computeruse

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"
)

func parseObject(argsJSON string) (map[string]json.RawMessage, error) {
	if argsJSON == "" {
		return nil, fmt.Errorf("invalid arguments: empty input")
	}
	if !utf8.ValidString(argsJSON) {
		return nil, fmt.Errorf("invalid arguments: invalid UTF-8")
	}
	if err := validateJSONStrings(argsJSON); err != nil {
		return nil, fmt.Errorf("invalid arguments: %v", err)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal([]byte(argsJSON), &root); err != nil {
		var value any
		if err := json.Unmarshal([]byte(argsJSON), &value); err != nil {
			return nil, fmt.Errorf("invalid arguments: %v", err)
		}
		return map[string]json.RawMessage{}, nil
	}
	if root == nil {
		return map[string]json.RawMessage{}, nil
	}
	return root, nil
}

func jsonString(raw json.RawMessage) (string, bool) {
	var value string
	if len(raw) == 0 || jsonNull(raw) || json.Unmarshal(raw, &value) != nil || strings.IndexByte(value, 0) >= 0 {
		return "", false
	}
	return value, true
}

func jsonNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func validateJSONStrings(input string) error {
	for i := 0; i < len(input); i++ {
		if input[i] != '"' {
			continue
		}
		for i++; i < len(input) && input[i] != '"'; i++ {
			if input[i] < 0x20 {
				return fmt.Errorf("invalid control character")
			}
			if input[i] != '\\' {
				continue
			}
			i++
			if i == len(input) {
				return fmt.Errorf("unterminated escape")
			}
			if input[i] != 'u' {
				continue
			}
			if i+4 >= len(input) {
				return fmt.Errorf("invalid unicode escape")
			}
			unit, ok := hexUnit(input[i+1 : i+5])
			if !ok {
				return fmt.Errorf("invalid unicode escape")
			}
			i += 4
			if unit == 0 {
				return fmt.Errorf("NUL in string")
			}
			if unit >= 0xd800 && unit <= 0xdbff {
				if i+6 >= len(input) || input[i+1] != '\\' || input[i+2] != 'u' {
					return fmt.Errorf("unpaired surrogate")
				}
				low, ok := hexUnit(input[i+3 : i+7])
				if !ok || low < 0xdc00 || low > 0xdfff {
					return fmt.Errorf("unpaired surrogate")
				}
				i += 6
			} else if unit >= 0xdc00 && unit <= 0xdfff {
				return fmt.Errorf("unpaired surrogate")
			}
		}
		if i == len(input) {
			return fmt.Errorf("unterminated string")
		}
	}
	return nil
}

func hexUnit(value string) (rune, bool) {
	unit, err := strconv.ParseUint(value, 16, 16)
	return rune(unit), err == nil
}

func duplicateField(input string) (string, error) {
	decoder := json.NewDecoder(strings.NewReader(input))
	token, err := decoder.Token()
	if err != nil {
		return "", fmt.Errorf("invalid arguments: %v", err)
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return "", fmt.Errorf("invalid arguments: expected an object")
	}
	seen := map[string]struct{}{}
	for decoder.More() {
		token, err = decoder.Token()
		if err != nil {
			return "", fmt.Errorf("invalid arguments: %v", err)
		}
		name, ok := token.(string)
		if !ok {
			return "", fmt.Errorf("invalid arguments: expected a field name")
		}
		if _, dup := seen[name]; dup {
			return name, nil
		}
		seen[name] = struct{}{}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return "", fmt.Errorf("invalid arguments: %v", err)
		}
	}
	if _, err := decoder.Token(); err != nil {
		return "", fmt.Errorf("invalid arguments: %v", err)
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return "", fmt.Errorf("invalid arguments: trailing value")
		}
		return "", fmt.Errorf("invalid arguments: %v", err)
	}
	return "", nil
}

// parseArgs validates input the same way parseComputerUseArgs does and
// returns the exported form.
func parseArgs(input string) (Args, error) {
	parsed, err := parseComputerUseArgs(input)
	if err != nil {
		return Args{}, err
	}
	return Args{
		Action: parsed.action, App: parsed.app, Window: parsed.window,
		Target: parsed.target, Text: parsed.text, Key: parsed.key,
	}, nil
}
