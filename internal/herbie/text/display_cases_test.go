package text

import (
	"strings"
	"testing"
)

func TestWidthClassifierCases(t *testing.T) {
	for _, tc := range []struct {
		name string
		s    string
		want int
	}{
		{"watch", "⌚", 2},
		{"desktop computer", "🖥", 1},
		{"emoji", "🦀", 2},
		{"cjk", "界", 2},
		{"spacing combining mark", "ा", 0}, // U+093E
		{"combining mark", "\u0301", 0},
		{"format", "\u2063", -1},
		{"unassigned", "\u0378", -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, consumed := UTF8CodepointCells([]byte(tc.s), 0)
			if got != tc.want || consumed != len(tc.s) {
				t.Fatalf("UTF8CodepointCells(%U) = %d, %d; want %d, %d", []rune(tc.s)[0], got, consumed, tc.want, len(tc.s))
			}
		})
	}
}

func TestFlattenForDisplayCases(t *testing.T) {
	marks := "a" + strings.Repeat("\u0301", 100)
	for _, tc := range []struct{ in, want string }{
		{"", ""},
		{"ls -la", "ls -la"},
		{"ls\npwd", "ls pwd"},
		{"a\n\n\tb  \r\n c", "a b c"},
		{"\n  hello world\n\n", "hello world"},
		{"  \n\t\r  ", ""},
		{"a\x01\x02\x03b\x7fc", "a b c"},
		{"café\nlatte", "café latte"},
		{"ab\xe2\x80\xaecd", "ab?cd"},
		{"ab\xe2\x80\x8dcd", "ab?cd"},
		{"ab\x80cd", "ab?cd"},
		{"a\u0301\u0301\u0301", "a\u0301\u0301\u0301"},
		{marks, "a" + strings.Repeat("\u0301", 8)},
	} {
		if got := FlattenForDisplay(tc.in); got != tc.want {
			t.Errorf("FlattenForDisplay(%x) = %x; want %x", tc.in, got, tc.want)
		}
	}
}

func TestTruncateForDisplayCases(t *testing.T) {
	for _, tc := range []struct {
		in   string
		max  int
		want string
	}{
		{"hello", 10, "hello"}, {"hello", 5, "hello"}, {"hello world", 8, "hello..."},
		{"hello", 3, "hel"}, {"café latte", 5, "ca..."}, {"café latte", 6, "caf..."},
		{"café", 4, "café"}, {"abcd\u0301", 4, "abcd\u0301"}, {"🦀abc", 4, "..."}, {"🦀abc", 5, "🦀abc"},
	} {
		if got := TruncateForDisplay(tc.in, tc.max); got != tc.want {
			t.Errorf("TruncateForDisplay(%q, %d) = %q; want %q", tc.in, tc.max, got, tc.want)
		}
	}
}

func TestWrapBreakPosCases(t *testing.T) {
	for _, tc := range []struct {
		in        string
		width     int
		end, next int
	}{
		{"hello", 10, 5, 5}, {"hello world", 10, 5, 6}, {"hello world", 5, 5, 6},
		{"helloworldmore stuff", 10, 10, 10}, {"hi  more", 5, 2, 4}, {"café world", 4, 5, 6},
		{"abcd\u0303ef", 4, 6, 6}, {"🦀abc", 1, 4, 4},
	} {
		end, next := wrapBreakPos(tc.in, tc.width)
		if end != tc.end || next != tc.next {
			t.Errorf("WrapBreakPos(%q, %d) = %d, %d; want %d, %d", tc.in, tc.width, end, next, tc.end, tc.next)
		}
	}
}

func TestDisplayCellsCases(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int
	}{{"abc", 3}, {"", 0}, {"café", 4}, {"a中", 3}, {"e\u0301", 1}, {"⌚", 2}, {"🖥", 1}} {
		if got := DisplayCells(tc.in); got != tc.want {
			t.Errorf("DisplayCells(%q) = %d; want %d", tc.in, got, tc.want)
		}
	}
}

func TestReflowForDisplayCases(t *testing.T) {
	for _, tc := range []struct {
		in                 string
		first, other, rows int
		reserve            int
		want               string
	}{
		{"short", 80, 80, 3, 0, "short"},
		{"hello world more", 10, 10, 2, 0, "hello\nworld more"},
		{"hello world more here", 10, 10, 2, 0, "hello\nworld..."},
		{"abcdefghijklmnopqrstuvwxyz", 10, 10, 2, 0, "abcdefghij\nklmnopq..."},
		{strings.Repeat("a", 40), 10, 10, 3, 0, "aaaaaaaaaa\naaaaaaaaaa\naaaaaaa..."},
		{strings.Repeat("a", 30), 10, 10, 3, 0, "aaaaaaaaaa\naaaaaaaaaa\naaaaaaaaaa"},
		{"hello brave new world", 5, 10, 2, 0, "hello\nbrave..."},
		{"界xxx", 4, 4, 1, 0, "..."},
		{"abcdef", 10, 10, 3, 5, "abcde\nf"},
		{"hello world", 11, 11, 1, 4, "hell..."},
		{"", 80, 80, 3, 0, ""},
	} {
		if got := ReflowForDisplay(tc.in, tc.first, tc.other, tc.rows, tc.reserve); got != tc.want {
			t.Errorf("ReflowForDisplay(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
}

func TestReflowLongCommand(t *testing.T) {
	cmd := "find . -type f -name '*.c' -not -path './build/*' -not -path './build-asan/*' | xargs grep -l 'TODO' | head -20 | while read f; do echo \"== $f ==\"; grep -n TODO \"$f\"; done"
	out := ReflowForDisplay(cmd, 93, 100, 3, 0)
	rows := strings.Split(out, "\n")
	if len(rows) > 3 || !strings.HasPrefix(out, "find . -type f") {
		t.Fatalf("unexpected long-command reflow: %q", out)
	}
	for i, row := range rows {
		limit := 100
		if i == 0 {
			limit = 93
		}
		if DisplayCells(row) > limit {
			t.Errorf("row %d has %d cells, limit %d", i, DisplayCells(row), limit)
		}
	}
	out = ReflowForDisplay("find . -type f -name '*.c' -not -path './build/*' | xargs grep -l TODO | head | while read f; do echo $f; done", 33, 40, 2, 0)
	if strings.Count(out, "\n") != 1 || !strings.HasSuffix(out, "...") || !strings.HasPrefix(out, "find . -type f") {
		t.Fatalf("unexpected truncated command reflow: %q", out)
	}
}
