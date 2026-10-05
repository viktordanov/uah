package wrap

import (
	"slices"
	"testing"
)

func TestHiddenLines(t *testing.T) {
	cases := []struct {
		in    string
		width int
		want  []string
	}{
		{"the quick brown fox", 10, []string{"the quick", "brown fox"}},
		{"a supercalifragilistic word", 5, []string{"a", "supercalifragilistic", "word"}},
		{"  spaced   out\ttext ", 0, []string{"spaced out text"}},
		{"   ", 10, nil},
		{"one two three", 7, []string{"one two", "three"}},
	}
	for _, c := range cases {
		if got := Lines(c.in, c.width); !slices.Equal(got, c.want) {
			t.Errorf("Lines(%q, %d) = %q, want %q", c.in, c.width, got, c.want)
		}
	}
}
