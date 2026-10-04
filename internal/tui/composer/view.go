package composer

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// Styles color the composer's parts. Each takes plain text and returns it
// styled; nil leaves it plain.
type Styles struct {
	Prompt      func(string) string
	Placeholder func(string) string
	Selection   func(string) string
}

// SetStyles sets the composer's colors.
func (c *Composer) SetStyles(s Styles) { c.styles = s }

// SetPrompt draws fn(row) before each visual row, row counted from the
// draft's first, so a mark on row 0 scrolls away with it. A prompt
// narrower than width is padded on the left.
func (c *Composer) SetPrompt(width int, fn func(row int) string) {
	total := c.width + c.promptWidth
	c.prompt, c.promptWidth = fn, max(width, 0)
	c.SetWidth(total)
}

// SetWidth fits the composer, prompt included, to w cells; the text gets
// at least one.
func (c *Composer) SetWidth(w int) {
	c.width = max(w-c.promptWidth, 1)
	c.fit()
}

// Width is the text's width, without the prompt.
func (c *Composer) Width() int { return c.width }

// SetMaxHeight sets how many rows the composer grows to before it
// scrolls; 0 or less is no limit.
func (c *Composer) SetMaxHeight(h int) {
	c.maxHeight = max(h, 0)
	c.fit()
}

// Height is the number of rows View draws: the draft's rows, at least
// one, at most the height limit.
func (c *Composer) Height() int { return c.height }

// ScrollOffset is the first visual row shown.
func (c *Composer) ScrollOffset() int { return c.offset }

// LineInfo places the cursor within its soft-wrapped line, as textarea's
// LineInfo, with columns in runes and offsets in cells.
type LineInfo struct {
	// Height is the line's number of rows, and RowOffset the cursor's row
	// among them.
	Height, RowOffset int
	// StartColumn is the rune offset where the cursor's row starts, and
	// Width its length in runes; CharWidth is its width in cells.
	StartColumn, Width, CharWidth int
	// ColumnOffset is the cursor's offset from the row's start in runes,
	// and CharOffset in cells.
	ColumnOffset, CharOffset int
}

// LineInfo describes the cursor's line and row.
func (c *Composer) LineInfo() LineInfo {
	lay := c.layout(c.row)
	g := lay.cluster(c.col)
	r := lay.row(g)
	start, end := lay.rows[r], lay.rowEnd(r)

	return LineInfo{
		Height:       len(lay.rows),
		RowOffset:    r,
		StartColumn:  lay.starts[start],
		Width:        lay.starts[end] - lay.starts[start],
		CharWidth:    lay.cellsBetween(start, end),
		ColumnOffset: c.col - lay.starts[start],
		CharOffset:   lay.cellsBetween(start, g),
	}
}

// Cursor is where the terminal's cursor goes, relative to View's top left
// cell; ok is false while the composer is blurred.
func (c *Composer) Cursor() (x, y int, ok bool) {
	if !c.focus {
		return 0, 0, false
	}

	return c.promptWidth + c.LineInfo().CharOffset, c.cursorRow() - c.offset, true
}

// View draws the visible rows, Height of them, joined by \n. Rows are not
// padded to the width.
func (c *Composer) View() string {
	if c.Placeholder != "" && len(c.lines) == 1 && len(c.lines[0].text) == 0 {
		return c.placeholderView()
	}
	out := make([]string, 0, c.height)
	vrow := 0 // the visual row, from the draft's first
	for i := range c.lines {
		lay := c.layout(i)
		if vrow+len(lay.rows) <= c.offset {
			vrow += len(lay.rows)

			continue
		}
		text := c.lines[i].text
		for r := range lay.rows {
			if vrow >= c.offset && vrow < c.offset+c.height {
				from, to := lay.starts[lay.rows[r]], lay.starts[lay.rowEnd(r)]
				out = append(out, c.promptView(vrow)+c.rowView(i, text, from, to))
			}
			vrow++
		}
		if vrow >= c.offset+c.height {
			break
		}
	}

	return strings.Join(out, "\n")
}

// rowView draws runes [from, to) of line i, with the selected part
// highlighted.
func (c *Composer) rowView(i int, text []rune, from, to int) string {
	sf, st, ok := c.selectionIn(i)
	if !ok || st <= from || sf >= to {
		return string(text[from:to])
	}
	sf, st = max(sf, from), min(st, to)

	return string(text[from:sf]) + apply(c.styles.Selection, string(text[sf:st])) + string(text[st:to])
}

// promptView is the prompt of visual row vrow, padded to its width.
func (c *Composer) promptView(vrow int) string {
	if c.prompt == nil {
		return strings.Repeat(" ", c.promptWidth)
	}
	p := c.prompt(vrow)
	if w := ansi.StringWidth(p); w < c.promptWidth {
		p = strings.Repeat(" ", c.promptWidth-w) + p
	}

	return apply(c.styles.Prompt, p)
}

// placeholderView draws the placeholder, word wrapped to the width, on
// as many rows as the composer is high: one, for an empty draft.
func (c *Composer) placeholderView() string {
	wrapped := ansi.Hardwrap(ansi.Wordwrap(c.Placeholder, c.width, ""), c.width, true)
	lines := strings.Split(strings.TrimSpace(wrapped), "\n")
	out := make([]string, c.height)
	for i := range out {
		out[i] = c.promptView(i)
		if i < len(lines) {
			out[i] += apply(c.styles.Placeholder, lines[i])
		}
	}

	return strings.Join(out, "\n")
}

func apply(style func(string) string, s string) string {
	if style == nil || s == "" {
		return s
	}

	return style(s)
}
