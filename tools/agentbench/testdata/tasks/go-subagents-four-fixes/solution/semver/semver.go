// Package semver compares semantic versions.
package semver

import (
	"strconv"
	"strings"
)

// Compare returns -1, 0, or 1 as version a is older than, equal to, or
// newer than b. Versions look like 1.2.3 or 1.2.3-rc.1 (a leading v is
// allowed).
func Compare(a, b string) int {
	a, b = strings.TrimPrefix(a, "v"), strings.TrimPrefix(b, "v")
	ca, pra, _ := strings.Cut(a, "-")
	cb, prb, _ := strings.Cut(b, "-")
	if c := compareIDs(strings.Split(ca, "."), strings.Split(cb, ".")); c != 0 {
		return c
	}
	switch {
	case pra == prb:
		return 0
	case pra == "":
		return 1
	case prb == "":
		return -1
	}

	return compareIDs(strings.Split(pra, "."), strings.Split(prb, "."))
}

func compareIDs(a, b []string) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		na, ea := strconv.Atoi(a[i])
		nb, eb := strconv.Atoi(b[i])
		switch {
		case ea == nil && eb == nil:
			if na != nb {
				return cmp(na < nb)
			}
		case ea == nil:
			return -1
		case eb == nil:
			return 1
		case a[i] != b[i]:
			return cmp(a[i] < b[i])
		}
	}
	switch {
	case len(a) < len(b):
		return -1
	case len(a) > len(b):
		return 1
	}

	return 0
}

func cmp(less bool) int {
	if less {
		return -1
	}

	return 1
}
