package main

import (
	"io"
	"os"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/viktordanov/uah/internal/tui/render"
)

// cliStyles are the listings' colors, the TUI's dark theme as the review's
// are: what is on or passed in Good, what needs a look in Warn, what is
// broken in Bad, and what is off or does not apply in Dim.
var cliStyles = newReviewStyles(render.Amber)

// colorOut is w for styled text, as the review writes: the terminal's
// colors on a terminal, none on a pipe or a file or with NO_COLOR.
func colorOut(w io.Writer) io.Writer {
	return reviewWriter(w, stdoutTTY(w), os.Environ())
}

// table lays out rows in columns two spaces apart, as text/tabwriter does,
// but measures a cell without its styles, so color never moves a column:
// the header bold, and a row's style on each of its cells that has none of
// its own.
type table struct {
	rows []tableRow
	// caps limit a column's width: a wider cell runs past it in its own
	// row, rather than pushing every row's next column out.
	caps map[int]int
	// right are the columns aligned right, as numbers read.
	right map[int]bool
}

type tableRow struct {
	style *lipgloss.Style
	cells []string
}

func (t *table) header(cells ...string) { t.row(&cliStyles.bold, cells...) }

// row adds a row in style (nil: plain); a cell may be rendered already.
func (t *table) row(style *lipgloss.Style, cells ...string) {
	t.rows = append(t.rows, tableRow{style, cells})
}

// capColumn limits column i to width n.
func (t *table) capColumn(i, n int) {
	if t.caps == nil {
		t.caps = map[int]int{}
	}
	t.caps[i] = n
}

// alignRight aligns the columns right.
func (t *table) alignRight(cols ...int) {
	if t.right == nil {
		t.right = map[int]bool{}
	}
	for _, i := range cols {
		t.right[i] = true
	}
}

// write prints the table to w through colorOut. The last cell of a row is
// not padded.
func (t *table) write(w io.Writer) error {
	var widths []int
	for _, r := range t.rows {
		for i, c := range r.cells {
			if i == len(widths) {
				widths = append(widths, 0)
			}
			if n, ok := t.caps[i]; !ok || ansi.StringWidth(c) <= n {
				widths[i] = max(widths[i], ansi.StringWidth(c))
			}
		}
	}
	var b strings.Builder
	for _, r := range t.rows {
		var line strings.Builder
		for i, c := range r.cells {
			fill := strings.Repeat(" ", max(widths[i]-ansi.StringWidth(c), 0))
			if r.style != nil && c != "" && !strings.Contains(c, "\x1b") {
				c = r.style.Render(c)
			}
			switch {
			case t.right[i]:
				c = fill + c
			case i < len(r.cells)-1:
				c += fill
			}
			if i < len(r.cells)-1 {
				c += "  "
			}
			line.WriteString(c)
		}
		b.WriteString(strings.TrimRight(line.String(), " ") + "\n") // empty last cells leave no spaces
	}
	_, err := io.WriteString(colorOut(w), b.String())

	return err
}
