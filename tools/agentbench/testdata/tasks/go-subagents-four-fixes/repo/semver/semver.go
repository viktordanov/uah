// Package semver compares semantic versions.
package semver

import "strings"

// Compare returns -1, 0, or 1 as version a is older than, equal to, or
// newer than b. Versions look like 1.2.3 or 1.2.3-rc.1 (a leading v is
// allowed).
func Compare(a, b string) int {
	a, b = strings.TrimPrefix(a, "v"), strings.TrimPrefix(b, "v")
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			if pa[i] < pb[i] {
				return -1
			}

			return 1
		}
	}

	return 0
}
