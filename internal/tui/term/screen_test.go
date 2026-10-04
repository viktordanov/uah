package term

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// drawn returns a screen w by h that already shows lines with the cursor
// at cur.
func drawn(t *testing.T, w, h int, lines []string, cur *Cursor) *Screen {
	t.Helper()
	s := NewScreen(w, h)
	require.NotNil(t, s.Frame(lines, cur))

	return s
}

func TestScreenFrameUnchangedIsNil(t *testing.T) {
	lines := []string{"\x1b[1mhead\x1b[m", "body λ", "λ ", "", ""}
	s := drawn(t, 20, 5, lines, NewCursor(2, 3))
	assert.Nil(t, s.Frame(lines, NewCursor(2, 3)))
	assert.Nil(t, s.Frame(append([]string(nil), lines...), NewCursor(2, 3)), "equal lines in a new slice")

	s = drawn(t, 20, 5, lines, nil)
	assert.Nil(t, s.Frame(lines, nil), "hidden cursor")
	// Trailing blank rows are the same as no lines for them.
	assert.Nil(t, s.Frame(lines[:3], nil))
}

func TestScreenFrameCursorOnly(t *testing.T) {
	lines := []string{"one", "two"}
	s := drawn(t, 10, 2, lines, NewCursor(1, 0))

	assert.Equal(t, "\x1b[2;4H", string(s.Frame(lines, NewCursor(3, 1))), "a move alone")
	assert.Equal(t, "\x1b[?25l", string(s.Frame(lines, nil)), "hide")
	assert.Nil(t, s.Frame(lines, nil))
	assert.Equal(t, "\x1b[1H\x1b[?25h", string(s.Frame(lines, NewCursor(0, 0))), "show at the top left")
	assert.Equal(t, "\x1b[2;10H", string(s.Frame(lines, NewCursor(99, 99))), "clamped to the screen")
	assert.Nil(t, s.Frame(lines, NewCursor(50, 7)), "clamped to the same cell")
}

func TestScreenFrameCursorAroundChanges(t *testing.T) {
	s := drawn(t, 10, 2, []string{"one", "two"}, NewCursor(1, 0))
	out := string(s.Frame([]string{"one", "twice"}, NewCursor(5, 1)))
	assert.True(t, strings.HasPrefix(out, "\x1b[?25l"), "hidden while drawing: %q", out)
	assert.True(t, strings.HasSuffix(out, "\x1b[2;6H\x1b[?25h"), "placed, then shown: %q", out)

	out = string(s.Frame([]string{"one", "two"}, nil))
	assert.True(t, strings.HasPrefix(out, "\x1b[?25l"), out)
	assert.NotContains(t, out, "\x1b[?25h")

	out = string(s.Frame([]string{"one", "three"}, nil))
	assert.NotContains(t, out, "\x1b[?25", "already hidden")
}

func TestScreenFrameSync(t *testing.T) {
	s := NewScreen(10, 2)
	s.SetSync(true)
	out := string(s.Frame([]string{"a"}, NewCursor(1, 0)))
	assert.True(t, strings.HasPrefix(out, "\x1b[?2026h"), out)
	assert.True(t, strings.HasSuffix(out, "\x1b[?2026l"), out)
	// A cursor move alone is not wrapped.
	assert.Equal(t, "\x1b[2H", string(s.Frame([]string{"a"}, NewCursor(0, 1))))
}

func TestScreenFrameAfterInvalidateRedraws(t *testing.T) {
	lines := []string{"top", "", "\x1b[31mred\x1b[m"}
	s := drawn(t, 10, 3, lines, NewCursor(0, 0))
	s.Invalidate()
	out := string(s.Frame(lines, NewCursor(0, 0)))
	assert.Equal(t, "\x1b[?25l\x1b[m\x1b[H\x1b[2J\x1b[1Htop\x1b[m\x1b[3H\x1b[31mred\x1b[m\x1b[m\x1b[1H\x1b[?25h", out)

	s.Resize(5, 2)
	w, h := s.Size()
	assert.Equal(t, [2]int{5, 2}, [2]int{w, h})
	out = string(s.Frame(lines, nil))
	assert.True(t, strings.HasPrefix(out, "\x1b[?25l\x1b[m\x1b[H\x1b[2J"), out)
}

// Regression: after another program used the terminal its cursor may show,
// so a frame without a cursor must hide it, though the last frame did.
func TestScreenFrameAfterInvalidateHidesCursor(t *testing.T) {
	s := drawn(t, 10, 2, []string{"a"}, nil)
	s.Invalidate()
	assert.True(t, strings.HasPrefix(string(s.Frame([]string{"a"}, nil)), "\x1b[?25l"))

	// A new screen does not know either.
	assert.True(t, strings.HasPrefix(string(NewScreen(10, 2).Frame([]string{"a"}, nil)), "\x1b[?25l"))
}

func TestScreenFrameDropsLinesFromTheTop(t *testing.T) {
	s := NewScreen(10, 2)
	out := string(s.Frame([]string{"a", "b", "c"}, nil))
	assert.Equal(t, "\x1b[?25l\x1b[m\x1b[H\x1b[2J\x1b[1Hb\x1b[m\x1b[2Hc\x1b[m", out)
	assert.Nil(t, s.Frame([]string{"x", "b", "c"}, nil), "the dropped line is not shown")
	assert.Equal(t, []string{"b", "c"}, s.shown)
}

func TestScreenFrameRewritesFromTheChange(t *testing.T) {
	s := drawn(t, 20, 1, []string{"\x1b[1mhello\x1b[m world"}, nil)
	assert.Equal(t, "\x1b[1;9H\x1b[m\x1b[Kxy\x1b[m", string(s.Frame([]string{"\x1b[1mhello\x1b[m woxy"}, nil)))
	// A shorter line erases the rest.
	assert.Equal(t, "\x1b[1;6H\x1b[m\x1b[K\x1b[m", string(s.Frame([]string{"\x1b[1mhello\x1b[m"}, nil)))

	s.noSpans = true
	assert.Equal(t, "\x1b[1H\x1b[m\x1b[2K\x1b[1mhelp\x1b[m\x1b[m", string(s.Frame([]string{"\x1b[1mhelp\x1b[m"}, nil)))
}

// Regression: a rewrite that reaches the last column must not erase after
// it, since with autowrap off the cursor stays on that column.
func TestScreenFrameRewriteToTheLastColumn(t *testing.T) {
	v := newVTTerm(5, 1)
	s := NewScreen(5, 1)
	v.write(s.Frame([]string{"abcde"}, nil))
	v.write(s.Frame([]string{"abcxy"}, nil))
	assert.Empty(t, v.mismatch(expected(5, 1, []string{"abcxy"}), []string{"abcxy"}, nil))
}

func TestScreenFrameScrolls(t *testing.T) {
	rows := func(from int) []string {
		out := []string{"head"}
		for i := from; i < from+8; i++ {
			out = append(out, string(rune('a'+i)))
		}

		return append(out, "foot")
	}
	s := drawn(t, 10, 10, rows(0), nil)
	out := string(s.Frame(rows(1), nil))
	assert.Equal(t, "\x1b[m\x1b[2;9r\x1b[1S\x1b[r\x1b[9H\x1b[m\x1b[Ki\x1b[m", out, "up one, the new line written")
	out = string(s.Frame(rows(0), nil))
	assert.Equal(t, "\x1b[m\x1b[2;9r\x1b[1T\x1b[r\x1b[2H\x1b[m\x1b[Ka\x1b[m", out, "down one")
	out = string(s.Frame(append([]string{"head", "z"}, rows(0)[2:]...), nil))
	assert.NotContains(t, out, "r\x1b[", "one line changed: no scroll")

	s.noScroll = true
	out = string(s.Frame(rows(1), nil))
	assert.NotContains(t, out, "r\x1b[")
	// Three kept rows more than staying put are needed to scroll.
	s = drawn(t, 10, 7, []string{"head", "1", "2", "3", "4", "5", "foot"}, nil)
	assert.NotContains(t, string(s.Frame([]string{"head", "2", "3", "4", "5", "6", "foot"}, nil)), "r\x1b[")
}

func TestDivergence(t *testing.T) {
	for _, tc := range []struct {
		name, old, line string
		at, col         int
		sgr             string
		ok              bool
	}{
		{name: "ascii", old: "hello world", line: "hello there", at: 6, col: 6, ok: true},
		{name: "equal", old: "same", line: "same", at: 4, col: 4, ok: true},
		{name: "longer", old: "ab", line: "abcd", at: 2, col: 2, ok: true},
		{name: "shorter", old: "abcd", line: "ab", at: 2, col: 2, ok: true},
		{name: "all new", old: "abc", line: "xyz", at: 0, col: 0, ok: true},
		{name: "lambda and box drawing", old: "λ ─│ab", line: "λ ─│ac", at: len("λ ─│a"), col: 5, ok: true},
		{name: "braille spinner", old: "⠋ working", line: "⠙ working", at: 0, col: 0, ok: true},
		{name: "braille after text", old: "ab⠋", line: "ab⠙", at: 2, col: 2, ok: true},
		{
			name: "sgr carried", old: "\x1b[31mab\x1b[1mcd", line: "\x1b[31mab\x1b[1mce",
			at: len("\x1b[31mab\x1b[1mc"), col: 3, sgr: "\x1b[31m\x1b[1m", ok: true,
		},
		{name: "sgr after a reset", old: "\x1b[31ma\x1b[mbc", line: "\x1b[31ma\x1b[mbd", at: len("\x1b[31ma\x1b[mb"), col: 2, ok: true},
		{name: "sgr after a 0 reset", old: "\x1b[31ma\x1b[0m\x1b[2mbc", line: "\x1b[31ma\x1b[0m\x1b[2mbd", at: len("\x1b[31ma\x1b[0m\x1b[2mb"), col: 2, sgr: "\x1b[2m", ok: true},
		{name: "change inside an sgr", old: "ab\x1b[31mc", line: "ab\x1b[32mc", at: 2, col: 2, ok: true},
		{name: "change inside an sgr keeps the earlier", old: "\x1b[1mab\x1b[31mc", line: "\x1b[1mab\x1b[32mc", at: len("\x1b[1mab"), col: 2, sgr: "\x1b[1m", ok: true},
		{name: "sgr added", old: "abc", line: "ab\x1b[1mc", at: 2, col: 2, ok: true},
		{name: "change inside a rune", old: "aλ", line: "aμ", at: 1, col: 1, ok: true},
		{name: "change inside a box rune", old: "x─", line: "x│", at: 1, col: 1, ok: true},
		{name: "combining mark after", old: "cafe", line: "café", ok: false},
		{name: "combining mark in old", old: "café", line: "cafex", ok: false},
		{name: "variation selector after", old: "⚠ x", line: "⚠️ x", ok: false},
		{name: "zero width joiner after", old: "ab", line: "a‍b", ok: false},
		{name: "cjk in prefix", old: "日本ab", line: "日本ac", ok: false},
		{name: "emoji in prefix", old: "🙂ab", line: "🙂ac", ok: false},
		{name: "emoji-capable shape in prefix", old: "▶ ab", line: "▶ ac", ok: false},
		{name: "wide at the change", old: "ab", line: "a日", ok: false},
		{name: "tab in prefix", old: "\tab", line: "\tac", ok: false},
		{name: "hyperlink in prefix", old: "\x1b]8;;x\x1b\\ab", line: "\x1b]8;;x\x1b\\ac", ok: false},
		{name: "other csi in prefix", old: "\x1b[2Kab", line: "\x1b[2Kac", ok: false},
		{name: "soft hyphen in prefix", old: "a­b", line: "a­c", ok: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			at, col, sgr, ok := divergence(tc.old, tc.line)
			require.Equal(t, tc.ok, ok)
			if !ok {
				return
			}
			assert.Equal(t, tc.at, at, "at")
			assert.Equal(t, tc.col, col, "col")
			assert.Equal(t, tc.sgr, sgr, "sgr")
			assert.Equal(t, tc.old[:at], tc.line[:at], "the prefix is common")
		})
	}
}

// A change past the screen's edge rewrites the row whole.
func TestScreenFrameChangePastTheEdge(t *testing.T) {
	s := drawn(t, 3, 1, []string{"abcd"}, nil)
	assert.Equal(t, "\x1b[1H\x1b[m\x1b[2Kabce\x1b[m", string(s.Frame([]string{"abce"}, nil)))
}
