package render

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"

	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/internal/tui/render/markdown"
	"github.com/viktordanov/uah/internal/tui/state"
)

// What a copy of selected text holds: what is drawn, cut by cells, minus
// the decoration around it (see docs/design/selection.md).

// SelectedText is the selected text as it is drawn at f's width, for the
// clipboard, and its number of lines. Each line keeps only its item's text
// (keep); blank lines at either end go.
func SelectedText(s state.State, c *Cache, f Frame) (string, int) {
	start, end, ok := s.SelectedRange()
	if !ok {
		return "", 0
	}
	cp := copier{s: s, st: c.styles}
	add := cp.add
	if start.Key == state.BannerKey {
		add(state.BannerKey, 0, c.styles.banner(s, f.Version, f.Width), false)
	}
	for i, it := range s.Items {
		if it.Key == start.Key || len(cp.out) > 0 {
			lines := c.transcriptLines(&s, i, f.Width)
			add(it.Key, gutter(it), lines, len(lines) > len(c.ownLines(&s, i, f.Width)))
		}
		if it.Key == end.Key {
			break
		}
	}
	out := cp.out
	for len(out) > 0 && out[0] == "" {
		out = out[1:]
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}

	return strings.Join(out, "\n"), len(out)
}

// keep are the cells [from, to) of each of an item's drawn lines that a
// copy keeps. Every line loses the item's gutter; each block of lines
// between blank ones loses the indent its lines share after it (a code
// block's or a table's padding, a tool row's indent); a code line loses
// the fence's language at its right end; and a quote loses its “ ” marks
// and the indent under the first.
func (st *Styles) keep(lines []string, gutter int, wraps []wrapRow) [][2]int {
	keep := make([][2]int, len(lines))
	quote := false
	for i := 0; i < len(lines); {
		j := i
		for j < len(lines) && (strings.TrimSpace(ansi.Strip(lines[j])) != "" || wraps != nil && wraps[j].piece >= 0) {
			j++ // a wrapped code row is never a blank line, even all spaces
		}
		indent := sharedIndent(ownRows(lines, wraps, i, j), gutter)
		for k := i; k < j; k++ {
			keep[k] = st.keepLine(lines[k], gutter+indent, &quote)
		}
		i = max(j, i+1)
	}

	return keep
}

// keepLine is the cells a copy keeps of one line from cell left on.
func (st *Styles) keepLine(line string, left int, quote *bool) [2]int {
	plain := ansi.Strip(line)
	text := strings.TrimRight(plain, " ")
	to := ansi.StringWidth(text)
	if bg := strings.Index(line, "\x1b[48;"); bg > 0 {
		if i := strings.LastIndex(line, st.dimOn); i > bg && isLabel(ansi.Strip(line[i:])) {
			to = min(to, ansi.StringWidth(ansi.Strip(line[:i]))) // the fence's language
		}
	}
	rest := ansi.Cut(plain, left, to)
	switch {
	case strings.HasPrefix(rest, "“ "):
		left, *quote = left+2, true
	case *quote && strings.HasPrefix(rest, "  "):
		left += 2
	}
	if *quote && strings.HasSuffix(text, " ”") {
		to, *quote = to-2, false
	}

	return [2]int{left, to}
}

// isLabel reports whether text is one word and spaces: a code block's
// language.
func isLabel(text string) bool {
	word := strings.TrimSpace(text)

	return word != "" && !strings.Contains(word, " ")
}

// sharedIndent is how many spaces all non-blank lines have after cell
// left.
func sharedIndent(lines []string, left int) int {
	indent := -1
	for _, l := range lines {
		plain := ansi.Strip(l)
		rest := ansi.Cut(plain, left, ansi.StringWidth(plain))
		n := len(rest) - len(strings.TrimLeft(rest, " "))
		if indent < 0 || n < indent {
			indent = n
		}
	}

	return max(indent, 0)
}

// copier gathers a copy's lines, item by item.
type copier struct {
	s   state.State
	st  *Styles
	out []string
}

// add copies the selected part of an item's drawn lines: each line's kept
// cells (keep), and a wrapped code line whole, its pieces joined without a
// break (wrapRows). spaced says the item's first line is the blank line
// above it.
func (cp *copier) add(key string, gutter int, lines []string, spaced bool) {
	wraps := wrapRows(lines)
	keep := cp.st.keep(lines, gutter, wraps)
	last := -1         // the line copied last
	var parts []string // the pieces of the wrapped line copied last, joined once it ends
	flush := func() {
		if parts != nil {
			cp.out[len(cp.out)-1], parts = strings.Join(parts, ""), nil
		}
	}
	defer flush()
	for l, line := range lines {
		if l == 0 && spaced && len(cp.out) > 0 && cp.out[len(cp.out)-1] == "" {
			continue // the blank line above an item that stands apart, after one the copy has
		}
		from, to, ok := cp.s.SelectedCols(key, l)
		if !ok {
			continue
		}
		plain := ansi.Strip(line)
		if wraps == nil || wraps[l].piece < 0 {
			flush()
			cp.out = append(cp.out, copyCells(plain, max(from, keep[l][0]), min(to, keep[l][1])))
			last = l

			continue
		}
		start, end := wrapCells(lines, wraps, keep, l)
		text := cutCells(plain, max(from, start), min(to, end))
		if wraps[l].piece > 0 && last == l-1 && len(cp.out) > 0 {
			if parts == nil {
				parts = []string{cp.out[len(cp.out)-1]}
			}
			parts = append(parts, text) // the line above goes on here, without a break
		} else {
			flush()
			cp.out = append(cp.out, text)
		}
		last = l
	}
}

// wrapRow is a drawn row of a wrapped code line (markdown.WrapTag): its
// piece's number and width in cells; piece is -1 for any other row.
type wrapRow struct{ piece, cells int }

var wrapTag = regexp.MustCompile(`\x1b\]8;` + markdown.WrapParam + `=(\d+),(\d+);`)

// wrapRows reads the wrapped code rows of an item's lines; nil when it has
// none.
func wrapRows(lines []string) []wrapRow {
	var rows []wrapRow
	for i, l := range lines {
		m := wrapTag.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		if rows == nil {
			rows = make([]wrapRow, len(lines))
			for k := range rows {
				rows[k].piece = -1
			}
		}
		rows[i].piece, _ = strconv.Atoi(m[1])
		rows[i].cells, _ = strconv.Atoi(m[2])
	}

	return rows
}

// ownRows are lines [i, j) less the continuations of wrapped code lines,
// which start mid-line and so have no indent of their own.
func ownRows(lines []string, wraps []wrapRow, i, j int) []string {
	if wraps == nil {
		return lines[i:j]
	}
	var out []string
	for k := i; k < j; k++ {
		if wraps[k].piece <= 0 {
			out = append(out, lines[k])
		}
	}

	return out
}

// wrapCells is the cells [start, end) of row l's piece of a wrapped code
// line: a continuation's, after the mark in the padding column, every
// cell; the first piece's from where the block's lines keep their code
// (keep), dedented as they are, to its width after the padding column,
// which its next row marks. The padding after a piece, where a wide
// character did not fit, is not the code's.
func wrapCells(lines []string, wraps []wrapRow, keep [][2]int, l int) (int, int) {
	w := wraps[l]
	if w.piece > 0 {
		at := markCell(lines[l]) + 1

		return at, at + w.cells
	}
	if l+1 < len(wraps) && wraps[l+1].piece > 0 {
		return keep[l][0], markCell(lines[l+1]) + 1 + w.cells
	}

	return keep[l][0], keep[l][1]
}

// markCell is the cell of a continuation's markdown.WrapMark.
func markCell(line string) int {
	before, _, ok := strings.Cut(ansi.Strip(line), markdown.WrapMark)
	if !ok {
		return 0
	}

	return ansi.StringWidth(before)
}

// cutCells is cells [from, to) of plain text, a wide character half inside
// counted whole, with its trailing spaces.
func cutCells(plain string, from, to int) string {
	if from >= to {
		return ""
	}
	from, to = snap(plain, from, to)

	return ansi.Cut(plain, from, to)
}

// copyCells is cells [from, to) of plain text, a wide character half
// inside counted whole, without trailing spaces.
func copyCells(plain string, from, to int) string {
	return strings.TrimRight(cutCells(plain, from, to), " ")
}

// snap widens cells [from, to) of text to whole characters, so a wide
// character half inside counts, and ends it at the text's end.
func snap(text string, from, to int) (int, int) {
	start, end, at := from, 0, 0
	g := uniseg.NewGraphemes(text)
	for g.Next() {
		next := at + g.Width()
		if at <= from && from < next {
			start = at
		}
		if at < to {
			end = next
		}
		at = next
	}

	return start, min(end, at)
}

// gutter is how many cells before an item's text are decoration: the λ or
// ! mark and the indent under it, the agent's •, reasoning's ~, and a
// warning's or an error's mark.
func gutter(it state.Item) int {
	switch it.Kind {
	case state.KindUser, state.KindShell, state.KindAssistant:
		return 2
	case state.KindReasoning:
		return 4
	case state.KindNotice:
		if it.Level == session.LevelWarning || it.Level == session.LevelError {
			return 4
		}

		return 2
	}

	return 0
}
