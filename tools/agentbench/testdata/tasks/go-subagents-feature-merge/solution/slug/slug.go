// Package slug makes URL slugs; see docs/slug.md.
package slug

import "strings"

// Make returns the URL slug of s.
func Make(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if r < 128 && (r >= 'a' && r <= 'z' || r >= '0' && r <= '9') {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(r)

			continue
		}
		dash = true
	}

	return b.String()
}
