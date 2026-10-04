package embedded_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/approval"
	"github.com/viktordanov/uah/internal/compaction"
	"github.com/viktordanov/uah/internal/engine/embedded"
	"github.com/viktordanov/uah/internal/sandbox"
	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/testing/fakellm"
	"github.com/viktordanov/uah/testing/harnesstest"
)

// TestEmbedded_Sandbox runs real commands in the workspace-write sandbox:
// writes inside the workspace work, writes elsewhere and to .git fail with a
// hint, and escalations are denied with a reason in a headless session.
func TestEmbedded_Sandbox(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	policy := sandbox.Policy{Mode: sandbox.WorkspaceWrite, Workspace: ws}
	if _, err := policy.Wrap([]string{"/bin/sh"}); err != nil {
		t.Skipf("no sandbox here: %v", err)
	}
	outside := harnesstest.OutsideDir(t, "uah-sandbox-")
	require.NoError(t, os.MkdirAll(filepath.Join(ws, ".git"), 0o700))

	e := newEnv(t,
		fakellm.Reply{
			Commands:  []string{"echo ok > inside.txt", "echo no > " + filepath.Join(outside, "x.txt"), "echo no > .git/config"},
			Escalated: []string{"curl -s https://example.com"},
		},
		fakellm.Reply{Text: "done"},
	)
	e.Workspace = ws
	eng := embedded.New(embedded.Config{
		StateDir: e.StateDir, Provider: "openai", Getenv: e.getenv,
		Sandbox: &policy, SandboxDir: filepath.Join(e.StateDir, "sandbox"),
	})
	s, err := session.Open(context.Background(), eng, session.Options{Settings: e.settings()})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	ev := &events{t: t, s: s}

	_, err = s.Submit("try some writes")
	require.NoError(t, err)
	result := ev.finished()
	assert.Equal(t, core.StatusOK, result.Status)

	assert.FileExists(t, filepath.Join(ws, "inside.txt"))
	assert.NoFileExists(t, filepath.Join(outside, "x.txt"))
	assert.NoFileExists(t, filepath.Join(ws, ".git", "config"))
	reqs := e.llm.Requests()
	assert.Contains(t, reqs[0].Tools["Bash"], "sandbox_permissions", "the model is offered escalation")
	outputs := strings.Join(reqs[len(reqs)-1].ToolOutputs, "\n---\n")
	assert.Equal(t, 2, strings.Count(outputs, "sandbox likely blocked this"), outputs)
	assert.Contains(t, outputs, "no user can approve it in this headless run")
}

// TestEmbedded_SandboxReadOnlyTempDir runs a command in the read-only
// sandbox: it can write the session's private $TMPDIR, under its operation
// directory, and nothing else.
func TestEmbedded_SandboxReadOnlyTempDir(t *testing.T) {
	ws := t.TempDir()
	policy := sandbox.Policy{Mode: sandbox.ReadOnly, Workspace: ws}
	if _, err := policy.Wrap([]string{"/bin/sh"}); err != nil {
		t.Skipf("no sandbox here: %v", err)
	}
	e := newEnv(t,
		fakellm.Reply{Commands: []string{`printf '%s' "$TMPDIR" && echo ok > "$TMPDIR/t.txt"`, "echo no > inside.txt"}},
		fakellm.Reply{Text: "done"},
	)
	e.Workspace = ws
	eng := embedded.New(embedded.Config{
		StateDir: e.StateDir, Provider: "openai", Getenv: e.getenv,
		Sandbox: &policy, SandboxDir: filepath.Join(e.StateDir, "sandbox"),
	})
	s, err := session.Open(context.Background(), eng, session.Options{Settings: e.settings().WithMode(approval.ModeReadOnly)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	ev := &events{t: t, s: s}

	_, err = s.Submit("write a temporary file")
	require.NoError(t, err)
	assert.Equal(t, core.StatusOK, ev.finished().Status)

	temp := session.TempDir(filepath.Join(e.StateDir, "sessions"), s.ID())
	assert.FileExists(t, filepath.Join(temp, "t.txt"))
	assert.NoFileExists(t, filepath.Join(ws, "inside.txt"))
	reqs := e.llm.Requests()
	outputs := strings.Join(reqs[len(reqs)-1].ToolOutputs, "\n---\n")
	assert.Contains(t, outputs, temp)
	assert.Equal(t, 1, strings.Count(outputs, "sandbox likely blocked this"), outputs)
}

// TestEmbedded_CompactionSurvivesANewSandboxPolicy: a command the sandbox
// blocked has a hint in its output, which a compaction covers. A session
// resumed under a changed policy, as after an upgrade, has a new
// sandboxing shell, and renders the old output with its hint all the same,
// so a compaction saved before Shape, which only its hash guards, still
// applies instead of leaving the full history.
func TestEmbedded_CompactionSurvivesANewSandboxPolicy(t *testing.T) {
	ws := t.TempDir()
	policy := sandbox.Policy{Mode: sandbox.WorkspaceWrite, Workspace: ws}
	if _, err := policy.Wrap([]string{"/bin/sh"}); err != nil {
		t.Skipf("no sandbox here: %v", err)
	}
	outside := harnesstest.OutsideDir(t, "uah-sandbox-")
	e := newEnv(t,
		fakellm.Reply{Commands: []string{"echo no > " + filepath.Join(outside, "x.txt")}},
		fakellm.Reply{Text: "answer one"},
		fakellm.Reply{Text: "SUMMARY"},
		fakellm.Reply{Text: "answer two"},
		fakellm.Reply{Text: "answer three"},
	)
	e.Workspace = ws
	sandboxed := func(p sandbox.Policy) *embedded.Engine {
		return embedded.New(embedded.Config{
			StateDir: e.StateDir, Provider: "openai", Getenv: e.getenv,
			Sandbox: &p, SandboxDir: filepath.Join(e.StateDir, "sandbox"),
		})
	}
	s, ev := e.open(t, sandboxed(policy), "")
	ask(t, s, ev, "first")
	require.NoError(t, s.Compact())
	ask(t, s, ev, "second")
	id := s.ID()
	require.NoError(t, s.Close())
	require.Contains(t, strings.Join(e.llm.Requests()[1].ToolOutputs, "\n"), "sandbox likely blocked this")

	log := compaction.OpenLog(filepath.Join(e.StateDir, "sessions"), id)
	records, _, err := log.Records()
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.NoError(t, os.Remove(log.Path()))
	legacy := records[0]
	legacy.Shape = "" // as uah wrote it before Shape
	require.NoError(t, log.Append(legacy))

	policy.WritableRoots = []string{t.TempDir()}
	s2, ev2 := e.open(t, sandboxed(policy), id)
	ask(t, s2, ev2, "third")

	reqs := e.llm.Requests()
	require.Len(t, reqs, 5)
	require.Len(t, reqs[3].UserTexts, 3)
	assert.True(t, strings.HasPrefix(reqs[3].UserTexts[1], summaryText("SUMMARY")), "compacted")
	assert.Equal(t, append(slices.Clone(reqs[3].UserTexts), "third"), reqs[4].UserTexts, "the compaction still applies")
	_, done := compactions(ev2.all)
	assert.Empty(t, done, "no mismatch")
}
