package render_test

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uagent/core"
	uaharness "github.com/viktordanov/uagent/harness"
	"github.com/viktordanov/uagent/testing/fixtures"

	"github.com/viktordanov/uah/internal/compaction"
	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/internal/tui/render"
	"github.com/viktordanov/uah/internal/tui/state"
)

var update = flag.Bool("update", false, "rewrite golden files")

var t0 = time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)

// Finished runs show the local time; goldens are drawn in UTC.
func init() { time.Local = time.UTC }

func apply(s state.State, evs ...any) state.State {
	for _, ev := range evs {
		s, _ = state.Reduce(s, ev)
	}

	return s
}

func base() state.State {
	return apply(state.New(t0), session.SessionOpened{
		At: t0, ID: "3f2a1b2c-0000-4000-8000-000000000000", Engine: "embedded",
		Settings: session.Settings{Provider: "openai-codex", Model: "gpt-6-sol", Effort: "high", Workspace: "/workspace/proj", Sandbox: "workspace-write"},
	})
}

func fixtureEvents(t *testing.T, name string) []any {
	t.Helper()
	var out []any
	require.NoError(t, uaharness.ReadEvents(bytes.NewReader(fixtures.RunnerOutput(name)), func(e core.Event) { out = append(out, e) }))

	return out
}

func screen(s state.State, draft string) string {
	composer := "λ " + draft
	out, _ := render.Screen(s, render.NewCache(render.Amber), render.Frame{Width: 100, Height: 24, Composer: composer, ComposerHeight: 1, Draft: draft})
	lines := strings.Split(ansi.Strip(out), "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " ")
	}

	return strings.Join(lines, "\n") + "\n"
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		require.NoError(t, os.MkdirAll("testdata", 0o700))
		require.NoError(t, os.WriteFile(path, []byte(got), 0o600))
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "run go test ./internal/tui/render -update")
	assert.Equal(t, string(want), got)
}

func finishedRun(t *testing.T) state.State {
	t.Helper()
	id := "7cb42beb-329b-4c7b-8c2f-abced68ef095"
	s := apply(base(),
		session.InputQueued{At: t0, Input: core.UserInput{ID: id, Text: "Run `ls` and `cat a.txt`, then reply with the file contents."}},
		session.InputSent{At: t0, IDs: []string{id}},
		core.RunStarted{At: t0, RunID: "20260924-120000-3f2a1b2c"},
	)
	s = apply(s, fixtureEvents(t, "simple.jsonl")...)

	return apply(s,
		core.RunFinished{At: t0, Result: core.Result{
			Request: core.Request{RunID: "20260924-120000-3f2a1b2c"}, Status: core.StatusOK, Wall: 5200 * time.Millisecond,
			Stats: core.Stats{Turns: 2, ToolCalls: 2, MaxParallelTools: 2, ToolBusyTime: 100 * time.Millisecond, Tokens: core.Tokens{InputTokens: 1127, OutputTokens: 55}},
		}},
		session.Idle{At: t0},
	)
}

func TestScreens(t *testing.T) {
	t.Run("finished run", func(t *testing.T) {
		golden(t, "finished", screen(finishedRun(t), ""))
		golden(t, "finished-details", screen(apply(finishedRun(t), state.ToggleDetails{}), ""))
	})

	t.Run("live run with a queue", func(t *testing.T) {
		now := t0.Add(8 * time.Second)
		s := apply(base(),
			session.InputQueued{At: t0, Input: core.UserInput{ID: "a", Text: "Fix the failing test in pkg/foo"}},
			session.InputSent{At: t0, IDs: []string{"a"}},
			core.RunStarted{At: t0, RunID: "20260924-120000-3f2a1b2c"},
			core.UserMessage{At: t0, ID: "a"},
			core.TurnStarted{At: t0, Turn: 1},
			core.ModelResponded{At: t0.Add(2 * time.Second), Turn: 1, Duration: 2 * time.Second, Usage: core.Tokens{InputTokens: 12400, OutputTokens: 830}},
			core.ToolCalled{At: t0.Add(2 * time.Second), CallID: "c1", Name: "Bash", Label: "go test ./pkg/foo"},
			core.ToolStarted{At: t0.Add(2 * time.Second), CallID: "c1", OpID: "o1"},
			core.ToolFinished{At: t0.Add(4 * time.Second), CallID: "c1", OpID: "o1", OK: false, Detail: "exit 1", Duration: 2300 * time.Millisecond},
			core.ToolCalled{At: t0.Add(4 * time.Second), CallID: "c2", Name: "Bash", Label: "go test ./... -run Bar"},
			core.ToolStarted{At: t0.Add(4 * time.Second), CallID: "c2", OpID: "o2"},
			core.AssistantMessage{At: t0.Add(4 * time.Second), Text: "I'll patch the fixture while the suite runs."},
			core.TurnStarted{At: t0.Add(5 * time.Second), Turn: 2},
			session.InputQueued{At: t0, Input: core.UserInput{ID: "b", Text: "also update the README"}},
			session.InputQueued{At: t0, Input: core.UserInput{ID: "c", Text: "skip the vendored tests"}, AfterTool: true},
			state.Tick{Now: now},
		)
		golden(t, "live", screen(s, ""))
		golden(t, "live-details", screen(apply(s, state.ToggleDetails{}), ""))
	})

	t.Run("command completion", func(t *testing.T) {
		golden(t, "completion", screen(finishedRun(t), "/re"))
	})

	t.Run("context meter and compaction", func(t *testing.T) {
		s := apply(finishedRun(t), core.ModelResponded{At: t0, Turn: 3, Usage: core.Tokens{InputTokens: 180_000, OutputTokens: 2_000}})
		golden(t, "context-meter", screen(s, ""))
		s = apply(s,
			engine.CompactionStarted{At: t0, Trigger: compaction.TriggerAuto, Tokens: 182_000},
			engine.Compacted{At: t0, Trigger: compaction.TriggerAuto, Summary: "Listed the files and read a.txt."},
		)
		golden(t, "compacted-details", screen(apply(s, state.ToggleDetails{}), ""))
	})

	t.Run("session picker", func(t *testing.T) {
		infos := []session.Info{
			{ID: "3f2a1b2c-aaaa", FirstPrompt: "Fix the failing test in pkg/foo", Runs: 3, Status: core.StatusOK, Model: "gpt-6-sol", Workspace: "/workspace/proj", LastActivity: t0.Add(-12 * time.Minute)},
			{ID: "9b8c7d6e-bbbb", FirstPrompt: "Summarize this project", Runs: 1, Status: core.StatusInterrupted, Model: "gpt-6-sol", Workspace: "/workspace/other", LastActivity: t0.Add(-2 * time.Hour)},
		}
		s := apply(base(), state.SessionsLoaded{Sessions: infos, Local: infos[:1]})
		golden(t, "picker", screen(s, ""))
		golden(t, "picker-all", screen(apply(s, state.PickerToggleAll{}, state.PickerMove{Delta: 1}), ""))
	})
}

func TestTranscriptScrolls(t *testing.T) {
	s := base()
	for i := range 60 {
		s = apply(s, session.Notice{Level: "info", Message: "line " + strings.Repeat("x", i%3) + string(rune('A'+i%26))})
	}
	bottom := screen(s, "")
	scrolled := screen(apply(s, state.ScrollBy{Lines: 10}), "")
	top := screen(apply(s, state.ScrollBy{Lines: 1000}), "")

	assert.NotEqual(t, bottom, scrolled)
	assert.Contains(t, top, "line A", "scrolling past the top stops at the first line")
	assert.Equal(t, 24, strings.Count(bottom, "\n"), "the screen is exactly the terminal height")
}

// TestScreen_HomeFromState: the workspace is shown under the state's home
// directory, not the process's.
func TestScreen_HomeFromState(t *testing.T) {
	s := base()
	s.Home = "/workspace"
	assert.Contains(t, screen(s, ""), "~/proj")
	s.Home = "/work"
	assert.NotContains(t, screen(s, ""), "~")
	assert.Contains(t, screen(s, ""), "/workspace/proj")
}

// The session picker fits a narrow terminal: its empty state and its key
// line are cut to the width, since the terminal does not wrap.
func TestPickerFitsTheWidth(t *testing.T) {
	s := apply(base(), state.SessionsLoaded{})
	for _, line := range strings.Split(screenAt(s, "", 30), "\n") {
		assert.LessOrEqual(t, ansi.StringWidth(line), 30, "%q", ansi.Strip(line))
	}
}
