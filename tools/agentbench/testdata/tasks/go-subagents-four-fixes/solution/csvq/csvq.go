// Package csvq writes CSV fields.
package csvq

import "strings"

// Field quotes a value for a CSV line when it needs quoting, doubling any
// quotes inside it (RFC 4180).
func Field(v string) string {
	if strings.ContainsAny(v, ",\"\r\n") {
		return `"` + strings.ReplaceAll(v, `"`, `""`) + `"`
	}

	return v
}

// Line joins fields into one CSV line.
func Line(fields []string) string {
	out := make([]string, len(fields))
	for i, f := range fields {
		out[i] = Field(f)
	}

	return strings.Join(out, ",")
}
