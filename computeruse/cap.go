package computeruse

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	outputCapLines     = 2000
	outputCapLineWidth = 500
	readLineDelim      = "→"
	defaultOutputCap   = 50 * 1024
)

// outputCapBytes returns the configured tool output cap, defaulting to 50 KiB.
func outputCapBytes() int {
	s := strings.TrimSpace(strings.ToLower(os.Getenv("COMPUTERUSER_TOOL_OUTPUT_CAP")))
	if s == "" {
		return defaultOutputCap
	}
	factor := 1
	if strings.HasSuffix(s, "k") {
		factor, s = 1024, s[:len(s)-1]
	} else if strings.HasSuffix(s, "m") {
		factor, s = 1024*1024, s[:len(s)-1]
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 || n > int(^uint(0)>>1)/factor {
		return defaultOutputCap
	}
	return n * factor
}

// capLineLengths replaces each suffix beyond maxLineBytes with an elision marker.
func capLineLengths(data []byte, maxLineBytes int) []byte {
	out := make([]byte, 0, len(data))
	for len(data) > 0 {
		i := 0
		for i < len(data) && data[i] != '\n' {
			i++
		}
		if i > maxLineBytes {
			out = append(out, data[:maxLineBytes]...)
			out = append(out, "...["+strconv.Itoa(i-maxLineBytes)+" bytes elided]"...)
		} else {
			out = append(out, data[:i]...)
		}
		if i < len(data) {
			out = append(out, '\n')
			i++
		}
		data = data[i:]
	}
	return out
}

// capToolText sanitizes, line-wraps, and byte-caps tool output, appending a
// truncation marker with optional recovery guidance when the cap bites.
func capToolText(input string, capBytes int, guidance string) string {
	output := string(capLineLengths([]byte(ctrlStrip(sanitizeUTF8(input))), outputCapLineWidth))
	if capBytes <= 0 {
		capBytes = outputCapBytes()
	}
	if len(output) <= capBytes {
		return output
	}
	marker := fmt.Sprintf("\n\n[truncated at %d bytes]", capBytes)
	if guidance != "" {
		marker = fmt.Sprintf("\n\n[truncated at %d bytes; %s]", capBytes, guidance)
	}
	if len(marker) >= capBytes {
		return string(truncateUTF8([]byte(marker), capBytes))
	}
	return string(truncateUTF8([]byte(output), capBytes-len(marker))) + marker
}

// sanitizeUTF8 replaces NUL bytes, which would otherwise terminate downstream
// C-string handling.
func sanitizeUTF8(input string) string {
	return strings.Map(func(r rune) rune {
		if r == 0 {
			return '\uFFFD'
		}
		return r
	}, input)
}

// truncateUTF8 cuts at a rune boundary so the result stays valid UTF-8.
func truncateUTF8(b []byte, limit int) []byte {
	if limit >= len(b) {
		return b
	}
	if limit <= 0 {
		return b[:0]
	}
	for limit > 0 && !utf8.RuneStart(b[limit]) {
		limit--
	}
	return b[:limit]
}

const (
	ctrlNormal = iota
	ctrlESC
	ctrlCSI
	ctrlOSC
	ctrlOSCEscape
	ctrlString
	ctrlStringEscape
	ctrlESCIntermediate
)

// ctrlStrip removes ANSI escape sequences and other control bytes.
func ctrlStrip(input string) string {
	var output strings.Builder
	output.Grow(len(input))
	state := ctrlNormal
	for _, c := range []byte(input) {
		again := true
		for again {
			again = false
			switch state {
			case ctrlNormal:
				if c == 0x1b {
					state = ctrlESC
				} else if c == '\t' || c == '\n' || (c >= 0x20 && c != 0x7f) {
					output.WriteByte(c)
				}
			case ctrlESC:
				switch {
				case c == '[':
					state = ctrlCSI
				case c == ']':
					state = ctrlOSC
				case c == 'P' || c == '^' || c == '_':
					state = ctrlString
				case c >= 0x20 && c <= 0x2f:
					state = ctrlESCIntermediate
				case c >= 0x30 && c <= 0x7e:
					state = ctrlNormal
				default:
					state = ctrlNormal
					again = true
				}
			case ctrlCSI:
				if ctrlAbort(c) {
					state = ctrlNormal
					again = true
				} else if c >= 0x40 && c <= 0x7e {
					state = ctrlNormal
				}
			case ctrlOSC:
				if ctrlAbort(c) {
					state = ctrlNormal
					again = true
				} else if c == 0x07 {
					state = ctrlNormal
				} else if c == 0x1b {
					state = ctrlOSCEscape
				}
			case ctrlOSCEscape:
				if c == '\\' {
					state = ctrlNormal
				} else if ctrlAbort(c) {
					state = ctrlNormal
					again = true
				} else {
					state = ctrlOSC
					again = true
				}
			case ctrlString:
				if ctrlAbort(c) {
					state = ctrlNormal
					again = true
				} else if c == 0x1b {
					state = ctrlStringEscape
				}
			case ctrlStringEscape:
				if c == '\\' {
					state = ctrlNormal
				} else if ctrlAbort(c) {
					state = ctrlNormal
					again = true
				} else {
					state = ctrlString
					again = true
				}
			case ctrlESCIntermediate:
				if ctrlAbort(c) {
					state = ctrlNormal
					again = true
				} else if c >= 0x30 && c <= 0x7e {
					state = ctrlNormal
				}
			}
		}
	}
	return output.String()
}

func ctrlAbort(c byte) bool {
	return c == '\n' || c == 0x18 || c == 0x1a
}
