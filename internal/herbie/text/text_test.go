package text

import "testing"

func TestUTF8Structural(t *testing.T) {
	for _, tc := range []struct {
		byte byte
		want int
	}{{'a', 1}, {0, 1}, {0x7f, 1}, {0xc2, 2}, {0xdf, 2}, {0xe0, 3}, {0xef, 3}, {0xf0, 4}, {0xf4, 4}, {0x80, 1}, {0xbf, 1}, {0xc0, 1}, {0xc1, 1}, {0xf5, 1}, {0xf8, 1}, {0xff, 1}} {
		if got := utf8SeqLen(tc.byte); got != tc.want {
			t.Errorf("length %x: %d", tc.byte, got)
		}
	}
	for _, s := range [][]byte{[]byte("é"), []byte("—"), []byte("🦀"), {0xf4, 0x8f, 0xbf, 0xbf}} {
		if !utf8SeqValid(s, len(s)) {
			t.Errorf("invalid %x", s)
		}
	}
	for _, s := range [][]byte{{0xc0, 0x80}, {0xe0, 0x80, 0x80}, {0xf0, 0x80, 0x80, 0x80}, {0xe0, 0x9f, 0xbf}, {0xed, 0xa0, 0x80}, {0xed, 0xbf, 0xbf}, {0xf4, 0x90, 0x80, 0x80}, {0xbf, 0xbf}, {0xc3, 0xa9, 0xa9}, {0xe2, 0x80}} {
		if utf8SeqValid(s, len(s)) {
			t.Errorf("accepted %x", s)
		}
	}
	for _, tc := range []struct {
		s    []byte
		want bool
	}{{nil, true}, {[]byte("plain"), true}, {[]byte{'a', 0, 'b'}, true}, {[]byte("é—"), true}, {[]byte{0xc3}, false}, {[]byte{0xc0, 0x80}, false}, {[]byte{0xed, 0xa0, 0x80}, false}} {
		if got := utf8IsValid(tc.s); got != tc.want {
			t.Errorf("buffer %x: %t", tc.s, got)
		}
	}
	s := []byte("caf\xc3\xa9")
	if utf8Next(s, 3) != 5 || UTF8Prev(s, 5) != 3 {
		t.Fatal("navigation")
	}
	if utf8Next([]byte{0xc0, 0x80}, 0) != 1 || UTF8Prev([]byte{0xc0, 0x80}, 2) != 1 {
		t.Fatal("malformed navigation")
	}
}
func TestTruncateUTF8(t *testing.T) {
	for _, tc := range []struct {
		in    string
		limit int
		want  string
	}{
		{"", 1, ""}, {"abc", -1, ""}, {"abc", 0, ""}, {"abc", 5, "abc"}, {"café", 4, "caf"}, {"🦀abc", 3, ""},
	} {
		if got := string(TruncateUTF8([]byte(tc.in), tc.limit)); got != tc.want {
			t.Errorf("TruncateUTF8(%q, %d) = %q, want %q", tc.in, tc.limit, got, tc.want)
		}
	}
}

func TestUTF8CellsAndStream(t *testing.T) {
	for _, tc := range []struct {
		s       []byte
		want, n int
	}{{[]byte("a"), 1, 1}, {[]byte("🦀"), 2, 4}, {[]byte("\xcc\x81"), 0, 2}, {[]byte{1}, -1, 1}, {[]byte("\xe2\x80\xae"), -1, 3}, {[]byte{0xc3}, -1, 1}} {
		got, n := UTF8CodepointCells(tc.s, 0)
		if got != tc.want || n != tc.n {
			t.Errorf("%x: %d,%d", tc.s, got, n)
		}
	}
	var st UTF8Stream
	if _, _, ok := st.Byte(0xc3); ok {
		t.Fatal("early stream output")
	}
	out, cells, ok := st.Byte(0xa9)
	if !ok || string(out) != "é" || cells != 1 {
		t.Fatal("stream utf8")
	}
	st.Reset()
	st.Byte(0xc3)
	out, cells, ok = st.Byte('a')
	if !ok || len(out) != 2 || cells != 2 {
		t.Fatal("stream malformed")
	}
	for _, b := range []byte{0xc0, 0xf5} {
		out, cells, ok = st.Byte(b)
		if !ok || len(out) != 1 || out[0] != b || cells != 1 {
			t.Errorf("impossible leader %x: %x, %d, %t", b, out, cells, ok)
		}
	}
	st.Reset()
	st.Byte(0xed)
	st.Byte(0xa0)
	out, cells, ok = st.Byte(0x80)
	if !ok || len(out) != 3 || cells != 1 {
		t.Fatal("invalid scalar stream")
	}
	st.Reset()
	st.Byte(0xe2)
	st.Byte(0x80)
	out, cells, ok = st.Flush()
	if !ok || len(out) != 2 || cells != 1 {
		t.Fatal("stream flush")
	}
	if _, _, ok := st.Flush(); ok {
		t.Fatal("second stream flush")
	}
	st.Byte(0xe2)
	st.Reset()
	out, cells, ok = st.Byte('a')
	if !ok || string(out) != "a" || cells != 1 {
		t.Fatal("stream reset")
	}
}
func TestSanitize(t *testing.T) {
	cases := []struct{ in, want string }{{"hello", "hello"}, {"a\x00b", "a�b"}, {"\xc0\x80", "��"}, {"\xed\xa0\x80", "���"}, {"\xf4\x90\x80\x80", "����"}, {"\xc3", "�"}, {"\xc3A", "�A"}}
	for _, tc := range cases {
		if got := SanitizeUTF8([]byte(tc.in)); got != tc.want {
			t.Errorf("%x: %x", tc.in, got)
		}
	}
}
func TestWidth(t *testing.T) {
	for _, tc := range []struct {
		s string
		n int
	}{{"abc", 3}, {"café", 4}, {"a中", 3}, {"e\xcc\x81", 1}, {"\x80", 1}, {"\x1b[1mabc\x1b[22m", 3}} {
		if got := DisplayCells(tc.s); got != tc.n {
			t.Errorf("cells %q: %d", tc.s, got)
		}
	}
	for _, tc := range []struct {
		s    string
		n    int
		want string
	}{{"hello world", 8, "hello..."}, {"hello", 3, "hel"}, {"café latte", 5, "ca..."}, {"🦀abc", 4, "..."}, {"abcd\xcc\x81", 4, "abcd\xcc\x81"}} {
		if got := TruncateForDisplay(tc.s, tc.n); got != tc.want {
			t.Errorf("truncate: %q", got)
		}
	}
}
func TestWrapReflowFlatten(t *testing.T) {
	end, next := wrapBreakPos("hello world", 5)
	if end != 5 || next != 6 {
		t.Fatal(end, next)
	}
	end, next = wrapBreakPos("abcd\xcc\x83ef", 4)
	if end != 6 || next != 6 {
		t.Fatal(end, next)
	}
	end, next = wrapBreakPos("🦀abc", 1)
	if end != 4 || next != 4 {
		t.Fatal(end, next)
	}
	for _, tc := range []struct{ in, want string }{{"hello world more", "hello\nworld more"}, {"hello world more here", "hello\nworld..."}, {"abcdefghijklmnopqrstuvwxyz", "abcdefghij\nklmnopq..."}} {
		if got := ReflowForDisplay(tc.in, 10, 10, 2, 0); got != tc.want {
			t.Errorf("reflow: %q", got)
		}
	}
	for _, tc := range []struct{ in, want string }{{"a\n\n\tb  \r\n c", "a b c"}, {"\n  hello world\n\n", "hello world"}, {"ab\xe2\x80\xaecd", "ab?cd"}, {"ab\x80cd", "ab?cd"}} {
		if got := FlattenForDisplay(tc.in); got != tc.want {
			t.Errorf("flatten %x: %q", tc.in, got)
		}
	}
}

func TestWrapRowBytes(t *testing.T) {
	for _, tc := range []struct {
		in       string
		width    int
		row, sep int
	}{{"hello", 10, 5, 0}, {"hello world", 10, 5, 1}, {"helloworld", 6, 6, 0}, {"hi\nthere", 10, 2, 1}, {"\nrest", 10, 0, 1}} {
		row, sep := WrapRowBytes(tc.in, tc.width)
		if row != tc.row || sep != tc.sep {
			t.Errorf("WrapRowBytes(%q, %d) = %d, %d; want %d, %d", tc.in, tc.width, row, sep, tc.row, tc.sep)
		}
	}
}

func TestUTF8StreamInvalidOneCell(t *testing.T) {
	// Malformed input must count one cell per invalid sequence, matching the
	// single U+FFFD the terminal renders for it.
	for _, tc := range []struct {
		in   []byte
		want int
	}{{[]byte("\xed\xa0\x80"), 1}, {[]byte{0xf4, 0x90, 0x80, 0x80}, 1}, {[]byte{0xe0, 0x80, 'x'}, 2}, {[]byte{0xe2, 0x80}, 1}, {[]byte{0xe0, 0x9f, 0xbf}, 1}} {
		var st UTF8Stream
		cells := 0
		for _, b := range tc.in {
			if _, c, ok := st.Byte(b); ok {
				cells += c
			}
		}
		if _, c, ok := st.Flush(); ok {
			cells += c
		}
		if cells != tc.want {
			t.Errorf("%x: cells %d, want %d", tc.in, cells, tc.want)
		}
	}
}
