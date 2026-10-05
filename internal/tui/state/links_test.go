package state_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/internal/tui/state"
)

var peekGo = state.FileLink{Path: "/w/internal/tui/state/peek.go", Line: 30, End: 32}

// linked is an open session with an answer and file links set to mode.
func linked(mode string) state.State {
	s := opened()
	s.FileLinks = mode
	s, _ = apply(s, core.AssistantMessage{At: t0, Text: "See `peek.go`.", Final: true})

	return s
}

// click presses and lets go on one cell of the answer, over link, and
// lets the double click's window pass: the effects are the timer's.
func click(s state.State, at time.Time, link *state.FileLink) (state.State, []state.Effect) {
	s, effects := clickNow(s, at, link)
	for _, e := range effects {
		if timer, ok := e.(state.EffLinkTimer); ok {
			return apply(s, state.LinkTimer{Seq: timer.Seq})
		}
	}

	return s, effects
}

// clickNow presses and lets go without waiting.
func clickNow(s state.State, at time.Time, link *state.FileLink) (state.State, []state.Effect) {
	key := s.Items[len(s.Items)-1].Key
	pos := state.TextPos{Key: key, Line: 0, Col: 8}

	return apply(s, state.MousePress{At: pos, Text: "• See peek.go.", When: at, Link: link}, state.MouseRelease{})
}

// TestLinks_ClickDoesWhatFileLinksSays: a click on a link peeks, edits, or
// opens, as [tui] file_links says; off does nothing, and a click off a
// link selects nothing and opens nothing.
func TestLinks_ClickDoesWhatFileLinksSays(t *testing.T) {
	s, effects := click(linked(state.LinksPeek), t0, &peekGo)
	assert.Equal(t, []state.Effect{state.EffPeekFile{Link: peekGo}}, effects)
	require.NotNil(t, s.Peek)
	assert.True(t, s.Peek.Loading)
	assert.Nil(t, s.Selection, "a click selects nothing")

	_, effects = click(linked(state.LinksEditor), t0, &peekGo)
	assert.Equal(t, []state.Effect{state.EffEditFile{Link: peekGo}}, effects)
	_, effects = click(linked(state.LinksOpen), t0, &peekGo)
	assert.Equal(t, []state.Effect{state.EffOpenFile{Link: peekGo}}, effects)
	s, effects = click(linked(state.LinksOff), t0, &peekGo)
	assert.Empty(t, effects)
	assert.Nil(t, s.Peek)

	s, effects = click(linked(state.LinksPeek), t0, nil)
	assert.Empty(t, effects, "no link under the click")
	assert.Nil(t, s.Peek)
}

// TestLinks_DragSelectsInstead: a press on a link and a drag off it
// selects and copies, and opens nothing; a double click on a link selects
// its word and opens nothing.
func TestLinks_DragSelectsInstead(t *testing.T) {
	s := linked(state.LinksEditor)
	key := s.Items[len(s.Items)-1].Key
	s, effects := apply(s,
		state.MousePress{At: state.TextPos{Key: key, Col: 8}, Text: "• See peek.go.", When: t0, Link: &peekGo},
		state.MouseDrag{At: state.TextPos{Key: key, Col: 12}},
		state.MouseRelease{},
	)
	assert.Equal(t, []state.Effect{state.EffCopySelection{}}, effects)
	assert.NotNil(t, s.Selection)

	s = linked(state.LinksEditor)
	s, effects = clickNow(s, t0, &peekGo)
	require.Len(t, effects, 1)
	timer, ok := effects[0].(state.EffLinkTimer)
	require.True(t, ok, "the first click waits for a second")
	assert.Equal(t, 500*time.Millisecond, timer.After)
	s, effects = clickNow(s, t0.Add(100*time.Millisecond), &peekGo)
	assert.Equal(t, []state.Effect{state.EffCopySelection{}}, effects, "the second selects the word")
	s, effects = apply(s, state.LinkTimer{Seq: timer.Seq})
	assert.Empty(t, effects, "and the link does not open")
	start, end, ok := s.SelectedRange()
	require.True(t, ok)
	assert.Equal(t, [2]int{6, 13}, [2]int{start.Col, end.Col})
}

// TestLinks_MessageWordsLookedUp: a finished answer's words that may name
// files are asked about once per text; a streamed one when it ends; the
// answer comes back on the message, unless its text changed since.
func TestLinks_MessageWordsLookedUp(t *testing.T) {
	s := opened()
	s.FileLinks = state.LinksPeek
	s, effects := apply(s, core.AssistantMessage{At: t0, Text: "Edited `a.go:12`, docs/x.md and ~/notes.txt; see https://x.io/a.go or -f.go.", Final: true})
	require.Len(t, effects, 1)
	ask, ok := effects[0].(state.EffResolveLinks)
	require.True(t, ok)
	assert.Equal(t, settings().Workspace, ask.Workspace)
	require.Len(t, ask.Messages, 1)
	m := ask.Messages[0]
	assert.Equal(t, []string{"a.go:12", "docs/x.md", "~/notes.txt"}, m.Words)

	_, again := apply(s, state.Tick{Now: t0})
	assert.Empty(t, again, "asked once")

	s, _ = apply(s, state.LinksResolved{Messages: []state.ResolvedLinks{{Key: m.Key, Text: m.Text, Links: map[string]state.FileLink{"a.go:12": {Path: "/w/a.go", Line: 12}}}}})
	it := s.Items[len(s.Items)-1]
	assert.Equal(t, map[string]state.FileLink{"a.go:12": {Path: "/w/a.go", Line: 12}}, it.Links)

	s, _ = apply(s, state.LinksResolved{Messages: []state.ResolvedLinks{{Key: m.Key, Text: "older text", Links: map[string]state.FileLink{"b.go": {Path: "/w/b.go"}}}}})
	assert.NotContains(t, s.Items[len(s.Items)-1].Links, "b.go", "for another text: dropped")

	early := state.New(t0)
	early.FileLinks, early.SessionID = state.LinksPeek, "sess-1" // a resumed session's history, loaded before it opens
	early, effects = apply(early, core.AssistantMessage{At: t0, Text: "See a.go.", Final: true})
	assert.Empty(t, effects, "no workspace yet: kept")
	_, effects = apply(early, session.SessionOpened{At: t0, ID: "sess-1", Engine: "embedded", Settings: settings()})
	require.Len(t, effects, 1, "asked once the session opens")
	assert.Equal(t, []string{"a.go"}, effects[0].(state.EffResolveLinks).Messages[0].Words)

	off := opened()
	off.FileLinks = state.LinksOff
	_, effects = apply(off, core.AssistantMessage{At: t0, Text: "See a.go.", Final: true})
	assert.Empty(t, effects, "links off: nothing looked up")
}

func TestLinks_LinkWord(t *testing.T) {
	for in, want := range map[string]string{
		"`internal/a.go`":   "internal/a.go",
		"a.go,":             "a.go",
		"(see:":             "",
		"**docs/x.md**":     "docs/x.md",
		"README.md.":        "README.md",
		"a.go:12.":          "a.go:12",
		"./run.sh":          "./run.sh",
		"~/notes.txt":       "~/notes.txt",
		"/etc/hosts":        "/etc/hosts",
		"and/or":            "and/or",
		"word":              "",
		"...":               "",
		"https://x.io/a.go": "",
		"--flag=x.go":       "",
		".gitignore":        "",
		"Makefile":          "",
		"v1.9.4":            "v1.9.4",
		"a\x1b.go":          "",
	} {
		got, ok := state.LinkWord(in)
		assert.Equal(t, want, got, "%q", in)
		assert.Equal(t, want != "", ok, "%q", in)
	}
}

func TestLinks_SplitLines(t *testing.T) {
	for in, want := range map[string]struct {
		path      string
		line, end int
	}{
		"a.go":          {"a.go", 0, 0},
		"a.go:12":       {"a.go", 12, 0},
		"a.go:12-20":    {"a.go", 12, 20},
		"a.go:12:5":     {"a.go", 12, 0},
		"a.go#L3":       {"a.go", 3, 0},
		"a.go#L3-L9":    {"a.go", 3, 9},
		"a.go:20-12":    {"a.go", 20, 0},
		"dir:x/a.go:7":  {"dir:x/a.go", 7, 0},
		"C:12":          {"C", 12, 0},
		":12":           {":12", 0, 0},
		"a.go:notaline": {"a.go:notaline", 0, 0},
	} {
		p, line, end := state.SplitLines(in)
		assert.Equal(t, want.path, p, in)
		assert.Equal(t, [2]int{want.line, want.end}, [2]int{line, end}, in)
	}
}

// TestPeek_OpenScrollClose: the overlay starts with the link's line near
// its top third, scrolls within the text, and closes; e and o close it and
// open the file in the editor or with the system.
func TestPeek_OpenScrollClose(t *testing.T) {
	lines := make([]string, 100)
	s, _ := click(linked(state.LinksPeek), t0, &peekGo)
	s, _ = apply(s, state.PeekLoaded{Link: peekGo, Lines: lines, Rows: 30})
	require.NotNil(t, s.Peek)
	assert.False(t, s.Peek.Loading)
	assert.Equal(t, 19, s.Peek.Top, "line 30 is the 11th of 30 rows")

	s, _ = apply(s, state.PeekLoaded{Link: state.FileLink{Path: "/other"}, Lines: nil})
	assert.Len(t, s.Peek.Lines, 100, "a read for another link is dropped")

	s, _ = apply(s, state.PeekScroll{Delta: -100, Rows: 30})
	assert.Zero(t, s.Peek.Top)
	s, _ = apply(s, state.PeekScroll{Delta: 1000, Rows: 30})
	assert.Equal(t, 70, s.Peek.Top, "the last line at the bottom")
	s, _ = apply(s, state.PeekClose{})
	assert.Nil(t, s.Peek)

	s, _ = click(linked(state.LinksPeek), t0, &peekGo)
	s, effects := apply(s, state.PeekEdit{})
	assert.Nil(t, s.Peek)
	assert.Equal(t, []state.Effect{state.EffEditFile{Link: peekGo}}, effects)
	s, _ = click(linked(state.LinksPeek), t0, &peekGo)
	s, effects = apply(s, state.PeekOpen{})
	assert.Nil(t, s.Peek)
	assert.Equal(t, []state.Effect{state.EffOpenFile{Link: peekGo}}, effects)

	short := state.FileLink{Path: "/w/short.go", Line: 3}
	s, _ = apply(linked(state.LinksPeek), state.OpenLink{Link: short}, state.PeekLoaded{Link: short, Lines: make([]string, 5), Rows: 30})
	assert.Zero(t, s.Peek.Top, "a short file from its top")

	s, _ = apply(s, state.FileOpened{Link: short, Err: assert.AnError})
	require.NotNil(t, s.Toast)
	assert.Equal(t, "can't open short.go: "+assert.AnError.Error(), s.Toast.Text, "a toast, not a notice")
	s, _ = apply(s, state.FileOpened{Link: short, With: "code"})
	assert.Equal(t, "opened short.go in code", s.Toast.Text)
}
