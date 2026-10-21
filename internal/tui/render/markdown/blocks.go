package markdown

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/yuin/goldmark/ast"
	east "github.com/yuin/goldmark/extension/ast"
)

// frame is where a block is drawn: its width, whether it sits in a
// quote, whose text is dim and italic, and how deep in lists it is.
type frame struct {
	w      int
	quoted bool
	depth  int
}

// base is the style a frame's text starts with.
func (r *Renderer) base(f frame) []Style {
	if f.quoted {
		return []Style{r.st.Dim, r.st.Italic}
	}

	return nil
}

// block draws one block node at the frame's width.
func (r *Renderer) block(src []byte, n ast.Node, f frame) []string {
	switch n := n.(type) {
	case *ast.Paragraph, *ast.TextBlock:
		return wrap(r.inline(src, n, r.base(f)), f.w)
	case *ast.Heading:
		return wrap(r.heading(src, n, f), f.w)
	case *ast.ThematicBreak:
		return []string{r.st.Dim.Render(strings.Repeat("─", min(f.w, 40)))}
	case *ast.FencedCodeBlock:
		return r.codeBlock(string(n.Language(src)), lines(src, n), f.w)
	case *ast.CodeBlock:
		return r.codeBlock("", lines(src, n), f.w)
	case *ast.Blockquote:
		return r.quote(src, n, f)
	case *ast.List:
		return r.list(src, n, f)
	case *ast.HTMLBlock:
		html := lines(src, n)
		if n.HasClosure() {
			html += string(n.ClosureLine.Value(src))
		}

		return wrap(untab(strings.TrimRight(html, "\n")), f.w)
	case *east.Table:
		return r.table(src, n, f)
	}

	return r.children(src, n, f, false)
}

// children draws a container's blocks, with a blank line between them
// unless tight.
func (r *Renderer) children(src []byte, n ast.Node, f frame, tight bool) []string {
	var out []string
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		lines := r.block(src, c, f)
		if len(lines) == 0 {
			continue
		}
		if len(out) > 0 && !tight {
			out = append(out, "")
		}
		out = append(out, lines...)
	}

	return out
}

// heading draws a heading: H1 in its style and in capitals, H2 in its
// own, and the rest in Heading.
func (r *Renderer) heading(src []byte, n *ast.Heading, f frame) string {
	style := r.st.Heading
	switch n.Level {
	case 1:
		style = r.st.H1
	case 2:
		style = r.st.H2
	}
	b := runs{upper: n.Level == 1}
	r.inlines(&b, src, n, append(r.base(f), style))

	return b.String()
}

// list draws a list: a dim "•" ("◦" when nested) or number before each
// item, and its lines under the item's first with a hanging indent. The
// numbers are right-aligned, so every item's text starts in one column.
func (r *Renderer) list(src []byte, l *ast.List, f frame) []string {
	width := 1
	if l.IsOrdered() {
		width = len(strconv.Itoa(l.Start+l.ChildCount()-1)) + 1
	}
	hang := strings.Repeat(" ", width+1)
	inner := frame{w: f.w - len(hang), quoted: f.quoted, depth: f.depth + 1}
	var out []string
	i := 0
	for item := l.FirstChild(); item != nil; item = item.NextSibling() {
		body := r.children(src, item, inner, l.IsTight)
		if len(body) == 0 {
			body = []string{""}
		}
		if i > 0 && !l.IsTight {
			out = append(out, "")
		}
		out = append(out, r.marker(l, f.depth, i, width)+" "+body[0])
		for _, b := range body[1:] {
			if b != "" {
				b = hang + b
			}
			out = append(out, b)
		}
		i++
	}

	return out
}

// marker is item i's dim bullet or number, right-aligned to width cells.
func (r *Renderer) marker(l *ast.List, depth, i, width int) string {
	m := "•"
	switch {
	case l.IsOrdered():
		m = strconv.Itoa(l.Start+i) + string(l.Marker)
	case depth > 0:
		m = "◦"
	}

	return strings.Repeat(" ", width-ansi.StringWidth(m)) + r.st.Dim.Render(m)
}

// lines is the text of a block's source lines. goldmark ends a code
// block's last line with a newline even at the end of the text; lines does
// not, so an open fence's partial line shows as partial.
func lines(src []byte, n ast.Node) string {
	var b strings.Builder
	segs := n.Lines()
	for i := range segs.Len() {
		seg := segs.At(i)
		v := seg.Value(src)
		if seg.ForceNewline && seg.Stop > 0 && src[seg.Stop-1] != '\n' {
			v = v[:len(v)-1]
		}
		b.Write(v)
	}

	return b.String()
}

// wrap word-wraps styled text to width w; its newlines stay line breaks.
func wrap(s string, w int) []string {
	return carry(strings.Split(ansi.Wrap(s, max(w, 10), ""), "\n"))
}

// carry ends each line's styles with it and opens them again on the next,
// so a style that wraps survives what is drawn before the next line, such
// as a quote bar that resets the terminal's style.
func carry(lines []string) []string {
	if len(lines) < 2 {
		return lines
	}
	var open []string
	for i, l := range lines {
		reopen := strings.Join(open, "")
		for _, seq := range sgr.FindAllString(l, -1) {
			if isReset(seq) {
				open = open[:0]
			} else {
				open = append(open, seq)
			}
		}
		if len(open) > 0 {
			l += sgrReset
		}
		lines[i] = reopen + l
	}

	return lines
}

// sgrReset ends every style.
const sgrReset = "\x1b[m"

// isReset reports whether an SGR sequence ends every style.
func isReset(seq string) bool { return seq == sgrReset || seq == "\x1b[0m" }

// sgr matches one SGR escape sequence.
var sgr = regexp.MustCompile("\x1b\\[[0-9;:]*m")

func untab(s string) string { return strings.ReplaceAll(s, "\t", "    ") }
