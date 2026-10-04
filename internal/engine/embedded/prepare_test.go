package embedded_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/approval"
	"github.com/viktordanov/uah/internal/contextprep"
	"github.com/viktordanov/uah/internal/engine/embedded"
	"github.com/viktordanov/uah/internal/instructions"
	"github.com/viktordanov/uah/internal/sandbox"
	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/testing/fakellm"
)

// TestContextPreparation: with context preparation on, a new session's
// first request carries the prepared context, a developer message, before
// the user's message,
// with git's state, the tracked files, the instruction files, and the
// harness's guidance; the system prompt is the same as
// without it, and a later run of the session adds no second block. A
// subagent's session gets one too; off, nothing is added.
func TestContextPreparation(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	systems := map[string]string{}
	for _, tt := range []struct {
		name     string
		on       bool
		subagent bool
	}{{"off", false, false}, {"on", true, false}, {"subagent", true, true}} {
		e := newEnv(t, fakellm.Reply{Text: "one"}, fakellm.Reply{Text: "two"})
		agents := filepath.Join(e.Workspace, "AGENTS.md")
		require.NoError(t, os.WriteFile(agents, []byte("Rules.\n"), 0o644))
		require.NoError(t, os.MkdirAll(filepath.Join(e.Workspace, "pkg"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(e.Workspace, "pkg", "a.go"), []byte("package pkg\n"), 0o644))
		for _, args := range [][]string{{"init", "-q", "-b", "trunk"}, {"add", "pkg", "AGENTS.md"}} {
			out, err := exec.Command("git", append([]string{"-C", e.Workspace}, args...)...).CombinedOutput()
			require.NoError(t, err, string(out))
		}
		settings := e.settings()
		settings.SystemPrompt = instructions.HostPrompt("Base.", "## "+agents+"\n\nRules.", "")
		eng := embedded.New(embedded.Config{
			StateDir: e.StateDir, Provider: "openai", Getenv: e.getenv, ContextPreparation: tt.on, InstructionFiles: []string{agents},
		})
		opts := session.Options{Settings: settings}
		if tt.subagent {
			opts.ID = session.NewSubagentID()
		}
		s, err := session.Open(t.Context(), eng, opts)
		require.NoError(t, err)
		t.Cleanup(func() { _ = s.Close() })
		ev := &events{t: t, s: s}
		_, err = s.Submit("hello")
		require.NoError(t, err)
		ev.finished()
		ev.idle()
		_, err = s.Submit("again")
		require.NoError(t, err)
		ev.finished()

		reqs := e.llm.Requests()
		require.Len(t, reqs, 2, tt.name)
		systems[tt.name] = strings.ReplaceAll(reqs[0].System, e.Workspace, "<ws>")
		assert.Equal(t, []string{"hello"}, reqs[0].UserTexts, "the prepared context is not a user message")
		if !tt.on {
			assert.Empty(t, reqs[0].DeveloperTexts)

			continue
		}
		require.Len(t, reqs[0].DeveloperTexts, 1, tt.name)
		prepared := reqs[0].DeveloperTexts[0]
		assert.True(t, contextprep.IsPrepared(prepared), prepared)
		assert.Contains(t, prepared, "## workspace\nGit branch: trunk")
		assert.Contains(t, prepared, "A  AGENTS.md")
		assert.Contains(t, prepared, "pkg/ (1 files)")
		assert.Contains(t, prepared, "## agent files\nInstruction files in the system prompt, in order (their @ lines are expanded in place):\n- "+agents+"\n")
		assert.Contains(t, prepared, "## harness\n")
		assert.Contains(t, prepared, "(default 40000)")
		assert.Equal(t, []string{prepared}, reqs[1].DeveloperTexts, "the second run adds no context")
		assert.Equal(t, []string{"hello", "again"}, reqs[1].UserTexts)
	}
	assert.Equal(t, systems["off"], systems["on"], "the system prompt is unchanged")
}

// TestContextPreparation_Sandbox: a session in a read-only sandbox is told
// so, with its private $TMPDIR, and the shell and OS it runs in.
func TestContextPreparation_Sandbox(t *testing.T) {
	e := newEnv(t, fakellm.Reply{Text: "one"})
	policy := sandbox.Policy{Mode: sandbox.WorkspaceWrite, Workspace: e.Workspace}
	if _, err := policy.Wrap([]string{"/bin/sh"}); err != nil {
		t.Skipf("no sandbox here: %v", err)
	}
	eng := embedded.New(embedded.Config{
		StateDir: e.StateDir, Provider: "openai", Getenv: e.getenv, ContextPreparation: true,
		Sandbox: &policy, SandboxDir: filepath.Join(e.StateDir, "sandbox"),
	})
	s, err := session.Open(t.Context(), eng, session.Options{Settings: e.settings().WithMode(approval.ModeReadOnly)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	ev := &events{t: t, s: s}
	_, err = s.Submit("hello")
	require.NoError(t, err)
	ev.finished()

	prepared := e.llm.Requests()[0].DeveloperTexts[0]
	require.True(t, contextprep.IsPrepared(prepared), prepared)
	assert.Contains(t, prepared, "## environment\nCommands run in ")
	assert.Contains(t, prepared, "## sandbox\nSandbox: read-only. Commands can read any file and write only $TMPDIR.")
	assert.Contains(t, prepared, "$TMPDIR ("+session.TempDir(filepath.Join(e.StateDir, "sessions"), s.ID())+")")
}

// TestContextPreparation_ForkNoteSurvivesANewEngine forks a session whose
// prepared context names its $TMPDIR, then runs the fork on a new engine,
// as after a first run that failed before recording its messages, a
// close, and a resume in another process: the fork's run still starts
// with the note naming its own $TMPDIR, after the copied items, and a
// later run does not repeat it.
func TestContextPreparation_ForkNoteSurvivesANewEngine(t *testing.T) {
	e := newEnv(t,
		fakellm.Reply{Commands: []string{"echo parent"}}, fakellm.Reply{Text: "parent done"},
		fakellm.Reply{Text: "fork one"}, fakellm.Reply{Text: "fork two"},
	)
	policy := sandbox.Policy{Mode: sandbox.WorkspaceWrite, Workspace: e.Workspace}
	if _, err := policy.Wrap([]string{"/bin/sh"}); err != nil {
		t.Skipf("no sandbox here: %v", err)
	}
	cfg := embedded.Config{
		StateDir: e.StateDir, Provider: "openai", Getenv: e.getenv, ContextPreparation: true,
		Sandbox: &policy, SandboxDir: filepath.Join(e.StateDir, "sandbox"),
	}
	sessions := filepath.Join(e.StateDir, "sessions")
	parent, err := session.Open(t.Context(), embedded.New(cfg), session.Options{Settings: e.settings().WithMode(approval.ModeReadOnly)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = parent.Close() })
	_, err = parent.Submit("hello")
	require.NoError(t, err)
	(&events{t: t, s: parent}).finished()
	call := e.llm.Requests()[1].CallIDs[0]
	require.NoError(t, embedded.New(cfg).Fork(t.Context(), parent.ID(), "fork-1", call))

	fork, err := session.Open(t.Context(), embedded.New(cfg), session.Options{ID: "fork-1", Resumed: true, Settings: e.settings().WithMode(approval.ModeReadOnly)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = fork.Close() })
	ev := &events{t: t, s: fork}
	_, err = fork.Submit("task")
	require.NoError(t, err)
	ev.finished()
	_, err = fork.Submit("again")
	require.NoError(t, err)
	ev.finished()

	reqs := e.llm.Requests()
	first, second := reqs[2], reqs[3]
	parentTemp, forkTemp := session.TempDir(sessions, parent.ID()), session.TempDir(sessions, "fork-1")
	require.Len(t, first.DeveloperTexts, 2, "the copied prepared context, then the note")
	assert.Contains(t, first.DeveloperTexts[0], parentTemp)
	assert.Contains(t, first.DeveloperTexts[1], "$TMPDIR ("+forkTemp+") is your private scratch directory")
	assert.Equal(t, first.DeveloperTexts, second.DeveloperTexts, "the note once, in the history")
	assert.NoFileExists(t, filepath.Join(sessions, "fork-1.forktmp"))
}
