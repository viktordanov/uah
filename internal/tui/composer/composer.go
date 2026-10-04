// Package composer is uah's prompt input: a multi-line editor that wraps
// its text at word boundaries, grows to a height limit and then scrolls,
// and places the terminal's real cursor. It replaces bubbles/textarea and
// keeps its default key map and its behavior where uah relies on it. It
// does no I/O: the caller feeds it keys and pastes, draws its View, and
// moves the terminal's cursor to Cursor.
package composer

import (
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxLines caps the draft's lines, as textarea's did. A paste past it is
// cut, and a new line is refused.
const MaxLines = 10000

// Composer is the editor's state. The zero value is not ready: use New.
// Its methods take a pointer; a copy shares the lines with the original,
// so keep one live copy, as with textarea.
type Composer struct {
	// Placeholder shows, dim, while the draft is empty.
	Placeholder string
	// KeyMap maps key names to what they do (Press).
	KeyMap KeyMap

	lines []line
	row   int // the cursor's line
	col   int // the cursor's rune offset in its line, on a cluster boundary
	// goal is the cell column ↑ and ↓ keep while they move, as textarea's
	// lastCharOffset; a horizontal move or an edit resets it.
	goal int

	width       int // the text's width, without the prompt
	promptWidth int
	prompt      func(row int) string
	styles      Styles

	maxHeight int // 0 is no limit
	height    int
	offset    int // the first visual row shown

	focus bool
	sel   selection
}

// line is one line of the draft and its cached layout.
type line struct {
	text []rune
	lay  *layout
}

// New returns an empty, focused composer, 40 cells wide, with no prompt
// and no height limit.
func New() Composer {
	c := Composer{KeyMap: DefaultKeyMap(), lines: []line{{}}, focus: true, height: 1}
	c.SetWidth(defaultWidth)

	return c
}

const defaultWidth = 40

// Value is the draft, its lines joined by \n.
func (c *Composer) Value() string {
	var b strings.Builder
	for i, l := range c.lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(string(l.text))
	}

	return b.String()
}

// SetValue replaces the draft and puts the cursor at its end.
func (c *Composer) SetValue(s string) {
	c.Reset()
	c.InsertString(s)
}

// Reset empties the draft.
func (c *Composer) Reset() {
	c.lines = []line{{}}
	c.row, c.col, c.goal, c.offset = 0, 0, 0, 0
	c.sel = selection{}
	c.fit()
}

// InsertString inserts s at the cursor as typed text: tabs become four
// spaces, \r\n and \r become \n, and other control characters and invalid
// UTF-8 are dropped. It leaves the cursor after the text and drops the
// selection.
func (c *Composer) InsertString(s string) {
	c.sel = selection{}
	c.insert(sanitize(s))
	c.fit()
}

// Paste inserts pasted text in place of the selection, if any.
func (c *Composer) Paste(s string) {
	c.deleteSelection()
	c.InsertString(s)
}

// LineCount is the number of lines in the draft.
func (c *Composer) LineCount() int { return len(c.lines) }

// Line is the cursor's line, from 0.
func (c *Composer) Line() int { return c.row }

// Column is the cursor's offset in its line, in runes.
func (c *Composer) Column() int { return c.col }

// CursorStart moves the cursor to its line's start.
func (c *Composer) CursorStart() { c.setCol(0); c.fit() }

// CursorEnd moves the cursor to its line's end.
func (c *Composer) CursorEnd() { c.setCol(len(c.cur())); c.fit() }

// MoveToBegin moves the cursor to the draft's start.
func (c *Composer) MoveToBegin() { c.row = 0; c.setCol(0); c.fit() }

// MoveToEnd moves the cursor to the draft's end.
func (c *Composer) MoveToEnd() { c.row = len(c.lines) - 1; c.setCol(len(c.cur())); c.fit() }

// Focus lets the composer take keys and show its cursor.
func (c *Composer) Focus() { c.focus = true }

// Blur makes the composer ignore keys and hide its cursor.
func (c *Composer) Blur() { c.focus = false }

// Focused reports whether the composer takes keys.
func (c *Composer) Focused() bool { return c.focus }

// cur is the cursor's line.
func (c *Composer) cur() []rune { return c.lines[c.row].text }

// setLine replaces line i's text and drops its layout.
func (c *Composer) setLine(i int, text []rune) { c.lines[i] = line{text: text} }

// setCol moves the cursor within its line, clamped, and resets the goal
// column, as textarea's SetCursorColumn.
func (c *Composer) setCol(col int) {
	c.col = min(max(col, 0), len(c.cur()))
	c.goal = 0
}

// layout is line i's layout at the current width.
func (c *Composer) layout(i int) *layout {
	l := &c.lines[i]
	if l.lay == nil || l.lay.width != c.width {
		l.lay = newLayout(l.text, c.width)
	}

	return l.lay
}

// insert puts runes, already sanitized, at the cursor.
func (c *Composer) insert(runes []rune) {
	parts := splitLines(runes)
	if room := MaxLines - len(c.lines) + 1; len(parts) > room {
		parts = parts[:room]
	}
	c.goal = 0
	text := c.cur()
	head := slices.Clone(text[:c.col])
	tail := slices.Clone(text[c.col:])
	if len(parts) == 1 {
		c.setLine(c.row, slices.Concat(head, parts[0], tail))
		c.col += len(parts[0])

		return
	}
	added := make([]line, len(parts))
	added[0] = line{text: slices.Concat(head, parts[0])}
	for i, p := range parts[1:] {
		added[i+1] = line{text: slices.Clone(p)}
	}
	last := len(parts) - 1
	c.col = len(parts[last])
	added[last].text = append(added[last].text, tail...)
	c.lines = slices.Concat(c.lines[:c.row], added, c.lines[c.row+1:])
	c.row += last
}

// splitLines cuts runes at \n.
func splitLines(runes []rune) [][]rune {
	parts := [][]rune{}
	start := 0
	for i, r := range runes {
		if r == '\n' {
			parts = append(parts, runes[start:i])
			start = i + 1
		}
	}

	return append(parts, runes[start:])
}

// sanitize turns typed or pasted text into the draft's runes, as
// textarea's sanitizer did, except that \r\n is one line break.
func sanitize(s string) []rune {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch {
		case r == utf8.RuneError: // invalid UTF-8, and U+FFFD, as textarea
		case r == '\r' || r == '\n':
			out = append(out, '\n')
		case r == '\t':
			out = append(out, ' ', ' ', ' ', ' ')
		case unicode.IsControl(r):
		default:
			out = append(out, r)
		}
	}

	return out
}

// splitLine breaks the cursor's line at the cursor; false when the draft
// has MaxLines lines.
func (c *Composer) splitLine() bool {
	if len(c.lines) >= MaxLines {
		return false
	}
	text := c.cur()
	head, tail := slices.Clone(text[:c.col]), slices.Clone(text[c.col:])
	c.lines = slices.Insert(c.lines, c.row+1, line{text: tail})
	c.setLine(c.row, head)
	c.row++
	c.col = 0 // keeps the goal column, as textarea's splitLine

	return true
}

// joinAbove joins the cursor's line to the one above, with the cursor at
// the join.
func (c *Composer) joinAbove() {
	if c.row == 0 {
		return
	}
	above := c.lines[c.row-1].text
	col := len(above)
	c.setLine(c.row-1, slices.Concat(above, c.cur()))
	c.lines = slices.Delete(c.lines, c.row, c.row+1)
	c.row--
	c.col = col // keeps the goal column, as textarea's mergeLineAbove
}

// joinBelow joins the line below to the cursor's.
func (c *Composer) joinBelow() {
	if c.row >= len(c.lines)-1 {
		return
	}
	c.setLine(c.row, slices.Concat(c.cur(), c.lines[c.row+1].text))
	c.lines = slices.Delete(c.lines, c.row+1, c.row+2)
}

// deleteRange deletes runes [from, to) of the cursor's line and puts the
// cursor at from.
func (c *Composer) deleteRange(from, to int) {
	c.setLine(c.row, slices.Concat(c.cur()[:from], c.cur()[to:]))
	c.setCol(from)
}

// fit snaps the cursor to a cluster boundary, sizes the composer to its
// rows, and scrolls the cursor into view. Every change ends with it.
func (c *Composer) fit() {
	c.row = min(max(c.row, 0), len(c.lines)-1)
	c.col = min(max(c.col, 0), len(c.cur()))
	lay := c.layout(c.row)
	if g := lay.cluster(c.col); lay.starts[g] != c.col { // inside a cluster: after it
		c.col = lay.starts[g+1]
	}
	total := c.totalRows()
	h := max(total, 1)
	if c.maxHeight > 0 {
		h = min(h, c.maxHeight)
	}
	c.height = h
	c.offset = min(c.offset, max(total-h, 0))
	if r := c.cursorRow(); r < c.offset {
		c.offset = r
	} else if r >= c.offset+h {
		c.offset = r - h + 1
	}
}

// totalRows is the number of visual rows of the whole draft.
func (c *Composer) totalRows() int {
	n := 0
	for i := range c.lines {
		n += len(c.layout(i).rows)
	}

	return n
}

// cursorRow is the visual row of the cursor, from the draft's first.
func (c *Composer) cursorRow() int {
	n := 0
	for i := range c.row {
		n += len(c.layout(i).rows)
	}
	lay := c.layout(c.row)

	return n + lay.row(lay.cluster(c.col))
}
