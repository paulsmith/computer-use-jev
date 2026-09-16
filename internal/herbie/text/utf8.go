// SPDX-License-Identifier: MIT
package text

import (
	"unicode"
	"unicode/utf8"
)

// utf8SeqLen returns the sequence length encoded by a lead byte, 1 if it is not a valid lead.
func utf8SeqLen(c byte) int {
	if c < 0x80 {
		return 1
	}
	if c >= 0xc2 && c <= 0xdf {
		return 2
	}
	if c >= 0xe0 && c <= 0xef {
		return 3
	}
	if c >= 0xf0 && c <= 0xf4 {
		return 4
	}
	return 1
}
func utf8IsValid(s []byte) bool { return utf8.Valid(s) }

// utf8SeqValid reports whether s starts with a well-formed sequence of exactly n bytes.
func utf8SeqValid(s []byte, n int) bool {
	if n < 1 || n > utf8.UTFMax || len(s) < n {
		return false
	}
	r, size := utf8.DecodeRune(s[:n])
	return size == n && (r != utf8.RuneError || n == 3)
}
func utf8Next(s []byte, i int) int {
	if i >= len(s) {
		return len(s)
	}
	_, size := utf8.DecodeRune(s[i:])
	return i + size
}
func UTF8Prev(s []byte, i int) int {
	if i <= 0 {
		return 0
	}
	_, size := utf8.DecodeLastRune(s[:i])
	return i - size
}

// TruncateUTF8 returns at most limit bytes, backing up to a UTF-8 rune boundary.
func TruncateUTF8(b []byte, limit int) []byte {
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

func dangerous(r rune) bool {
	return r == 0xad || r == 0x34f || r == 0x61c || r == 0x115f || r == 0x1160 || r == 0x180e || r >= 0x200b && r <= 0x200f || r >= 0x2028 && r <= 0x202e || r >= 0x2060 && r <= 0x206f || r == 0x3164 || r == 0xfeff || r == 0xffa0 || r >= 0xfff9 && r <= 0xfffb || r >= 0xe0000 && r <= 0xe007f
}
func wide(r rune) bool {
	return r >= 0x1100 && (r <= 0x115f || r >= 0x231a && r <= 0x231b || r == 0x2329 || r == 0x232a || r >= 0x2e80 && r <= 0xa4cf && r != 0x303f || r >= 0xac00 && r <= 0xd7a3 || r >= 0xf900 && r <= 0xfaff || r >= 0xfe10 && r <= 0xfe19 || r >= 0xfe30 && r <= 0xfe6f || r >= 0xff00 && r <= 0xff60 || r >= 0xffe0 && r <= 0xffe6 || r >= 0x1f300 && r <= 0x1faff && r != 0x1f5a5 || r >= 0x20000 && r <= 0x3fffd)
}

// UTF8CodepointCells returns -1 for malformed, controls, and dangerous codepoints.
func UTF8CodepointCells(s []byte, i int) (cells, consumed int) {
	if i >= len(s) {
		return 0, 0
	}
	r, n := utf8.DecodeRune(s[i:])
	if r == utf8.RuneError && n == 1 {
		return -1, 1
	}
	if dangerous(r) || r < 0x20 || r >= 0x7f && r <= 0x9f {
		return -1, n
	}
	if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) || unicode.Is(unicode.Mc, r) {
		return 0, n
	}
	if unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Cn, r) {
		return -1, n
	}
	if wide(r) {
		return 2, n
	}
	return 1, n
}

type UTF8Stream struct {
	buf  [4]byte
	have int
}

func (s *UTF8Stream) Reset() { s.have = 0 }
func (s *UTF8Stream) Byte(c byte) (out []byte, cells int, ok bool) {
	if s.have == 0 {
		s.buf[0] = c
		s.have = 1
		if utf8SeqLen(c) != 1 {
			return nil, 0, false
		}
		out = s.buf[:1]
		cells, _ = UTF8CodepointCells(out, 0)
		if cells < 0 {
			cells = 1
		}
		s.have = 0
		return out, cells, true
	}
	if c&0xc0 != 0x80 {
		s.buf[s.have] = c
		n := s.have
		s.have = 0
		// One U+FFFD for the invalid prefix, plus the re-sync byte if it
		// renders immediately; a sequence leader is only buffered.
		cells = 1
		if utf8SeqLen(c) == 1 {
			cells = 2
		}
		return s.buf[:n+1], cells, true
	}
	s.buf[s.have] = c
	s.have++
	need := utf8SeqLen(s.buf[0])
	if s.have < need {
		return nil, 0, false
	}
	out = s.buf[:s.have]
	if !utf8SeqValid(out, need) {
		cells = 1
	} else {
		cells, _ = UTF8CodepointCells(out, 0)
		if cells < 0 {
			cells = 1
		}
	}
	s.have = 0
	return out, cells, true
}
func (s *UTF8Stream) Flush() (out []byte, cells int, ok bool) {
	if s.have == 0 {
		return nil, 0, false
	}
	out = s.buf[:s.have]
	cells = 1
	s.have = 0
	return out, cells, true
}
