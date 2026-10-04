package bubble_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/history"
	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/internal/tui/bubble"
	"github.com/viktordanov/uah/internal/tui/term"
	"github.com/viktordanov/uah/testing/fakellm"
)

// withHistory gives deps a history file holding prompts of the sessions'
// workspace, in a temporary directory.
func withHistory(t *testing.T, d bubble.Deps, prompts ...string) (bubble.Deps, history.File) {
	t.Helper()
	file, err := history.New(t.TempDir(), "", nil)
	require.NoError(t, err)
	workspace := workspaceOf(t, d)
	for _, p := range prompts {
		require.NoError(t, file.Append(history.Entry{SessionID: "earlier", TS: 1, Text: p, Workspace: workspace}))
	}
	d.History = &file

	return d, file
}

// workspaceOf is the workspace deps open a new session in, from the
// session's first event.
func workspaceOf(t *testing.T, d bubble.Deps) string {
	t.Helper()
	s, _, err := d.Open(t.Context(), "")
	require.NoError(t, err)
	defer s.Close()
	opened, ok := (<-s.Events()).(session.SessionOpened)
	require.True(t, ok)

	return opened.Settings.Workspace
}

// footer is the screen's last line.
func (d *driver) footer() string {
	lines := strings.Split(d.view(), "\n")

	return lines[len(lines)-1]
}

// TestTUI_PromptHistory drives ↑ and ↓ over the history file and this
// process's prompts, the file's appends, an edited recall, a draft ctrl+c
// cleared, and ctrl+r's search, as a user would.
func TestTUI_PromptHistory(t *testing.T) {
	deps, file := withHistory(t, deps(t, "simple.jsonl"), "older prompt", "newest prompt")
	d := start(t, deps)
	d.until("the session is open and the history read", func() bool {
		m := d.m.(bubble.Model)

		return m.Exit().SessionID != "" && m.Prompts() == 2
	})

	d.key(term.KeyUp, 0)
	assert.Equal(t, "newest prompt", d.draft())
	assert.Contains(t, d.view(), "λ newest prompt")
	d.key(term.KeyUp, 0)
	assert.Equal(t, "older prompt", d.draft())
	d.key(term.KeyUp, 0)
	assert.Equal(t, "older prompt", d.draft(), "the oldest stays")
	d.key(term.KeyDown, 0)
	assert.Equal(t, "newest prompt", d.draft())
	d.key(term.KeyDown, 0)
	assert.Empty(t, d.draft(), "past the newest: the empty composer again")

	d.typeText("hi there")
	d.key(term.KeyEnter, 0)
	d.waitFor("• hello")
	d.waitIdle() // ctrl+c quits at the end only while nothing runs
	d.until("the prompt in the file", func() bool {
		entries, err := file.Load()

		return err == nil && len(entries) == 3 && entries[2].Text == "hi there" && entries[2].SessionID == d.m.(bubble.Model).Exit().SessionID
	})

	d.key(term.KeyUp, 0)
	assert.Equal(t, "hi there", d.draft())
	d.typeText("!")
	d.key(term.KeyUp, 0)
	assert.Equal(t, "hi there!", d.draft(), "an edited prompt is yours: ↑ no longer replaces it")

	d.key('c', term.ModCtrl)
	assert.Empty(t, d.draft())
	d.key(term.KeyUp, 0)
	assert.Equal(t, "hi there!", d.draft(), "ctrl+c's draft comes back, as in Codex")
	d.key('c', term.ModCtrl)

	d.key('r', term.ModCtrl)
	assert.Contains(t, d.footer(), "reverse-i-search:")
	assert.Empty(t, d.draft(), "opening the search previews nothing")
	d.typeText("OLD")
	assert.Equal(t, "older prompt", d.draft(), "the newest match, ignoring case")
	assert.Contains(t, d.footer(), "reverse-i-search: OLD  enter accept · esc cancel")
	v := d.m.View()
	require.NotNil(t, v.Cursor)
	assert.Contains(t, strings.Split(d.view(), "\n")[v.Cursor.Y], "reverse-i-search: OLD", "the cursor is in the query")
	d.key(term.KeyEnter, 0)
	assert.Equal(t, "older prompt", d.draft())
	assert.NotContains(t, d.footer(), "reverse-i-search")
	d.key(term.KeyDown, 0)
	assert.Equal(t, "newest prompt", d.draft(), "↓ goes on from the match")

	d.key('r', term.ModCtrl)
	d.typeText("zzz")
	assert.Contains(t, d.footer(), "no match")
	assert.Equal(t, "newest prompt", d.draft(), "no match shows the draft")
	d.key(term.KeyEscape, 0)
	assert.Equal(t, "newest prompt", d.draft(), "esc keeps the draft")
	assert.NotContains(t, d.footer(), "reverse-i-search")

	d.key('c', term.ModCtrl)
	d.key('c', term.ModCtrl)
	d.waitQuit()
	entries, err := file.Load()
	require.NoError(t, err)
	assert.Len(t, entries, 3, "ctrl+c's draft never reaches the file")
}

// TestTUI_HistoryLeavesBacktrackAlone: while an earlier message
// is selected, ↑ and ↓ move the selection, not the history; after it,
// ↑ recalls.
func TestTUI_HistoryLeavesBacktrackAlone(t *testing.T) {
	deps, _ := rewindDeps(t, fakellm.Reply{Text: "answer one"}, fakellm.Reply{Text: "answer two"})
	deps, _ = withHistory(t, deps, "from the file")
	d := start(t, deps)
	for _, text := range []string{"first", "second"} {
		d.typeText(text)
		d.key(term.KeyEnter, 0)
		d.waitFor("answer " + map[string]string{"first": "one", "second": "two"}[text])
		d.waitIdle()
	}

	d.key(term.KeyEscape, 0)
	d.key(term.KeyEscape, 0)
	d.waitFor("▶ second")
	d.key(term.KeyUp, 0)
	d.waitFor("▶ first")
	assert.Empty(t, d.draft(), "↑ moved the selection, not the history")
	d.key('c', term.ModCtrl)
	assert.NotContains(t, d.view(), "▶")

	d.key(term.KeyUp, 0)
	assert.Equal(t, "second", d.draft())
}

func TestComposerRows(t *testing.T) {
	for h, want := range map[int]int{10: 8, 16: 8, 24: 12, 30: 15, 40: 20, 100: 50} {
		assert.Equal(t, want, bubble.ComposerRows(h), "a terminal %d rows high", h)
	}
}

// TestTUI_ComposerGrowsWithTheWindow: a long draft fills up to half the
// window, again after a resize, and the transcript keeps the rest.
func TestTUI_ComposerGrowsWithTheWindow(t *testing.T) {
	d := start(t, deps(t, "simple.jsonl"))
	d.typeText("hi")
	d.key(term.KeyEnter, 0)
	d.waitFor("• hello")
	lines := make([]string, 60)
	for i := range lines {
		lines[i] = fmt.Sprintf("line %02d", i+1)
	}
	d.send(term.PasteMsg{Content: strings.Join(lines, "\n")})

	for _, size := range []struct{ h, rows int }{{30, 15}, {50, 25}, {20, 10}, {12, 8}} {
		d.send(term.WindowSizeMsg{Width: 100, Height: size.h})
		screen := d.view()
		assert.Len(t, strings.Split(screen, "\n"), size.h)
		assert.Equal(t, size.rows, strings.Count(screen, "line "), "%d rows of draft on a window %d high", size.rows, size.h)
		assert.Contains(t, screen, "line 60", "the cursor's line shows")
		if size.h >= 20 {
			assert.Contains(t, screen, "• hello", "the transcript keeps the rest")
		}
	}
}
