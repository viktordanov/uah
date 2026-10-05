// Package wrap wraps text into lines; see docs/wrap.md.
package wrap

import "strings"

// Lines wraps s into lines of at most width bytes.
func Lines(s string, width int) []string {
	words := strings.Fields(s)
	if len(words) == 0 {
		return nil
	}
	if width < 1 {
		return []string{strings.Join(words, " ")}
	}
	var lines []string
	cur := ""
	for _, w := range words {
		switch {
		case cur == "":
			cur = w
		case len(cur)+1+len(w) <= width:
			cur += " " + w
		default:
			lines = append(lines, cur)
			cur = w
		}
	}

	return append(lines, cur)
}
