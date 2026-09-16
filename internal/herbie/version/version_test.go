package version

import "testing"

func TestIdentity(t *testing.T) {
	for _, test := range []struct {
		release, revision, want string
		dirty                   bool
	}{
		{"v1.2.3", "abc", "v1.2.3", false},
		{"v1.2.3", "abc", "v1.2.3+", true},
		{"", "abc", "abc", false},
		{"", "abc", "abc+", true},
		{"", "", "devel", false},
	} {
		if got := identity(test.release, test.revision, test.dirty); got != test.want {
			t.Errorf("identity(%q, %q, %t) = %q, want %q", test.release, test.revision, test.dirty, got, test.want)
		}
	}
}
