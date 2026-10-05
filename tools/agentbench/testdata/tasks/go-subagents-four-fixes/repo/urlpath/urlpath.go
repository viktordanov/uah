// Package urlpath builds URL paths.
package urlpath

import "strings"

// Join joins path segments with single slashes. The result starts with a
// slash, and keeps a trailing slash when the last segment has one.
func Join(segments ...string) string {
	return "/" + strings.TrimPrefix(strings.Join(segments, "/"), "/")
}
