package render_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/tui/render"
	"github.com/viktordanov/uah/internal/tui/state"
)

// peekSource is a Go file of n lines for the overlay.
func peekSource(n int) []string {
	lines := []string{"package peek", "", "// Peek is a file shown over the session."}
	for i := len(lines); i < n; i++ {
		lines = append(lines, fmt.Sprintf("\tx%d := step(%d) // line %d\twith a tab", i+1, i+1, i+1))
	}

	return lines
}

// peeking is a session with an answer, peeking at lines 30-32 of a
// 100-line file, read for an overlay of rows lines.
func peeking(c *render.Cache, rows int) state.State {
	s := linksOn(apply(base(), core.RunStarted{At: t0, RunID: "r1"}, core.AssistantMessage{At: t0, Text: "Done: see `peek.go`.", Final: true}))
	l := state.FileLink{Path: ws + "/internal/tui/state/peek.go", Line: 30, End: 32}
	s = apply(s, state.OpenLink{Link: l})
	lines := c.Styles().Highlight("peek.go", peekSource(100))

	return apply(s, state.PeekLoaded{Link: l, Lines: lines, Rows: rows})
}

func peekScreen(s state.State, c *render.Cache, w, h int) []string {
	out, _ := render.Screen(s, c, render.Frame{Width: w, Height: h, Composer: "λ ", ComposerHeight: 1})

	return strings.Split(out, "\n")
}

func stripped(lines []string) string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = strings.TrimRight(ansi.Strip(l), " ")
	}

	return strings.Join(out, "\n") + "\n"
}

// TestPeek_Overlay draws the overlay at 120x40, centered at 80% of the
// screen, and at 60x20, where it takes the whole screen: the path and the
// lines in its title, the line numbers, the link's lines near the top
// third on the band, and the keys in its bottom edge.
func TestPeek_Overlay(t *testing.T) {
	for _, size := range [][2]int{{120, 40}, {60, 20}} {
		w, h := size[0], size[1]
		c := render.NewCache(render.Amber)
		s := peeking(c, render.PeekRows(w, h))
		lines := peekScreen(s, c, w, h)
		require.Len(t, lines, h)
		for i, l := range lines {
			assert.LessOrEqual(t, ansi.StringWidth(l), w, "row %d", i)
		}
		golden(t, fmt.Sprintf("peek-%dx%d", w, h), stripped(lines))

		x, y, pw, ph := render.PeekRect(w, h)
		if w == 60 {
			assert.Equal(t, [4]int{0, 0, 60, 20}, [4]int{x, y, pw, ph}, "the whole screen below 60x20 and at it")
		} else {
			assert.Equal(t, [4]int{12, 4, 96, 32}, [4]int{x, y, pw, ph})
		}
		top := strings.Index(stripped(lines[y+1:y+ph-1]), "30 ")
		assert.Positive(t, top, "line 30 shows")
		marked := 0
		for _, l := range lines[y+1 : y+ph-1] {
			_, row, ok := strings.Cut(l, "\x1b]8;;\x1b\\") // the overlay starts after the end of any link
			require.True(t, ok)
			if strings.Contains(row[:strings.LastIndex(row, "│")], bandOn(render.Amber)) {
				marked++
			}
		}
		assert.Equal(t, 3, marked, "lines 30 to 32 on the band")
	}
}

// bandOn switches the theme's band on, as the renderer writes it.
func bandOn(t render.Theme) string {
	r, g, b, _ := t.Band.RGBA()

	return fmt.Sprintf("\x1b[48;2;%d;%d;%dm", r>>8, g>>8, b>>8)
}

// TestPeek_NoLayoutShift: the screen under the overlay is the screen
// without it, cell for cell, outside the overlay's rectangle.
func TestPeek_NoLayoutShift(t *testing.T) {
	w, h := 120, 40
	c := render.NewCache(render.Amber)
	s := peeking(c, render.PeekRows(w, h))
	with := peekScreen(s, c, w, h)
	s.Peek = nil
	without := peekScreen(s, c, w, h)
	x, y, pw, ph := render.PeekRect(w, h)
	for i := range h {
		a, b := ansi.Strip(with[i]), ansi.Strip(without[i])
		if i < y || i >= y+ph {
			assert.Equal(t, b, a, "row %d is untouched", i)

			continue
		}
		assert.Equal(t, strings.TrimRight(ansi.Truncate(b, x, ""), " "), strings.TrimRight(ansi.Truncate(a, x, ""), " "), "row %d left of the overlay", i)
		assert.Equal(t, ansi.Strip(ansi.TruncateLeft(without[i], x+pw, "")), ansi.Strip(ansi.TruncateLeft(with[i], x+pw, "")), "row %d right of it", i)
	}
}

// TestPeek_States: reading, a note for a file not shown, and the top
// clamped to the text after a resize.
func TestPeek_States(t *testing.T) {
	c := render.NewCache(render.Amber)
	s := linksOn(base())
	l := state.FileLink{Path: ws + "/bin/tool"}
	s = apply(s, state.OpenLink{Link: l})
	assert.Contains(t, stripped(peekScreen(s, c, 80, 24)), "reading…")
	s = apply(s, state.PeekLoaded{Link: l, Note: "binary file, not shown", Rows: 20})
	got := stripped(peekScreen(s, c, 80, 24))
	assert.Contains(t, got, "binary file, not shown")
	assert.Contains(t, got, "bin/tool", "the title")

	s = peeking(c, 36)
	s = apply(s, state.PeekScroll{Delta: 1000, Rows: 36})
	got = stripped(peekScreen(s, c, 60, 20))
	assert.Contains(t, got, "100 ", "the last line shows after a resize")
	assert.Contains(t, got, "of 100", "the position")
}

// TestPeek_EdgeCases: a screen too short for the frame draws no overlay
// and does not panic; a wide character the overlay's right edge cuts
// leaves a space and the columns after it in place; escapes in a path or
// a note are not written.
func TestPeek_EdgeCases(t *testing.T) {
	c := render.NewCache(render.Amber)
	s := peeking(c, 1)
	for _, size := range [][2]int{{80, 1}, {80, 2}, {3, 10}, {80, 3}} {
		assert.NotPanics(t, func() { peekScreen(s, c, size[0], size[1]) }, "%v", size)
	}

	w, h := 120, 40
	x, _, pw, _ := render.PeekRect(w, h)
	// After "• ", 日 takes the overlay's last column and the one after it.
	s = linksOn(apply(base(), core.AssistantMessage{At: t0, Text: strings.Repeat("a", x+pw-3) + "日本tail", Final: true}))
	tailCol := func(lines []string) int {
		for _, l := range lines {
			if p := ansi.Strip(l); strings.Contains(p, "tail") {
				return ansi.StringWidth(p[:strings.Index(p, "tail")])
			}
		}

		return -1
	}
	want := tailCol(peekScreen(s, c, w, h))
	s.Peek = &state.Peek{Link: state.FileLink{Path: ws + "/a.go"}, Lines: []string{"x"}}
	lines := peekScreen(s, c, w, h)
	assert.Equal(t, want, tailCol(lines), "tail keeps its column")
	for i, l := range lines {
		assert.LessOrEqual(t, ansi.StringWidth(l), w, "row %d", i)
	}

	evil := ws + "/x\x1b]52;c;aGk=\x07.go"
	s.Peek = &state.Peek{Link: state.FileLink{Path: evil}, Note: "lstat " + evil + ": no such file"}
	out := strings.Join(peekScreen(s, c, w, h), "\n")
	assert.NotContains(t, out, "\x1b]52", "no escape from the path or the note")
}
