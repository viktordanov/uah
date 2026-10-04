package bubble_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	uaharness "github.com/viktordanov/uagent/harness"
	"github.com/viktordanov/uagent/testing/fixtures"

	"github.com/viktordanov/uah/internal/history"
	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/internal/tui/bubble"
	"github.com/viktordanov/uah/internal/tui/term"
	"github.com/viktordanov/uah/testing/harnesstest"
)

// folderDeps opens real sessions on the fake runner: a new one in the
// folder the returned func last named (first at the start), a resumed one
// in the folder it was opened in.
func folderDeps(t *testing.T, first string) (bubble.Deps, func(string)) {
	t.Helper()
	env := harnesstest.NewEnv(t)
	t.Setenv("FAKERUNNER_FIXTURE", fixtures.Path("simple.jsonl"))
	t.Setenv("FAKERUNNER_ECHO", "1")
	eng := harnesstest.RunnerEngine(uaharness.Config{
		RunnerPath: harnesstest.FakeRunner(t), StateDir: env.StateDir, KillGrace: 300 * time.Millisecond, Getenv: env.Getenv,
	})
	var mu sync.Mutex
	next, folders := first, map[string]string{}
	open := func(ctx context.Context, id string) (*session.Session, []session.LoadedRun, error) {
		mu.Lock()
		workspace := next
		if id != "" {
			workspace = folders[id]
		}
		mu.Unlock()
		settings := session.Settings{Provider: "openai-codex", Model: "gpt-6-sol", Effort: "high", Workspace: workspace}
		s, err := session.Open(ctx, eng, session.Options{ID: id, Resumed: id != "", Settings: settings})
		if err == nil {
			mu.Lock()
			folders[s.ID()] = workspace
			mu.Unlock()
		}

		return s, nil, err
	}
	moveTo := func(workspace string) {
		mu.Lock()
		next = workspace
		mu.Unlock()
	}

	return bubble.Deps{Open: open, Sessions: func() ([]session.Info, error) { return nil, nil }}, moveTo
}

// TestTUI_HistoryPerFolder: one history file holds two folders' prompts
// and a line without a folder; each session's ↑ and ctrl+r see its own
// folder's, a prompt is written with its folder, and /new in another
// folder and /resume back move the view with the session.
func TestTUI_HistoryPerFolder(t *testing.T) {
	here, there := t.TempDir(), t.TempDir()
	deps, moveTo := folderDeps(t, here)
	file, err := history.New(t.TempDir(), "", nil)
	require.NoError(t, err)
	require.NoError(t, file.Append(
		history.Entry{SessionID: "a", TS: 1, Text: "fix here", Workspace: here},
		history.Entry{SessionID: "b", TS: 2, Text: "fix there", Workspace: there},
		history.Entry{SessionID: "c", TS: 3, Text: "fix from codex"},
	))
	deps.History = &file
	d := start(t, deps)
	d.until("the session is open and the history read", func() bool {
		m := d.m.(bubble.Model)

		return m.Exit().SessionID != "" && m.Prompts() == 1
	})
	first := d.m.(bubble.Model).Exit().SessionID

	d.key(term.KeyUp, 0)
	assert.Equal(t, "fix here", d.draft())
	d.key(term.KeyUp, 0)
	assert.Equal(t, "fix here", d.draft(), "no other folder's prompt, and no line without a folder")
	d.key(term.KeyDown, 0)
	d.key('r', term.ModCtrl)
	d.typeText("fix")
	assert.Equal(t, "fix here", d.draft())
	d.key('r', term.ModCtrl)
	assert.Equal(t, "fix here", d.draft(), "the search sees this folder only")
	d.key(term.KeyEscape, 0)

	d.typeText("hi there")
	d.key(term.KeyEnter, 0)
	d.waitFor("• hello")
	d.waitIdle() // /new waits while a run is live
	d.until("the prompt in the file with its folder", func() bool {
		entries, err := file.Load()

		return err == nil && len(entries) == 4 && entries[3] == history.Entry{SessionID: first, TS: entries[3].TS, Text: "hi there", Workspace: here}
	})

	moveTo(there)
	d.typeText("/new")
	d.key(term.KeyEnter, 0)
	d.until("a session in the other folder", func() bool {
		m := d.m.(bubble.Model)

		return m.Exit().SessionID != first && m.Prompts() == 1
	})
	d.key(term.KeyUp, 0)
	assert.Equal(t, "fix there", d.draft(), "the other folder's history")
	d.key(term.KeyUp, 0)
	assert.Equal(t, "fix there", d.draft())
	d.key('c', term.ModCtrl)

	d.typeText("/resume " + first)
	d.key(term.KeyEnter, 0)
	d.until("the first session again", func() bool {
		m := d.m.(bubble.Model)

		return m.Exit().SessionID == first && m.Prompts() == 2
	})
	d.key(term.KeyUp, 0)
	assert.Equal(t, "hi there", d.draft(), "back in the first folder, its prompts, this process's too")
	d.key(term.KeyUp, 0)
	assert.Equal(t, "fix here", d.draft())
}
