package state

import (
	"cmp"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/viktordanov/uah/internal/session"
)

// File paths in the transcript are links ([tui] file_links): the paths of
// the files a tool call reads, a patch's and /diff's file headers, a
// review finding's place, and the words of the agent's Markdown that name
// a file in the workspace. The renderer draws them as OSC 8 hyperlinks, so
// the terminal's own cmd+click works, and a click with the mouse reported
// does what file_links says: peek at the file in an overlay (peek.go), or
// open it in the editor or with the system's default app. See
// docs/design/file-links.md.

// What a click on a link does, [tui] file_links' values.
const (
	LinksPeek   = "peek"
	LinksEditor = "editor"
	LinksOpen   = "open"
	LinksOff    = "off"
)

// LinksModes are file_links' values, the default first.
var LinksModes = []string{LinksPeek, LinksEditor, LinksOpen, LinksOff}

// FileLink is a file a link opens: its absolute path, and the line or the
// lines it points at (1-based; 0 when unknown).
type FileLink struct {
	Path      string
	Line, End int
}

type (
	// OpenLink opens a link as file_links says: a click on it, which the
	// shell finds with render.Cache.LinkAt.
	OpenLink struct{ Link FileLink }
	// EffEditFile opens the file in the editor at its line, and EffOpenFile
	// with the system's default app; a failure comes back as FileOpened.
	EffEditFile struct{ Link FileLink }
	EffOpenFile struct{ Link FileLink }
	// FileOpened reports a file that could not be opened.
	FileOpened struct {
		Link FileLink
		Err  error
	}
	// EffResolveLinks asks which words of the agent's messages name a
	// regular file in the workspace (Home for ~); the answer is
	// LinksResolved.
	EffResolveLinks struct {
		Workspace, Home string
		Messages        []LinkWords
	}
	// LinkWords are the words of one message that may name a file, for
	// the message with Key and Text.
	LinkWords struct {
		Key, Text string
		Words     []string
	}
	// LinksResolved are the words that name files, per message: Links
	// maps a word, as LinkWord gives it, to its file.
	LinksResolved struct{ Messages []ResolvedLinks }
	// ResolvedLinks are one message's links, for the text they were found
	// in.
	ResolvedLinks struct {
		Key, Text string
		Links     map[string]FileLink
	}
)

func (EffEditFile) effect()     {}
func (EffOpenFile) effect()     {}
func (EffResolveLinks) effect() {}

// linksOn reports whether paths are links.
func (s State) linksOn() bool { return s.FileLinks != "" && s.FileLinks != LinksOff }

// onLinks handles opening links and the words found to name files; ok is
// false for any other event. A press keeps its link and a release that
// selected nothing (a click, not a drag or a double click) opens it, after
// the selection has seen both.
func (s *State) onLinks(ev any) (effects []Effect, ok bool) {
	switch e := ev.(type) {
	case MousePress:
		s.pressed = e.Link

		return nil, false
	case MouseRelease:
		sel, link := s.Selection, s.pressed
		click := sel != nil && sel.Dragging && !sel.moved && s.click.n == 1
		s.pressed = nil
		effects, _ = s.onSelection(ev)
		if click && link != nil {
			effects = append(effects, s.openLink(*link)...)
		}

		return effects, true
	case OpenLink:
		return s.openLink(e.Link), true
	case LinksResolved:
		s.linksResolved(e)

		return nil, true
	case FileOpened:
		if e.Err != nil {
			s.notice(session.LevelWarning, "file link: "+e.Err.Error())
		}

		return nil, true
	}

	return s.onPeek(ev)
}

// onPointer gives file links, then the selection and shell mode, the first
// look at every event.
func (s *State) onPointer(ev any) ([]Effect, bool) {
	if effects, ok := s.onLinks(ev); ok {
		return effects, true
	}

	return s.onSelectionOrShell(ev)
}

// openLink does what file_links says with a link.
func (s *State) openLink(l FileLink) []Effect {
	switch s.FileLinks {
	case LinksPeek:
		s.Peek = &Peek{Link: l, Loading: true}

		return []Effect{EffPeekFile{Link: l}}
	case LinksEditor:
		return []Effect{EffEditFile{Link: l}}
	case LinksOpen:
		return []Effect{EffOpenFile{Link: l}}
	}

	return nil
}

// noteLinks queues a finished message of the agent's whose words were not
// looked at yet; Reduce asks for them after the event (resolveLinks).
func (s *State) noteLinks(it *Item) {
	if !s.linksOn() || it.Kind != KindAssistant || it.Streaming || it.Text == "" || it.linked == it.Text {
		return
	}
	if !slices.Contains(s.unlinked, it.Key) {
		s.unlinked = append(s.unlinked, it.Key)
	}
}

// relinkAll queues every finished message of the agent's, as when links
// are turned on.
func (s *State) relinkAll() {
	for i := range s.Items {
		s.noteLinks(&s.Items[i])
	}
}

// maxLinkWords is how many words of one message are looked up.
const maxLinkWords = 64

// resolveLinks asks for the queued messages' words; nil when none has a
// word that may name a file, or before the workspace is known, which keeps
// them queued.
func (s *State) resolveLinks() []Effect {
	workspace := cmp.Or(s.Settings.Workspace, s.loadedWorkspace)
	if workspace == "" {
		return nil
	}
	keys := s.unlinked
	s.unlinked = nil
	var msgs []LinkWords
	for _, key := range keys {
		i, ok := s.index[key]
		if !ok {
			continue
		}
		it := &s.Items[i]
		it.linked = it.Text // asked once per text
		if words := linkWords(it.Text); len(words) > 0 {
			msgs = append(msgs, LinkWords{Key: key, Text: it.Text, Words: words})
		}
	}
	if len(msgs) == 0 {
		return nil
	}

	return []Effect{EffResolveLinks{Workspace: workspace, Home: s.Home, Messages: msgs}}
}

// linksResolved puts each message's links on it, when its text is still
// the one they were found in.
func (s *State) linksResolved(e LinksResolved) {
	for _, m := range e.Messages {
		i, ok := s.index[m.Key]
		if !ok || s.Items[i].Text != m.Text || len(m.Links) == 0 {
			continue
		}
		s.Items[i].Links = m.Links
		s.Items[i].Version++
	}
}

// linkWords are the distinct words of a Markdown text that may name a file
// (LinkWord), outside fenced code blocks, at most maxLinkWords.
func linkWords(text string) []string {
	var out []string
	fence := ""
	for line := range strings.SplitSeq(text, "\n") {
		trimmed := strings.TrimLeft(line, " ")
		if f := fenceOf(trimmed); f != "" && len(line)-len(trimmed) < 4 {
			switch {
			case fence == "":
				fence = f
			case strings.HasPrefix(trimmed, fence) && strings.TrimSpace(strings.TrimLeft(trimmed, fence[:1])) == "":
				fence = ""
			}

			continue
		}
		if fence != "" {
			continue
		}
		for w := range strings.FieldsSeq(line) {
			if word, ok := LinkWord(w); ok && !slices.Contains(out, word) {
				out = append(out, word)
				if len(out) == maxLinkWords {
					return out
				}
			}
		}
	}

	return out
}

// fenceOf is the fence a line opens or closes (``` or ~~~, or longer), or
// "".
func fenceOf(line string) string {
	for _, c := range []string{"`", "~"} {
		n := len(line) - len(strings.TrimLeft(line, c))
		if n >= 3 {
			return line[:n]
		}
	}

	return ""
}

// linkTrim are the marks around a word that are not part of a path: code
// spans, emphasis, quotes, brackets, and punctuation.
const linkTrim = "`*_\"'()[]{}<>,;!?"

// lineSuffix is a line or a range after a path: ":12", ":12-20",
// ":12:5" (a column), "#L12", or "#L12-L20".
var lineSuffix = regexp.MustCompile(`(?::(\d+)(?:-(\d+)|:\d+)?|#L(\d+)(?:-L?(\d+))?)$`)

// extension is a file name's extension, as a path names a file by.
var extension = regexp.MustCompile(`^\.[A-Za-z0-9_+-]{1,10}$`)

// LinkWord is the part of a word of text that may name a file: without the
// marks and punctuation around it, with a slash or a file extension, and
// no URL, flag, or control character. ok is false for any other word. The
// renderer finds a message's links in its drawn lines with it, so a word
// is looked up the same way it was resolved.
func LinkWord(word string) (string, bool) {
	w := strings.Trim(word, linkTrim)
	w = strings.TrimRight(w, ".:")
	w = strings.Trim(w, linkTrim)
	if len(w) < 3 || len(w) > 512 || strings.Contains(w, "://") || strings.HasPrefix(w, "-") {
		return "", false
	}
	if strings.ContainsFunc(w, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return "", false
	}
	p, _, _ := SplitLines(w)
	base := path.Base(p)
	if strings.Trim(p, "./~") == "" || (!strings.Contains(p, "/") && (!extension.MatchString(path.Ext(base)) || path.Ext(base) == base)) {
		return "", false
	}

	return w, true
}

// SplitLines splits a word into its path and the line or lines after it
// (":12", ":12-20", "#L12-L20"; 0 when none).
func SplitLines(word string) (p string, line, end int) {
	m := lineSuffix.FindStringSubmatchIndex(word)
	if m == nil || m[0] == 0 {
		return word, 0, 0
	}
	num := func(i int) int {
		if m[2*i] < 0 {
			return 0
		}
		n, _ := strconv.Atoi(word[m[2*i]:m[2*i+1]])

		return n
	}
	line, end = num(1), num(2)
	if line == 0 {
		line, end = num(3), num(4)
	}
	if end < line {
		end = 0
	}

	return word[:m[0]], line, end
}
