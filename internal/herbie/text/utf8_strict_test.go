package text

import (
	"bytes"
	"testing"
)

func TestUTF8StrictNavigation(t *testing.T) {
	for _, tc := range []struct {
		in       []byte
		next, at int
	}{
		{[]byte("abc"), 1, 0}, {[]byte("abc"), 2, 1}, {[]byte("abc"), 3, 2},
		{[]byte("caf\xc3\xa9"), 5, 3}, {[]byte("\xe2\x80"), 1, 0},
		{[]byte("\xc3z"), 1, 0}, {[]byte("\x80\x80"), 1, 0},
		{[]byte("\xc0\x80"), 1, 0},
	} {
		if got := utf8Next(tc.in, tc.at); got != tc.next {
			t.Errorf("next %x at %d: %d", tc.in, tc.at, got)
		}
	}
	for _, tc := range []struct {
		in       []byte
		at, want int
	}{
		{[]byte("abc"), 3, 2}, {[]byte("abc"), 2, 1}, {[]byte("abc"), 1, 0},
		{[]byte("caf\xc3\xa9"), 5, 3}, {[]byte("caf\xc3\xa9"), 3, 2},
		{[]byte("\x80\x80"), 2, 1}, {[]byte("\xc0\x80"), 2, 1}, {[]byte("\xc3\xc3"), 2, 1},
	} {
		if got := UTF8Prev(tc.in, tc.at); got != tc.want {
			t.Errorf("prev %x at %d: %d", tc.in, tc.at, got)
		}
	}
	if utf8Next([]byte("ab"), 2) != 2 || utf8Next([]byte("ab"), 100) != 2 || UTF8Prev([]byte("ab"), 0) != 0 {
		t.Fatal("bounds")
	}
}

func TestUTF8SanitizerStrictFixtures(t *testing.T) {
	fffd := []byte("�")
	for _, tc := range []struct{ in, want []byte }{
		{[]byte("hello world\n"), []byte("hello world\n")},
		{[]byte("\xc3\xa9\xe4\xb8\xad\xf0\x9d\x93\x90"), []byte("\xc3\xa9\xe4\xb8\xad\xf0\x9d\x93\x90")},
		{[]byte{0xa0}, fffd}, {[]byte{0xff}, fffd}, {[]byte{0xe4}, fffd},
		{[]byte{0xc0, 0x80}, bytes.Repeat(fffd, 2)}, {[]byte{0xe0, 0x80, 0x80}, bytes.Repeat(fffd, 3)},
		{[]byte{0xed, 0xa0, 0x80}, bytes.Repeat(fffd, 3)}, {[]byte{0xf4, 0x90, 0x80, 0x80}, bytes.Repeat(fffd, 4)},
		{[]byte{'a', 0, 'b'}, append(append([]byte("a"), fffd...), 'b')}, {[]byte("\xc2 "), append(append([]byte{}, fffd...), ' ')},
	} {
		if got := []byte(SanitizeUTF8(tc.in)); !bytes.Equal(got, tc.want) {
			t.Errorf("sanitize %x: %x want %x", tc.in, got, tc.want)
		}
	}
}
