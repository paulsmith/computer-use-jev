package text

import (
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestUnifiedDiff(t *testing.T) {
	for _, test := range []struct{ old, new, want string }{
		{"hello\nworld\n", "hello\nthere\n", "--- a/foo\n+++ b/foo\n@@ -1,2 +1,2 @@\n hello\n-world\n+there\n"},
		{"a\n", "b\n", "--- a/foo\n+++ b/foo\n@@ -1 +1 @@\n-a\n+b\n"},
		{"", "alpha\nbeta\n", "--- /dev/null\n+++ b/new.txt\n@@ -0,0 +1,2 @@\n+alpha\n+beta\n"},
		{"only line\n", "", "--- a/old\n+++ /dev/null\n@@ -1 +0,0 @@\n-only line\n"},
		{"a\nb", "a\nb\n", "--- a/foo\n+++ b/foo\n@@ -1,2 +1,2 @@\n a\n-b\n\\ No newline at end of file\n+b\n"},
		{"a\nb\n", "a\nb", "--- a/foo\n+++ b/foo\n@@ -1,2 +1,2 @@\n a\n-b\n+b\n\\ No newline at end of file\n"},
		{"a\nz", "b\nz", "--- a/foo\n+++ b/foo\n@@ -1,2 +1,2 @@\n-a\n+b\n z\n\\ No newline at end of file\n"},
		{"void a()\n{\n}\n\nvoid c()\n{\n}\n", "void a()\n{\n}\n\nvoid b()\n{\n}\n\nvoid c()\n{\n}\n", "--- a/foo\n+++ b/foo\n@@ -2,6 +2,10 @@\n {\n }\n \n+void b()\n+{\n+}\n+\n void c()\n {\n }\n"},
	} {
		a, b := labels(test.want)
		if got := UnifiedDiff([]byte(test.old), []byte(test.new), a, b); got != test.want {
			t.Errorf("UnifiedDiff = %q, want %q", got, test.want)
		}
	}
	if got := UnifiedDiff([]byte("x\n"), []byte("x\n"), "a/f", "b/f"); got != "" {
		t.Errorf("identical = %q", got)
	}
}

func labels(want string) (string, string) {
	lines := strings.Split(want, "\n")
	return strings.TrimPrefix(lines[0], "--- "), strings.TrimPrefix(lines[1], "+++ ")
}

func TestUnifiedDiffSanitizesAndStaysSparse(t *testing.T) {
	got := UnifiedDiff([]byte{'a', 0, 0xff, 'b', '\n'}, []byte("abc\n"), "a/f", "b/f")
	if strings.ContainsRune(got, 0) || !utf8.ValidString(got) || strings.Count(got, "�") != 2 {
		t.Fatalf("unsanitized diff %q", got)
	}
	var old, new strings.Builder
	for i := range 40000 {
		old.WriteString("l")
		old.WriteString(itoa(i))
		old.WriteByte('\n')
		if i%2000 == 500 {
			new.WriteString("e")
		} else {
			new.WriteString("l")
		}
		new.WriteString(itoa(i))
		new.WriteByte('\n')
	}
	got = UnifiedDiff([]byte(old.String()), []byte(new.String()), "a/f", "b/f")
	if strings.Count(got, "@@ -") != 20 || !strings.Contains(got, "@@ -498,7 +498,7 @@\n") || len(got) >= 4096 {
		t.Fatalf("sparse diff was not local: %d hunks, %d bytes", strings.Count(got, "@@ -"), len(got))
	}
}

func TestUnifiedDiffBudgetedRewrite(t *testing.T) {
	var old, new strings.Builder
	for i := range 1500 {
		old.WriteString("old" + itoa(i) + "\n")
		new.WriteString("new" + itoa(i) + "\n")
	}
	got := UnifiedDiff([]byte(old.String()), []byte(new.String()), "a/f", "b/f")
	if strings.Count(got, "@@ -") != 1 || !strings.Contains(got, "@@ -1,1500 +1,1500 @@\n") || !strings.Contains(got, "-old1499\n") || !strings.Contains(got, "+new1499\n") {
		t.Fatalf("rewrite diff was not one replacement hunk")
	}
}

func itoa(n int) string { return string(strconv.AppendInt(nil, int64(n), 10)) }
