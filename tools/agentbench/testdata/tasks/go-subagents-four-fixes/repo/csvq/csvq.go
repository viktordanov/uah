// Package csvq writes CSV fields.
package csvq

import "strings"

// Field quotes a value for a CSV line when it needs quoting.
func Field(v string) string {
	if strings.ContainsAny(v, ",\"") {
		return `"` + v + `"`
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
