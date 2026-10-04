package bubble_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/viktordanov/uah/internal/engine/embedded"
	"github.com/viktordanov/uah/internal/sandbox"
	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/internal/tui/bubble"
	"github.com/viktordanov/uah/internal/tui/term"
	"github.com/viktordanov/uah/testing/fakellm"
	"github.com/viktordanov/uah/testing/harnesstest"
)

// approvalDeps opens interactive sessions on the embedded engine in the
// workspace-write sandbox, with a model that asks to create target outside
// it.
func approvalDeps(t *testing.T) (bubble.Deps, string) {
	t.Helper()
	env := harnesstest.NewEnv(t)
	policy := sandbox.Policy{Mode: sandbox.WorkspaceWrite, Workspace: env.Workspace}
	if _, err := policy.Wrap([]string{"/bin/sh"}); err != nil {
		t.Skipf("no sandbox here: %v", err)
	}
	outside := harnesstest.OutsideDir(t, "uah-tui-approval-")
	target := filepath.Join(outside, "x.txt")
	llm := fakellm.New(t, fakellm.Reply{Escalated: []string{"touch " + target}}, fakellm.Reply{Text: "done"})
	getenv := func(key string) string {
		switch key {
		case "OPENAI_API_KEY":
			return "test-key"
		case "SHELL":
			return "/bin/sh"
		}

		return env.Getenv(key)
	}
	eng := embedded.New(embedded.Config{
		StateDir: env.StateDir, Provider: "openai", Getenv: getenv,
		Sandbox: &policy, SandboxDir: filepath.Join(env.StateDir, "sandbox"),
	})
	settings := session.Settings{Provider: "openai", Model: "gpt-test", Effort: "high", Workspace: env.Workspace, BaseURL: llm.URL}

	return bubble.Deps{
		Open: func(ctx context.Context, id string) (*session.Session, []session.LoadedRun, error) {
			s, err := session.Open(ctx, eng, session.Options{ID: id, Settings: settings, Interactive: true})

			return s, nil, err
		},
		Sessions: func() ([]session.Info, error) { return nil, nil },
	}, target
}

func TestTUI_ApproveAnEscalation(t *testing.T) {
	deps, target := approvalDeps(t)
	d := start(t, deps)
	d.typeText("make the file")
	d.key(term.KeyEnter, 0)

	d.waitFor("Run outside the sandbox?")
	assert.Contains(t, d.view(), "Reason: it needs the network")
	assert.Contains(t, d.view(), "$ touch "+target)
	d.typeText("x") // other keys wait
	assert.Contains(t, d.view(), "Run outside the sandbox?")
	d.typeText("y")

	d.waitFor("✔ approved: touch " + target)
	d.waitFor("done")
	assert.NotContains(t, d.view(), "Run outside the sandbox?")
	assert.FileExists(t, target)

	d.typeText("/quit")
	d.key(term.KeyEnter, 0)
	d.waitQuit()
}

func TestTUI_DeclineAnEscalation(t *testing.T) {
	deps, target := approvalDeps(t)
	d := start(t, deps)
	d.typeText("make the file")
	d.key(term.KeyEnter, 0)

	d.waitFor("Run outside the sandbox?")
	d.key(term.KeyEscape, 0)

	d.waitFor("✗ declined: touch " + target)
	d.waitFor("done")
	assert.NoFileExists(t, target)
}
