package render

import (
	"regexp"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/viktordanov/uah/internal/tui/state"
)

// Notes drawn over the transcript's window rather than in rows of their
// own, so nothing on the screen moves when one comes or goes: the pill
// that says output arrived below a window scrolled up (state/scroll.go),
// centered on its last row as Codex draws it, and the toast (state.Toast),
// in the corner on the right. Each is one row on the theme's accent, as
// the header is; Note draws any other.

// pillLabels are the new-output pill's words, Codex's with uah's key,
// longest first: the first that fits is drawn.
var pillLabels = []string{
	" New activity · ↓ Back to bottom · end ",
	" New activity · ↓ Bottom · end ",
	" New · ↓ Bottom · end ",
	" ↓ Bottom · end ",
	" ↓ end ",
}

// cells are the cells [from, to) of a window row.
type cells struct{ row, from, to int }

// notes draws the pill and the toast over the window's rows out, w cells
// wide, and records where the pill went. With the pill on the last row,
// the toast takes the first.
func (c *Cache) notes(s state.State, out []string, w int) {
	c.pill = cells{row: -1}
	if len(out) == 0 {
		return
	}
	toastRow := len(out) - 1
	if s.NewBelow && s.Scroll > 0 && (s.Selection == nil || !s.Selection.Dragging) {
		if label := fitting(pillLabels, w-2); label != "" {
			row, at := len(out)-1, (w-ansi.StringWidth(label))/2
			out[row] = c.styles.Note(out[row], label, at)
			c.pill, toastRow = cells{row: row, from: at, to: at + ansi.StringWidth(label)}, 0
		}
	}
	if t := s.Toast; t != nil && w > 4 {
		text := ansi.Truncate(" "+t.Text+" ", w-2, "… ")
		out[toastRow] = c.styles.Note(out[toastRow], text, w-ansi.StringWidth(text)-1)
	}
}

// fitting is the first label at most w cells wide, or "".
func fitting(labels []string, w int) string {
	for _, l := range labels {
		if ansi.StringWidth(l) <= w {
			return l
		}
	}

	return ""
}

// Note draws text, plain, as a chip over line from cell at: the cells it
// covers go, the rest keep their styles, and a line shorter than at is
// padded to it.
func (st *Styles) Note(line, text string, at int) string {
	at = max(at, 0)
	left := ansi.Truncate(line, at, "")
	if pad := at - ansi.StringWidth(left); pad > 0 { // short, or a wide character the note's start cuts
		if ansi.StringWidth(line) > at {
			left += "\x1b[m" + stylesAt(line, at-1)
		}
		left += strings.Repeat(" ", pad)
	}
	to := at + ansi.StringWidth(text)
	// The styles in force where the note ends carry on after it.
	resume := strings.Join(sgr.FindAllString(ansi.Truncate(line, to, ""), -1), "")
	right := ansi.TruncateLeft(line, to, "")
	if w := ansi.StringWidth(right); w > 0 && w > ansi.StringWidth(line)-to {
		// A wide character the note's end cuts: a space for its half, in
		// its styles.
		right = "\x1b[m" + stylesAt(line, to-1) + " " + ansi.TruncateLeft(line, to+1, "")
	}

	return left + "\x1b[m" + st.chip.Render(text) + "\x1b[m" + resume + right
}

// leadingSGR matches the SGR sequences a line starts with.
var leadingSGR = regexp.MustCompile("^(?:\x1b\\[[0-9;]*m)*")

// stylesAt is the SGR sequences in force at cell col of line: what
// ansi.TruncateLeft keeps before the character there.
func stylesAt(line string, col int) string {
	return leadingSGR.FindString(ansi.TruncateLeft(line, col, ""))
}

// OnPill reports whether screen cell (x, y) is on the new-output pill the
// last frame drew, which a click takes to the bottom.
func (c *Cache) OnPill(x, y int) bool {
	p := c.pill

	return p.row >= 0 && y-c.top == p.row && x >= p.from && x < p.to
}

// Anchor is the transcript line on the bottom row of the last frame's
// window and how many lines lay below it, for state.Anchored.
func (c *Cache) Anchor() (at state.TextPos, scroll int) { return c.bottom, c.scrolled }
