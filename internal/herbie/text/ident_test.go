package text

import "testing"

func TestIsIdentName(t *testing.T) {
	for _, tc := range []struct {
		name   string
		input  string
		extras []rune
		want   bool
	}{
		{"empty", "", nil, false},
		{"lowercase", "a", nil, true},
		{"uppercase", "Z", nil, true},
		{"digit", "7", nil, true},
		{"underscore", "name_value", []rune{'_'}, true},
		{"dash", "name-value", []rune{'-'}, true},
		{"dot", "name.value", []rune{'.'}, true},
		{"space", "name value", []rune{'_', '-', '.'}, false},
		{"dash not allowed", "name-value", []rune{'_'}, false},
		{"unicode letter", "café", []rune{'_', '-', '.'}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsIdentName(tc.input, tc.extras...); got != tc.want {
				t.Fatalf("IsIdentName(%q, %q) = %t, want %t", tc.input, string(tc.extras), got, tc.want)
			}
		})
	}
}
