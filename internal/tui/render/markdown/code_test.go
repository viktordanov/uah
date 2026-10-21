package markdown

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
)

// codePieces keeps every character and the styles in force on each piece,
// and splits a long line in one pass: a 300,000-character line, such as
// minified JSON, takes far less than a second.
func TestCodePieces(t *testing.T) {
	line := "\x1b[31mabcdefghij\x1b[39m \x1b[32mklmnop  qr\x1b[39m"
	pieces := codePieces(line, 8)
	var joined strings.Builder
	for _, p := range pieces {
		assert.LessOrEqual(t, ansi.StringWidth(p), 8)
		joined.WriteString(ansi.Strip(p))
	}
	assert.Equal(t, ansi.Strip(line), joined.String(), "every character, spaces too")
	assert.True(t, strings.HasPrefix(pieces[1], "\x1b[31m"), "the red goes on: %q", pieces[1])

	wide := codePieces(strings.Repeat("a", 7)+"界b", 8)
	assert.Equal(t, []string{"aaaaaaa", "界b"}, wide, "a wide character crossing the end starts the next piece")

	long := strings.Repeat(`{"key":"value","n":12345},`, 12_000) // 300,000 characters
	started := time.Now()
	pieces = codePieces("\x1b[33m"+long+"\x1b[39m", 77)
	assert.Less(t, time.Since(started), time.Second, "one pass, not one per piece")
	assert.Len(t, pieces, (len(long)+76)/77)
}
