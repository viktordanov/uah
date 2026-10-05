package render

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/viktordanov/uah/internal/tui/render/markdown"
	"github.com/viktordanov/uah/internal/tui/state"
)

// The peek overlay (state/peek.go) draws a file over the session, in the
// panel's frame: centered, about 80% of the screen each way, or all of it
// on a screen under 60x20. Its title is the file's path relative to the
// workspace and the link's lines; its body the file's lines with their
// numbers, the link's lines on the band. The screen under it is drawn as
// usual and only the overlay's cells are replaced, so nothing behind it
// moves and the session goes on.

// Below peekMinW by peekMinH the overlay takes the whole screen.
const (
	peekMinW = 60
	peekMinH = 20
)

// PeekRect is where the overlay is on a w by h screen: its top left cell
// and its size.
func PeekRect(w, h int) (x, y, pw, ph int) {
	if w < peekMinW || h < peekMinH {
		return 0, 0, w, h
	}
	pw, ph = max(w*4/5, peekMinW), max(h*4/5, peekMinH)

	return (w - pw) / 2, (h - ph) / 2, pw, ph
}

// PeekRows is how many of the file's lines the overlay shows on a w by h
// screen: its height inside the frame.
func PeekRows(w, h int) int {
	if w < peekMinW || h < peekMinH {
		return max(h-2, 1)
	}

	return max(h*4/5, peekMinH) - 2
}

// Highlight colors a file's lines for the overlay by the language its name
// says, as the theme colors code blocks. It keeps no cache, so the shell
// calls it where it reads the file, off the update loop.
func (st *Styles) Highlight(name string, lines []string) []string {
	return markdown.Highlight(st.codeStyle, name, lines)
}

// overlayPeek draws the overlay over a screen's lines.
func (st *Styles) overlayPeek(lines []string, s state.State, w, h int) []string {
	x, y, pw, ph := PeekRect(w, h)
	for len(lines) < h {
		lines = append(lines, "")
	}
	for i, row := range st.peekLines(s, pw, ph) {
		base := lines[y+i]
		left := leftCells(base, x)
		if n := ansi.StringWidth(left); n < x {
			left += strings.Repeat(" ", x-n)
		}
		// The overlay starts and ends with no style or link in force.
		lines[y+i] = left + "\x1b[m" + linkClose + row + "\x1b[m" + ansi.TruncateLeft(base, x+pw, "")
	}

	return lines
}

// leftCells is a line's first n cells with the styles in force there,
// without the rest of its escape sequences (ansi.Truncate keeps them); a
// wide character across the edge is left out.
func leftCells(line string, n int) string {
	col, end := 0, 0
	var pstate byte
	for rest := line; rest != ""; {
		_, width, k, next := ansi.DecodeSequence(rest, pstate, nil)
		if col+width > n {
			break
		}
		col += width
		pstate, rest = next, rest[k:]
		end = len(line) - len(rest)
	}

	return line[:end]
}

// peekLines are the overlay's rows, pw by ph.
func (st *Styles) peekLines(s state.State, pw, ph int) []string {
	p := s.Peek
	rows := max(ph-2, 1)
	inner := max(pw-2, 1)
	var body []string
	switch {
	case p.Loading:
		body = append(body, st.peekRow(st.dim.Render("reading…"), inner, false))
	case p.Note != "":
		body = append(body, st.peekRow(st.warn.Render(p.Note), inner, false))
	}
	top := clampTop(p.Top, len(p.Lines), rows-len(body))
	gw := len(strconv.Itoa(max(len(p.Lines), 1)))
	end := min(top+rows-len(body), len(p.Lines))
	for i := top; i < end; i++ {
		n := i + 1
		mark := n >= p.Link.Line && n <= max(p.Link.End, p.Link.Line) && p.Link.Line > 0
		gutter := st.dim.Render(fmt.Sprintf("%*d ", gw, n))
		body = append(body, st.peekRow(gutter+untab(p.Lines[i]), inner, mark))
	}
	for len(body) < rows {
		body = append(body, st.peekRow("", inner, false))
	}
	title := st.accent.Render(peekTitle(s))
	hint := "esc close · e editor · o open · ↑↓ pgup pgdn g G scroll"
	if len(p.Lines) > 0 {
		hint = fmt.Sprintf("%d-%d of %d · ", top+1, end, len(p.Lines)) + hint
	}
	if panelShown(s) {
		hint = "waiting for you below · " + hint
	}
	if pw < panelMin {
		out := []string{title}

		return append(out, body[:max(rows-1, 0)]...)
	}
	out := []string{st.edge("╭", "╮", ansi.Truncate(title, max(pw-7, 1), "…"), pw)}
	for _, l := range body {
		out = append(out, st.tool.Render("│")+l+st.tool.Render("│"))
	}

	return append(out, st.edge("╰", "╯", st.dim.Render(ansi.Truncate(hint, max(pw-7, 1), "…")), pw))
}

// peekRow is one row inside the frame, w cells: a space, then the line cut
// to fit, on the band when marked, padded so it covers what is behind.
func (st *Styles) peekRow(line string, w int, marked bool) string {
	line = " " + ansi.Truncate(line, max(w-2, 0), "…")
	if marked {
		return onBackground(st.bandOn, line, w)
	}

	return line + "\x1b[m" + strings.Repeat(" ", max(w-ansi.StringWidth(line), 0))
}

// peekTitle is the file's path, relative to the workspace or under ~, and
// the link's lines: "internal/tui/state/peek.go:12-20".
func peekTitle(s state.State) string {
	l := s.Peek.Link
	path := l.Path
	if ws := s.Settings.Workspace; ws != "" {
		if rel, err := filepath.Rel(ws, path); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			path = rel
		}
	}
	if filepath.IsAbs(path) {
		path = home(s.Home, path)
	}
	switch {
	case l.End > l.Line && l.Line > 0:
		path += fmt.Sprintf(":%d-%d", l.Line, l.End)
	case l.Line > 0:
		path += ":" + strconv.Itoa(l.Line)
	}

	return path
}

// clampTop keeps the first line shown within the text, as the reducer
// does, for a screen resized since.
func clampTop(top, lines, rows int) int {
	return max(min(top, lines-max(rows, 1)), 0)
}
