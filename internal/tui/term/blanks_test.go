package term

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEraseTail(t *testing.T) {
	const bg = "\x1b[48;5;236m"
	for _, tc := range []struct {
		name, line, want string
		w                int
	}{
		{"plain blanks dropped", "ab   ", "ab", 10},
		{"before a reset", "ab   \x1b[m", "ab\x1b[m", 10},
		{"background to the end", bg + "ab      ", bg + "ab\x1b[K", 8},
		{"true color", "\x1b[48;2;1;2;3m    ", "\x1b[48;2;1;2;3m\x1b[K", 4},
		{"background opened on the blanks", "x" + bg + "     \x1b[m", "x" + bg + "\x1b[K\x1b[m", 6},
		{"foreground ended first", "\x1b[31mx\x1b[39;48;5;236m      ", "\x1b[31mx\x1b[39;48;5;236m\x1b[K", 7},
		{"short of the row", bg + "ab      ", bg + "ab      ", 12},
		{"too few", bg + "abcde   ", bg + "abcde   ", 8},
		{"with a foreground", "\x1b[31;48;5;236mab      ", "\x1b[31;48;5;236mab      ", 8},
		{"plain with a foreground", "\x1b[31mab   ", "\x1b[31mab   ", 10},
		{"underlined", "\x1b[4mab   ", "\x1b[4mab   ", 10},
		{"a style between blanks", "ab  \x1b[1m  ", "ab  \x1b[1m  ", 10},
		{"a link", "\x1b]8;;file:///a\x1b\\ab\x1b]8;;\x1b\\   ", "\x1b]8;;file:///a\x1b\\ab\x1b]8;;\x1b\\   ", 10},
		{"no blanks", "abc", "abc", 10},
	} {
		assert.Equal(t, tc.want, eraseTail(tc.line, tc.w), tc.name)
	}
}
