package bubble_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	uaharness "github.com/viktordanov/uagent/harness"
	"github.com/viktordanov/uagent/testing/fixtures"

	"github.com/viktordanov/uah/internal/sandbox"
	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/internal/tui/bubble"
	"github.com/viktordanov/uah/internal/tui/term"
	"github.com/viktordanov/uah/internal/usershell"
	"github.com/viktordanov/uah/testing/harnesstest"
)

// shellDeps opens sessions on uagent's fake runner, as deps does, and with
// a runner for the user's commands.
func shellDeps(t *testing.T) bubble.Deps {
	t.Helper()
	env := harnesstest.NewEnv(t)
	t.Setenv("FAKERUNNER_FIXTURE", fixtures.Path("simple.jsonl"))
	t.Setenv("FAKERUNNER_ECHO", "1")
	eng := harnesstest.RunnerEngine(uaharness.Config{
		RunnerPath: harnesstest.FakeRunner(t), StateDir: env.StateDir, KillGrace: 300 * time.Millisecond, Getenv: env.Getenv,
	})
	opts := session.Options{
		Settings: session.Settings{Provider: "openai-codex", Model: "gpt-6-sol", Effort: "high", Workspace: env.Workspace},
		Shell:    &usershell.Runner{Dir: t.TempDir(), Policy: sandbox.Policy{Workspace: env.Workspace}, Shell: "/bin/sh"},
	}

	return bubble.Deps{
		Open: func(ctx context.Context, _ string) (*session.Session, []session.LoadedRun, error) {
			s, err := session.Open(ctx, eng, opts)

			return s, nil, err
		},
		Sessions: func() ([]session.Info, error) { return session.Sessions(env.StateDir) },
	}
}

// TestTUI_ShellMode types ! into the empty composer, runs a command in a
// real session on the fake runner, and sends the next message with its
// record: the fake runner echoes it, and the transcript shows it once.
func TestTUI_ShellMode(t *testing.T) {
	d := start(t, shellDeps(t))
	d.until("the session is open", func() bool { return d.m.(bubble.Model).Exit().SessionID != "" })

	d.typeText("!")
	assert.Contains(t, d.view(), "! Run a command in the workspace", "the ! replaces the λ, and the placeholder says so")
	assert.Contains(t, d.view(), "! shell mode · enter runs the command · esc leaves")
	d.key(term.KeyBackspace, 0)
	assert.Contains(t, d.view(), "λ Ask uah to do anything", "backspace on the empty composer leaves shell mode")

	d.typeText("!echo from the shell; exit 3")
	d.key(term.KeyEnter, 0)
	d.waitFor("! echo from the shell; exit 3  ✗ exit 3")
	assert.Contains(t, d.view(), "from the shell")
	assert.Contains(t, d.view(), "the agent sees this with your next message")
	assert.Contains(t, d.view(), "λ Ask uah to do anything", "back to messages after a command")

	d.typeText("what failed?")
	d.key(term.KeyEnter, 0)
	d.waitFor("• hello")
	assert.NotContains(t, d.view(), "the agent sees this with your next message", "the runner echoed the record")
	assert.NotContains(t, d.view(), "<user_shell_command>", "the record shows as the command, not its tags")
}
