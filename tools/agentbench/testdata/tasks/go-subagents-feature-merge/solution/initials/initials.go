// Package initials gives a name's initials; see docs/initials.md.
package initials

import (
	"strings"
	"unicode"
)

// Of returns the initials of name.
func Of(name string) string {
	var b strings.Builder
	for _, part := range strings.FieldsFunc(name, func(r rune) bool { return r == ' ' || r == '-' }) {
		for _, r := range part {
			b.WriteRune(unicode.ToUpper(r))

			break
		}
	}

	return b.String()
}
