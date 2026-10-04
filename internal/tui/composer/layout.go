package composer

import (
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

// layout is one line of the draft cut into grapheme clusters and wrapped
// into rows. It is cached on the line and dropped when the line or the
// width changes.
type layout struct {
	width int
	// starts holds the rune offset of each cluster, and the line's length
	// after the last one.
	starts []int
	cells  []int  // each cluster's width in cells
	spaces []bool // whether each cluster is whitespace, where rows break
	// rows holds the cluster each row starts at; rows[0] is 0.
	rows []int
}

// clusters is the number of grapheme clusters in the line.
func (l *layout) clusters() int { return len(l.cells) }

// rowEnd is the cluster after row r's last.
func (l *layout) rowEnd(r int) int {
	if r+1 < len(l.rows) {
		return l.rows[r+1]
	}

	return len(l.cells)
}

// cellsBetween is the width of clusters [from, to).
func (l *layout) cellsBetween(from, to int) int {
	w := 0
	for _, c := range l.cells[from:to] {
		w += c
	}

	return w
}

// cluster is the cluster that starts at rune offset col, or the cluster
// col falls inside of; the line's end is clusters().
func (l *layout) cluster(col int) int {
	lo, hi := 0, len(l.cells)
	for lo < hi { // the last start <= col
		mid := (lo + hi + 1) / 2
		if l.starts[mid] <= col {
			lo = mid
		} else {
			hi = mid - 1
		}
	}

	return lo
}

// row is the row cluster g shows on. A cursor at a row's end shows at the
// next row's start, as the terminal's cursor needs a cell of its own.
func (l *layout) row(g int) int {
	r := 0
	for r+1 < len(l.rows) && l.rows[r+1] <= g {
		r++
	}

	return r
}

// newLayout segments text with the renderer's width rules
// (ansi.GraphemeWidth) and wraps it at width cells.
func newLayout(text []rune, width int) *layout {
	l := &layout{width: width, starts: make([]int, 0, len(text)+1)}
	s := string(text)
	off := 0
	for s != "" {
		cluster, w := ansi.FirstGraphemeCluster(s, ansi.GraphemeWidth)
		r, _ := utf8.DecodeRuneInString(cluster)
		l.starts = append(l.starts, off)
		l.cells = append(l.cells, w)
		l.spaces = append(l.spaces, unicode.IsSpace(r) && utf8.RuneCountInString(cluster) == 1)
		off += utf8.RuneCountInString(cluster)
		s = s[len(cluster):]
	}
	l.starts = append(l.starts, off)
	l.rows = wrap(l.cells, l.spaces, width)

	return l
}

// wrap breaks clusters into rows of at most width cells, at spaces where
// it can, as bubbles/textarea does: a word moves to the next row with the
// space after it, a word wider than the row is cut, and a run of spaces
// hangs off the end of its row until the row is full. The last row keeps
// a cell free for the cursor after the text, so a full last row is
// followed by an empty one. A cluster wider than width gets a row alone.
func wrap(cells []int, spaces []bool, width int) []int {
	width = max(width, 1)
	rows := []int{0}
	newRow := func(at int) {
		if at > rows[len(rows)-1] {
			rows = append(rows, at)
		}
	}
	rowW, word, wordW := 0, 0, 0 // word: the first cluster of the word being read
	for i, w := range cells {
		// A cluster of no width still takes the cursor's cell.
		w = max(w, 1)
		if !spaces[i] {
			if wordW+w > width { // the word alone fills a row: cut it
				if rowW > 0 {
					newRow(word)
				}
				newRow(i)
				rowW, word, wordW = 0, i, 0
			}
			wordW += w

			continue
		}
		if rowW > 0 && rowW+wordW+w > width {
			newRow(word)
			rowW = 0
		}
		rowW += wordW
		if rowW > 0 && rowW+w > width {
			newRow(i)
			rowW = 0
		}
		rowW += w
		word, wordW = i+1, 0
	}
	if rowW > 0 && rowW+wordW+1 > width {
		newRow(word)
		rowW = 0
	}
	if rowW+wordW+1 > width {
		newRow(len(cells))
	}

	return rows
}
