package bubble_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	uaharness "github.com/viktordanov/uagent/harness"
	"github.com/viktordanov/uagent/testing/fixtures"

	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/internal/tui/bubble"
	"github.com/viktordanov/uah/internal/tui/term"
	"github.com/viktordanov/uah/testing/harnesstest"
)

// TestTUI_DiffAndReviewMenu runs /diff on a real git work tree, lists its
// branches after /review branch, and shows that a review needs the
// embedded engine (the fake runner is the process engine).
func TestTUI_DiffAndReviewMenu(t *testing.T) {
	env := harnesstest.NewEnv(t)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	git := func(args ...string) {
		cmd := exec.CommandContext(context.Background(), "git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = env.Workspace
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}
	require.NoError(t, os.WriteFile(filepath.Join(env.Workspace, "a.txt"), []byte("one\n"), 0o600))
	git("init", "-q", "-b", "main")
	git("add", "-A")
	git("commit", "-q", "-m", "first")
	git("branch", "release")
	require.NoError(t, os.WriteFile(filepath.Join(env.Workspace, "a.txt"), []byte("two\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(env.Workspace, "new.txt"), []byte("fresh\n"), 0o600))

	t.Setenv("FAKERUNNER_FIXTURE", fixtures.Path("simple.jsonl"))
	eng := harnesstest.RunnerEngine(uaharness.Config{
		RunnerPath: harnesstest.FakeRunner(t), StateDir: env.StateDir, KillGrace: 300 * time.Millisecond, Getenv: env.Getenv,
	})
	settings := session.Settings{Provider: "openai-codex", Model: "gpt-6-sol", Effort: "high", Workspace: env.Workspace}
	d := start(t, bubble.Deps{
		Open: func(ctx context.Context, _ string) (*session.Session, []session.LoadedRun, error) {
			s, err := session.Open(ctx, eng, session.Options{Settings: settings})

			return s, nil, err
		},
		Sessions: func() ([]session.Info, error) { return session.Sessions(env.StateDir) },
	})
	d.until("the session is open", func() bool { return d.m.(bubble.Model).Exit().SessionID != "" })

	d.typeText("/diff")
	d.key(term.KeyEscape, 0) // close the menu, so enter runs the draft as typed
	d.key(term.KeyEnter, 0)
	d.waitFor("DIFF   2 files (+2 -1)")
	assert.Contains(t, d.view(), "└ a.txt (+1 -1)")
	assert.Contains(t, d.view(), "└ new.txt untracked (+1 -0)")
	assert.Contains(t, d.view(), "fresh")

	d.typeText("/review branch ")
	d.waitFor("release")
	assert.Contains(t, d.view(), "main")
	d.key(term.KeyDown, 0)
	d.key(term.KeyEnter, 0) // the second branch: review against release
	d.waitFor("/review needs the embedded engine")
}
