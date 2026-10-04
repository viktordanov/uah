package composer

import (
	"slices"
	"strings"
)

// Position is a place in the draft: a line and a rune offset in it.
type Position struct{ Row, Col int }

func (p Position) before(q Position) bool {
	return p.Row < q.Row || (p.Row == q.Row && p.Col < q.Col)
}

// selection is the keyboard selection, from anchor, where it began, to
// head, the cursor.
type selection struct {
	on           bool
	anchor, head Position
}

// HasSelection reports whether some text is selected.
func (c *Composer) HasSelection() bool { return c.sel.on && c.sel.anchor != c.sel.head }

// Selection is the selected range, start before end.
func (c *Composer) Selection() (start, end Position, ok bool) {
	if !c.HasSelection() {
		return Position{}, Position{}, false
	}
	start, end = c.sel.anchor, c.sel.head
	if end.before(start) {
		start, end = end, start
	}

	return start, end, true
}

// SelectedText is the selected text, its lines joined by \n.
func (c *Composer) SelectedText() string {
	start, end, ok := c.Selection()
	if !ok {
		return ""
	}
	if start.Row == end.Row {
		return string(c.lines[start.Row].text[start.Col:end.Col])
	}
	parts := []string{string(c.lines[start.Row].text[start.Col:])}
	for _, l := range c.lines[start.Row+1 : end.Row] {
		parts = append(parts, string(l.text))
	}
	parts = append(parts, string(c.lines[end.Row].text[:end.Col]))

	return strings.Join(parts, "\n")
}

// SelectAll selects the whole draft and puts the cursor at its end.
func (c *Composer) SelectAll() {
	c.MoveToEnd()
	c.sel = selection{on: true, head: Position{Row: c.row, Col: c.col}}
}

// ClearSelection drops the selection, keeping the text.
func (c *Composer) ClearSelection() { c.sel = selection{} }

// selectWith extends the selection by a cursor move, starting it at the
// cursor if none is on.
func (c *Composer) selectWith(move func()) {
	if !c.sel.on {
		c.sel = selection{on: true, anchor: Position{Row: c.row, Col: c.col}}
	}
	move()
	c.sel.head = Position{Row: c.row, Col: c.col}
}

// selectionIn is the selected rune range of line i.
func (c *Composer) selectionIn(i int) (from, to int, ok bool) {
	start, end, ok := c.Selection()
	if !ok || i < start.Row || i > end.Row {
		return 0, 0, false
	}
	from, to = 0, len(c.lines[i].text)
	if i == start.Row {
		from = start.Col
	}
	if i == end.Row {
		to = end.Col
	}

	return from, to, from < to
}

// deleteSelection deletes the selected text and puts the cursor where it
// began; false when nothing is selected.
func (c *Composer) deleteSelection() bool {
	start, end, ok := c.Selection()
	c.sel = selection{}
	if !ok {
		return false
	}
	merged := slices.Concat(c.lines[start.Row].text[:start.Col], c.lines[end.Row].text[end.Col:])
	c.lines = slices.Delete(c.lines, start.Row+1, end.Row+1)
	c.row = start.Row
	c.setLine(c.row, merged)
	c.setCol(start.Col)

	return true
}
