// Package markdown draws the Markdown the model writes as terminal lines,
// in uah's look after Codex's: goldmark parses it with the GFM extensions,
// and a renderer of its own walks the tree. A Renderer keeps the finished
// top-level blocks of what it drew, so a message that grows re-renders only
// its last block. It does no I/O and has no global state: each Renderer has
// its own styles and caches. docs/design/markdown.md has the design.
package markdown

import (
	"slices"
	"strings"
	"sync"

	"github.com/alecthomas/chroma/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
)

// Style draws text; lipgloss styles are Styles.
type Style interface {
	Render(strs ...string) string
}

// Styles are what a Renderer draws with.
type Styles struct {
	Bold, Italic, Strike Style
	// Code is `code`, and code blocks that are not highlighted.
	Code Style
	// Dim draws quotes, list markers, rules, link URLs, and code labels.
	Dim Style
	// H1 and H2 draw the first two heading levels (H1 in capitals), and
	// Heading the others.
	H1, H2, Heading Style
	TableHeader     Style
	// Band draws one line of a code block or a table row on the band,
	// padded to width w.
	Band func(line string, w int) string
	// Added and Removed draw a diff block's + and - lines as Band does, on
	// the edit tool's tints; nil draws them on the band.
	Added, Removed func(line string, w int) string
	// Alerts draw a GitHub alert's title by its kind (NOTE, TIP, IMPORTANT,
	// WARNING, CAUTION); a missing kind is bold.
	Alerts map[string]Style
	// CodeStyle colors highlighted code; nil leaves all code in Code.
	CodeStyle *chroma.Style
}

// maxDocs is how many documents a Renderer keeps finished blocks for.
const maxDocs = 16

// Renderer draws Markdown with one set of Styles, keeping finished blocks
// and highlighted code. It is safe for concurrent use.
type Renderer struct {
	mu sync.Mutex
	st Styles
	// open and close are a quote's dim marks, and closeAlone the closing
	// one on a line of its own.
	open, close, closeAlone string
	parser                  parser.Parser
	// links draws web links as hyperlinks (SetLinks).
	links bool
	// docs are the documents drawn last, the most recent first.
	docs []*doc
	code codeCache
	// stats count the work done, for the tests that bound it.
	stats stats
}

type stats struct {
	// parsed is the bytes parsed and blocks the top-level blocks drawn.
	parsed, blocks int
}

// New is a Renderer that draws with st.
func New(st Styles) *Renderer {
	md := goldmark.New(goldmark.WithExtensions(extension.GFM))

	if st.Added == nil {
		st.Added = st.Band
	}
	if st.Removed == nil {
		st.Removed = st.Band
	}

	return &Renderer{st: st, open: st.Dim.Render("“ "), close: st.Dim.Render(" ”"), closeAlone: st.Dim.Render("”"), parser: md.Parser(), code: newCodeCache()}
}

// SetLinks turns web links (weblinks.go) on or off; a change drops the
// kept documents, whose lines carry the old setting.
func (r *Renderer) SetLinks(on bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.links != on {
		r.links, r.docs = on, nil
	}
}

// doc is a document drawn at one width with one pair of prefixes: the
// source of its finished blocks, their lines, and the last result.
type doc struct {
	width       int
	first, rest string
	stable      string
	lines       []string
	text        string
	out         []string
}

// Render draws text at width w. The first line starts with first and the
// others with rest (both already styled and of equal width). Only the text
// after the finished blocks of the same document drawn before is parsed,
// so a text that grows costs its last block, not the whole.
func (r *Renderer) Render(text string, w int, first, rest string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	d := r.find(text, max(w-ansi.StringWidth(rest), 10), first, rest)
	if d.out == nil || d.text != text {
		d.out, d.text = r.update(d, text), text
	}

	return slices.Clone(d.out)
}

// find returns the kept document text continues, or a new one. A document
// whose last text text does not extend is forked, so two texts that share
// finished blocks do not take turns overwriting one document.
func (r *Renderer) find(text string, w int, first, rest string) *doc {
	best := -1
	for i, d := range r.docs {
		if d.width == w && d.first == first && d.rest == rest && strings.HasPrefix(text, d.stable) &&
			(best < 0 || len(d.stable) > len(r.docs[best].stable)) {
			best = i
		}
	}
	var d *doc
	switch {
	case best < 0:
		d = &doc{width: w, first: first, rest: rest}
	case strings.HasPrefix(text, r.docs[best].text):
		d = r.docs[best]
		r.docs = slices.Delete(r.docs, best, best+1)
	default:
		b := r.docs[best]
		d = &doc{width: w, first: first, rest: rest, stable: b.stable, lines: slices.Clip(b.lines)}
	}
	if len(r.docs) >= maxDocs {
		r.docs = r.docs[:maxDocs-1]
	}
	r.docs = slices.Insert(r.docs, 0, d)

	return d
}

// update draws text for d: it parses the text after d's finished blocks,
// keeps every top-level block but the last as finished, and draws the last.
func (r *Renderer) update(d *doc, text string) []string {
	src := []byte(text[len(d.stable):])
	blocks, refs := r.parse(src)
	if refs {
		// A link reference definition changes links in other blocks, as
		// in Codex: draw the whole text and keep nothing.
		src = []byte(text)
		blocks, _ = r.parse(src)

		return orFirst(r.join(d, nil, src, blocks), d.first)
	}
	if n := len(blocks); n > 1 {
		if start := lineStart(src, blocks[n-1].Pos()); start > 0 {
			d.lines = r.join(d, d.lines, src, blocks[:n-1])
			d.stable = text[:len(d.stable)+start]
			blocks = blocks[n-1:]
		}
	}

	return orFirst(r.join(d, slices.Clip(d.lines), src, blocks), d.first)
}

// orFirst is lines, or the first prefix alone for none.
func orFirst(lines []string, first string) []string {
	if len(lines) == 0 {
		return []string{first}
	}

	return lines
}

// parse returns the top-level blocks of src and whether it defines a link
// reference.
func (r *Renderer) parse(src []byte) ([]ast.Node, bool) {
	r.stats.parsed += len(src)
	pc := parser.NewContext()
	root := r.parser.Parse(text.NewReader(src), parser.WithContext(pc))
	var blocks []ast.Node
	for n := root.FirstChild(); n != nil; n = n.NextSibling() {
		blocks = append(blocks, n)
	}

	return blocks, len(pc.References()) > 0
}

// join appends blocks to out with d's prefixes and a blank line between
// blocks.
func (r *Renderer) join(d *doc, out []string, src []byte, blocks []ast.Node) []string {
	for _, b := range blocks {
		r.stats.blocks++
		lines := r.block(src, b, frame{w: d.width})
		if len(lines) == 0 {
			continue
		}
		if len(out) > 0 {
			out = append(out, d.rest)
		}
		for _, l := range lines {
			prefix := d.rest
			if len(out) == 0 {
				prefix = d.first
			}
			out = append(out, prefix+l)
		}
	}

	return out
}

// lineStart is the start of the line holding pos, or -1 for no position.
func lineStart(src []byte, pos int) int {
	if pos < 0 || pos > len(src) {
		return -1
	}
	for pos > 0 && src[pos-1] != '\n' {
		pos--
	}

	return pos
}
