package text

import "slices"

// IsIdentName reports whether s is nonempty and contains only ASCII letters,
// digits, or one of the supplied extra runes.
func IsIdentName(s string, extras ...rune) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !isIdentRune(r, extras...) {
			return false
		}
	}
	return true
}

func isIdentRune(r rune, extras ...rune) bool {
	if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
		return true
	}
	return slices.Contains(extras, r)
}
