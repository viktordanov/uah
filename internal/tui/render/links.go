package render

import (
	"net/url"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/viktordanov/uah/internal/tui/state"
)

// File paths are drawn as links (state/links.go): underlined, inside an
// OSC 8 hyperlink to file://host/path, so a terminal's own cmd+click or
// ctrl+click opens them. The link is part of the line, as its colors are,
// so the cache keeps it with the item's lines, every width measure skips
// it, and a click finds it again in the line under the mouse (LinkAt).
// The line or range a link points at rides in the hyperlink's parameters
// ("line=12-20"), which terminals ignore. A link is drawn only where the
// line is cut, never wrapped, except a word of the agent's Markdown, which
// linkWords links on each of its two lines.

// linkClose ends a hyperlink.
const linkClose = "\x1b]8;;\x1b\\"

// linkConf is how the frames draw links: on or off, the machine's name
// for the URLs, and the workspace relative paths are under.
type linkConf struct {
	on              bool
	host, workspace string
}

// setLinks takes the state's link settings; a change draws every item
// again (cacheEntry.links).
func (st *Styles) setLinks(s state.State) {
	conf := linkConf{on: s.FileLinks != "" && s.FileLinks != state.LinksOff, host: s.Host, workspace: s.Settings.Workspace}
	if conf != st.links {
		st.links = conf
		st.linkGen++
	}
}

// fileLink is the link to path, made absolute under the workspace, at line
// to end (0: none).
func (st *Styles) fileLink(path string, line, end int) state.FileLink {
	if !filepath.IsAbs(path) && st.links.workspace != "" {
		path = filepath.Join(st.links.workspace, path)
	}

	return state.FileLink{Path: path, Line: line, End: end}
}

// linked draws text in style, underlined, as a link to l; without links,
// or for a path that is not absolute, it is just text in style.
func (st *Styles) linked(text string, style lipgloss.Style, l state.FileLink) string {
	if !st.links.on || !filepath.IsAbs(l.Path) || text == "" {
		return style.Render(text)
	}

	// The underline goes outside the style: lipgloss draws an underlined
	// text a character at a time.
	return linkOpen(st.links.host, l) + "\x1b[4m" + style.Render(text) + "\x1b[24m" + linkClose
}

// linkOpen starts a hyperlink to l: "ESC ] 8 ; line=12-20 ; file://host/path ST".
func linkOpen(host string, l state.FileLink) string {
	u := url.URL{Scheme: "file", Host: host, Path: l.Path}
	params := ""
	if l.Line > 0 {
		params = "line=" + strconv.Itoa(l.Line)
		if l.End > l.Line {
			params += "-" + strconv.Itoa(l.End)
		}
	}

	// A ";" ends the URL for some terminals; escaped, it reads the same.
	return "\x1b]8;" + params + ";" + strings.ReplaceAll(u.String(), ";", "%3B") + "\x1b\\"
}

// parseLink reads the link an OSC 8 sequence starts; ok is false for an
// end, another URL scheme, or another sequence.
func parseLink(seq string) (state.FileLink, bool) {
	body, ok := strings.CutPrefix(seq, "\x1b]8;")
	if !ok {
		return state.FileLink{}, false
	}
	body = strings.TrimSuffix(strings.TrimSuffix(body, "\x1b\\"), "\a")
	params, uri, ok := strings.Cut(body, ";")
	if !ok || uri == "" {
		return state.FileLink{}, false
	}
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "file" || !filepath.IsAbs(u.Path) {
		return state.FileLink{}, false
	}
	l := state.FileLink{Path: u.Path}
	for p := range strings.SplitSeq(params, ":") {
		if v, ok := strings.CutPrefix(p, "line="); ok {
			from, to, _ := strings.Cut(v, "-")
			l.Line, _ = strconv.Atoi(from)
			l.End, _ = strconv.Atoi(to)
		}
	}

	return l, true
}

// LinkAt is the file link drawn at screen cell (x, y) in the last frame's
// transcript; ok is false where there is none.
func (c *Cache) LinkAt(x, y int) (state.FileLink, bool) {
	row := y - c.top
	if row < 0 || row >= len(c.window) || x < 0 {
		return state.FileLink{}, false
	}

	return linkAtCell(c.window[row], x)
}

// linkAtCell is the link over cell x of a drawn line.
func linkAtCell(line string, x int) (state.FileLink, bool) {
	var (
		cur    state.FileLink
		in     bool
		col    int
		pstate byte
	)
	for line != "" {
		seq, width, n, next := ansi.DecodeSequence(line, pstate, nil)
		pstate, line = next, line[n:]
		if strings.HasPrefix(seq, "\x1b]8;") {
			cur, in = parseLink(seq)

			continue
		}
		if width == 0 {
			continue
		}
		if x < col+width {
			return cur, in
		}
		col += width
	}

	return state.FileLink{}, false
}

// linkWords links the words of a message's drawn lines that its Links
// name (state.LinkWord finds them as the reducer did), and a word the
// wrap split over two lines, on both.
func (st *Styles) linkWords(lines []string, links map[string]state.FileLink) []string {
	if !st.links.on || len(links) == 0 {
		return lines
	}
	plain := make([]string, len(lines))
	for i, l := range lines {
		plain[i] = ansi.Strip(l)
	}
	spans := make([][]cellSpan, len(lines))
	for i, p := range plain {
		for _, w := range words(p) {
			word, ok := state.LinkWord(w.text)
			at := strings.Index(w.text, word)
			if !ok || at < 0 {
				continue
			}
			if l, ok := links[word]; ok {
				from := w.cell + ansi.StringWidth(w.text[:at])
				spans[i] = append(spans[i], cellSpan{from: from, to: from + ansi.StringWidth(word), link: l})
			}
		}
		// A word split at the line's end: its rest starts the next line.
		if i+1 < len(plain) {
			st.splitWord(plain, spans, i, links)
		}
	}
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = st.linkCells(l, ordered(spans[i]))
	}

	return out
}

// ordered sorts a line's spans and drops any that overlaps the one before.
func ordered(spans []cellSpan) []cellSpan {
	slices.SortStableFunc(spans, func(a, b cellSpan) int { return a.from - b.from })
	out := spans[:0]
	for _, sp := range spans {
		if len(out) == 0 || sp.from >= out[len(out)-1].to {
			out = append(out, sp)
		}
	}

	return out
}

// splitWord links a word whose first part ends line i and whose rest
// starts line i+1, after its indent.
func (st *Styles) splitWord(plain []string, spans [][]cellSpan, i int, links map[string]state.FileLink) {
	head := words(plain[i])
	tail := words(plain[i+1])
	if len(head) == 0 || len(tail) == 0 || strings.HasSuffix(plain[i], " ") {
		return
	}
	a, b := head[len(head)-1], tail[0]
	word, ok := state.LinkWord(a.text + b.text)
	if !ok {
		return
	}
	l, ok := links[word]
	if !ok {
		return
	}
	if _, whole := state.LinkWord(a.text); whole {
		if _, linked := links[mustWord(a.text)]; linked {
			return // the first part is a link of its own
		}
	}
	at := strings.Index(a.text+b.text, word)
	if at < 0 || at >= len(a.text) || at+len(word) <= len(a.text) {
		return
	}
	spans[i] = append(spans[i], cellSpan{from: a.cell + ansi.StringWidth(a.text[:at]), to: a.cell + ansi.StringWidth(a.text), link: l})
	spans[i+1] = append(spans[i+1], cellSpan{from: b.cell, to: b.cell + ansi.StringWidth(word[len(a.text)-at:]), link: l})
}

func mustWord(w string) string {
	word, _ := state.LinkWord(w)

	return word
}

// word is a run of non-space text of a line: its bytes, and the byte and
// the cell it starts at.
type word struct {
	text      string
	col, cell int
}

// words splits a line without styles at its spaces.
func words(line string) []word {
	var out []word
	cell := 0
	start, startCell := -1, 0
	for i, r := range line {
		if r == ' ' {
			if start >= 0 {
				out = append(out, word{text: line[start:i], col: start, cell: startCell})
				start = -1
			}
			cell++

			continue
		}
		if start < 0 {
			start, startCell = i, cell
		}
		cell += ansi.StringWidth(string(r))
	}
	if start >= 0 {
		out = append(out, word{text: line[start:], col: start, cell: startCell})
	}

	return out
}

// cellSpan is a link over cells [from, to) of a line.
type cellSpan struct {
	from, to int
	link     state.FileLink
}

// linkCells draws the spans of a line as links: the hyperlink and the
// underline start at a span's first cell and end after its last, and the
// underline comes back after any style change inside it.
func (st *Styles) linkCells(line string, spans []cellSpan) string {
	if len(spans) == 0 {
		return line
	}
	var b strings.Builder
	col, k := 0, 0
	open := false
	var pstate byte
	for line != "" {
		seq, width, n, next := ansi.DecodeSequence(line, pstate, nil)
		pstate, line = next, line[n:]
		if width > 0 {
			if open && col >= spans[k].to {
				b.WriteString("\x1b[24m" + linkClose)
				open = false
				k++
			}
			if !open && k < len(spans) && col >= spans[k].from && col < spans[k].to {
				b.WriteString(linkOpen(st.links.host, spans[k].link) + "\x1b[4m")
				open = true
			}
			col += width
		}
		b.WriteString(seq)
		if open && width == 0 && strings.HasPrefix(seq, "\x1b[") && strings.HasSuffix(seq, "m") {
			b.WriteString("\x1b[4m") // a reset inside the link
		}
	}
	if open {
		b.WriteString("\x1b[24m" + linkClose)
	}

	return b.String()
}
