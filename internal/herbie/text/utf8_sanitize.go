// SPDX-License-Identifier: MIT
package text

import "strings"

func SanitizeUTF8(data []byte) string {
	return strings.Map(func(r rune) rune {
		if r == 0 {
			return '\uFFFD'
		}
		return r
	}, string(data))
}
