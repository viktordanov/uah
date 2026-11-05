package markdown

import (
	"bytes"
	"slices"
	"strings"

	"github.com/yuin/goldmark/ast"
	east "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/util"
)

// inline draws a block's inline content as one styled string: `code`,
// bold, italic, strikethrough, and links with their URL dim after them.
// Soft and hard line breaks are newlines, as in Codex. Every piece of text
// is drawn with all the styles around it, innermost first, so a style
// ends with its text and never cuts the one around it short.
func (r *Renderer) inline(src []byte, n ast.Node, styles []Style) string {
	b := runs{links: r.links}
	r.inlines(&b, src, n, styles)

	return b.String()
}

// inlines draws a node's inline children. Prose nodes in a row, up to a
// line break, are drawn as one text, so a URL goldmark split (at a "_" or
// a "[" that made nothing, or after an autolink it cut before a character
// beyond ASCII) is found whole.
func (r *Renderer) inlines(b *runs, src []byte, parent ast.Node, styles []Style) {
	for n := parent.FirstChild(); n != nil; n = n.NextSibling() {
		v, brk, ok := prose(src, n, b.links)
		if !ok {
			r.inlineNode(b, src, n, styles)

			continue
		}
		s := append([]byte(nil), v...)
		for !brk {
			v, next, ok := prose(src, n.NextSibling(), b.links)
			if !ok {
				break
			}
			s = append(s, v...)
			n, brk = n.NextSibling(), next
		}
		b.text(string(s), styles, b.upper)
		if brk {
			b.put("\n", nil)
		}
	}
}

// prose is the text of a node drawn as prose, and whether a line break
// follows it: a text node's, and with links on, the label of a web URL
// the GFM extension found in the text, which text searches again with
// the text around it. ok is false for any other node.
func prose(src []byte, n ast.Node, links bool) (v []byte, brk, ok bool) {
	switch n := n.(type) {
	case *ast.Text:
		v = n.Segment.Value(src)
		if !n.IsRaw() {
			v = unescape(v)
		}

		return v, n.SoftLineBreak() || n.HardLineBreak(), true
	case *ast.AutoLink:
		label := n.Label(src)
		if links && n.AutoLinkType == ast.AutoLinkURL && n.Protocol == nil && !delimited(src, n) && webScheme(label) {
			return label, false, true
		}
	}

	return nil, false, false
}

// delimited reports whether an autolink is written in <>.
func delimited(src []byte, n *ast.AutoLink) bool {
	at := n.Pos()

	return at >= 0 && at < len(src) && src[at] == '<'
}

// webScheme reports whether s starts with http:// or https://, in any case.
func webScheme(s []byte) bool {
	return len(s) >= 7 && bytes.EqualFold(s[:7], []byte("http://")) || len(s) >= 8 && bytes.EqualFold(s[:8], []byte("https://"))
}

func (r *Renderer) inlineNode(b *runs, src []byte, n ast.Node, styles []Style) {
	switch n := n.(type) {
	case *ast.String:
		b.text(string(n.Value), styles, b.upper)
	case *ast.CodeSpan:
		var code runs
		r.inlines(&code, src, n, nil)
		b.put(strings.ReplaceAll(code.String(), "\n", " "), with(styles, r.st.Code))
	case *ast.Emphasis:
		style := r.st.Italic
		if n.Level > 1 {
			style = r.st.Bold
		}
		r.inlines(b, src, n, with(styles, style))
	case *east.Strikethrough:
		r.inlines(b, src, n, with(styles, r.st.Strike))
	case *ast.Link, *ast.Image, *ast.AutoLink:
		r.link(b, src, n, styles)
	case *ast.RawHTML:
		for i := range n.Segments.Len() {
			seg := n.Segments.At(i)
			b.put(string(seg.Value(src)), styles)
		}
	case *east.TaskCheckBox:
		box := "[ ]"
		if n.IsChecked {
			box = "[x]"
		}
		b.put(box, with(styles, r.st.Dim))
		b.put(" ", styles)
	default:
		r.inlines(b, src, n, styles)
	}
}

// link draws a link's text and its URL dim in parentheses, an image's
// description and its URL, and an autolink as its URL. With links on, a
// web destination (webTarget) is the target of the text and of the URL
// after it, and an autolink's URL is found as a bare one is.
func (r *Renderer) link(b *runs, src []byte, n ast.Node, styles []Style) {
	var url string
	switch n := n.(type) {
	case *ast.AutoLink:
		label := string(n.Label(src))
		// An autolink in <> is delimited: its whole label is the URL.
		// One the GFM extension found in the text is found again as a
		// bare URL is, for the same ends.
		if !delimited(src, n) {
			b.text(label, styles, false)

			return
		}
		outer := b.link
		if target, ok := webTarget(label); ok && b.links && !b.inLink {
			b.link = target
		}
		b.put(label, styles)
		b.link = outer

		return
	case *ast.Link:
		url = string(n.Destination)
	case *ast.Image:
		url = string(n.Destination)
	}
	outer, inside := b.link, b.inLink
	if target, ok := webTarget(string(unescape([]byte(url)))); ok && b.links && !inside {
		b.link = target
	}
	target := b.link
	b.inLink = true
	r.inlines(b, src, n, styles)
	b.inLink = inside
	if url != "" && url != plainText(src, n) {
		dim := with(styles, r.st.Dim)
		b.link = outer
		b.put(" ", styles)
		b.put("(", dim)
		b.link = target
		b.put(url, dim)
		b.link = outer
		b.put(")", dim)
	}
	b.link = outer
}

// runs builds styled text. Text in a row with the same styles (the same
// slice, as siblings share) and the same link is drawn as one run. With
// upper, text (not code or URLs) is drawn in capitals. With links, a run
// with a link is drawn inside its hyperlink, and prose links its bare
// URLs; code never does.
type runs struct {
	b      strings.Builder
	run    strings.Builder
	styles []Style
	upper  bool
	links  bool
	// link is the target of the text put now, and runLink the run's;
	// inLink is set inside a link's or an image's text, whatever its
	// destination.
	link, runLink string
	inLink        bool
}

// text adds prose drawn with styles, in capitals with upper but its URLs.
// With links on and outside a link's text, each bare URL in it (webURLs)
// is inside a hyperlink to itself.
func (b *runs) text(s string, styles []Style, upper bool) {
	caps := func(s string) string {
		if upper {
			return strings.ToUpper(s)
		}

		return s
	}
	if !b.links || b.inLink {
		b.put(caps(s), styles)

		return
	}
	at := 0
	for _, u := range webURLs(s) {
		b.put(caps(s[at:u.from]), styles)
		b.link = u.target
		b.put(s[u.from:u.to], styles)
		b.link = ""
		at = u.to
	}
	b.put(caps(s[at:]), styles)
}

// put adds s drawn with styles, the outermost first in the list, in the
// current link.
func (b *runs) put(s string, styles []Style) {
	if s == "" {
		return
	}
	if len(styles) != len(b.styles) || len(styles) > 0 && &styles[0] != &b.styles[0] || b.link != b.runLink {
		b.flush()
		b.styles, b.runLink = styles, b.link
	}
	b.run.WriteString(untab(s))
}

func (b *runs) flush() {
	s := b.run.String()
	b.run.Reset()
	if s == "" {
		return
	}
	for _, style := range slices.Backward(b.styles) {
		s = style.Render(s)
	}
	if b.runLink != "" {
		s = Hyperlink("", b.runLink) + s + LinkClose
	}
	b.b.WriteString(s)
}

func (b *runs) String() string {
	b.flush()

	return b.b.String()
}

// with is styles and one more inside them, without sharing the array.
func with(styles []Style, s Style) []Style {
	return append(slices.Clip(styles), s)
}

// unescape resolves backslash escapes and character references, as
// goldmark's HTML renderer does.
func unescape(v []byte) []byte {
	if bytes.IndexByte(v, '\\') >= 0 {
		v = util.UnescapePunctuations(v)
	}
	if bytes.IndexByte(v, '&') >= 0 {
		v = util.ResolveEntityNames(util.ResolveNumericReferences(v))
	}

	return v
}

// plainText is an inline node's text without styles.
func plainText(src []byte, n ast.Node) string {
	var b strings.Builder
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		if t, ok := c.(*ast.Text); ok {
			b.Write(t.Segment.Value(src))
		} else {
			b.WriteString(plainText(src, c))
		}
	}

	return b.String()
}
