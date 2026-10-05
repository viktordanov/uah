// Package urlpath builds URL paths.
package urlpath

import "strings"

// Join joins path segments with single slashes. The result starts with a
// slash, and keeps a trailing slash when the last segment has one.
func Join(segments ...string) string {
	var parts []string
	for _, s := range segments {
		for _, p := range strings.Split(s, "/") {
			if p != "" {
				parts = append(parts, p)
			}
		}
	}
	out := "/" + strings.Join(parts, "/")
	if n := len(segments); n > 0 && strings.HasSuffix(segments[n-1], "/") && out != "/" {
		out += "/"
	}

	return out
}
