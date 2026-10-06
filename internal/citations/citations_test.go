package citations_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/viktordanov/uah/internal/citations"
)

func TestStrip(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"none", "plain text", "plain text"},
		{"at the end", "Done. citeturn2view0", "Done."},
		{"before a period", "It works citeturn0search1turn0search4.", "It works."},
		{"mid sentence", "See citeturn1view0 the docs", "See the docs"},
		{"file citation", "Per the filefileciteturn0file0, yes", "Per the file, yes"},
		{"streaming, not closed yet", "Almost citetur", "Almost "},
		{"stray marks", "abc", "abc"},
		{"two", "A citex and B citey.", "A and B."},
	} {
		assert.Equal(t, tc.want, citations.Strip(tc.in), tc.name)
	}
}
