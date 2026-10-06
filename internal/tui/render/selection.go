package render

import (
	"slices"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/viktordanov/uah/internal/tui/state"
)

// Text selected with the mouse (state.Selection): each frame records which
// transcript line every row of the window shows, so the shell can turn the
// mouse into a state.TextPos (At), and draws the selection over the
// window's lines as fade does, so the cache keeps each item's own lines.

// rowRefs are the transcript lines of all, a transcript's lines built from
// rev (each item's lines, bottom item first, with its key in keys) after
// head lines of banner.
func rowRefs(keys []string, rev [][]string, head int) []state.TextPos {
	refs := make([]state.TextPos, 0, head+len(rev)*2)
	for l := range head {
		refs = append(refs, state.TextPos{Key: state.BannerKey, Line: l})
	}
	for i, lines := range slices.Backward(rev) {
		for l := range lines {
			refs = append(refs, state.TextPos{Key: keys[i], Line: l})
		}
	}

	return refs
}

// selectWindow records the window's rows, refs for its last lines (the
// first pad rows are empty), and draws the selection over them.
func (c *Cache) selectWindow(s state.State, out []string, refs []state.TextPos) {
	pad := len(out) - len(refs)
	c.rows, c.window = make([]state.TextPos, len(out)), out
	copy(c.rows[pad:], refs)
	if s.Selection == nil {
		return
	}
	for j := pad; j < len(out); j++ {
		if from, to, ok := s.SelectedCols(c.rows[j].Key, c.rows[j].Line); ok {
			out[j] = c.styles.highlight(out[j], from, to, c.styles.selectOn)
		}
	}
}

// highlight draws cells [from, to) of a line with the SGR on, such as the
// selection's background in the terminal's own text color; the rest keeps
// its styles.
func (st *Styles) highlight(line string, from, to int, on string) string {
	from, to = snap(ansi.Strip(line), from, to)
	if from >= to {
		return line
	}
	before := ansi.Truncate(line, to, "")
	// The styles in force where the selection ends carry on after it.
	resume := strings.Join(sgr.FindAllString(before, -1), "")

	return ansi.Truncate(line, from, "") + "\x1b[m" + on + ansi.Strip(ansi.Cut(line, from, to)) + "\x1b[m" +
		resume + ansi.TruncateLeft(line, to, "")
}

// At is the transcript cell drawn at screen cell (x, y) in the last frame,
// and its line's text without styles. ok is false outside the transcript
// and on the empty rows above a short one. Clamp moves a y above or below
// the transcript to its first or last line, for a drag past its edge.
func (c *Cache) At(x, y int, clamp bool) (pos state.TextPos, text string, ok bool) {
	row := y - c.top
	if clamp {
		first := 0
		for first < len(c.rows) && c.rows[first].Key == "" {
			first++
		}
		row = min(max(row, first), len(c.rows)-1)
	}
	if row < 0 || row >= len(c.rows) || c.rows[row].Key == "" {
		return state.TextPos{}, "", false
	}
	pos = c.rows[row]
	pos.Col = max(x, 0)

	return pos, ansi.Strip(c.window[row]), true
}

// Edge says where screen row y lies from the last frame's transcript, for
// a drag's edge scroll: negative on its first row or above, positive below
// it, and 0 inside. Its size is one on the edge row and one more for each
// row past it, so a drag to the top row scrolls up even where the
// transcript starts at the screen's top.
func (c *Cache) Edge(y int) int {
	switch bottom := c.top + len(c.rows); {
	case y <= c.top:
		return y - c.top - 1
	case y >= bottom:
		return y - bottom + 1
	}

	return 0
}
