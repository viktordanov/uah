package app_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/app"
	"github.com/viktordanov/uah/internal/contextprep"
	"github.com/viktordanov/uah/internal/sandbox"
)

// TestPreviewContext: the modules a new session in the workspace gets, from
// the user's prompts folder, the project's, and [context] modules; a
// project module applies only after TrustContextModules, and again after a
// change only once trusted again.
func TestPreviewContext(t *testing.T) {
	e, in := setupEnv(t)
	writeConfig(t, &in, "[context]\nmodules = [\"go\"]\n")
	prompts := filepath.Join(filepath.Dir(in.ConfigPath), "prompts")
	writeFile(t, filepath.Join(prompts, "context.d", "mine.md"), "---\nid: mine\ndescription: x\n---\nThe user's note.\n")
	repo := filepath.Join(e.Workspace, ".uah", "context.d", "repo.md")
	writeFile(t, repo, "---\nid: repo\ndescription: x\n---\nThe project's note.\n")
	writeFile(t, filepath.Join(e.Workspace, "go.mod"), "module x\n")
	writeFile(t, filepath.Join(e.Workspace, "AGENTS.md"), "Rules.\n")
	ctx := context.Background()

	p, err := app.PreviewContext(ctx, in, os.Getenv)
	require.NoError(t, err)
	assert.Equal(t, e.Workspace, p.Workspace)
	byPath := map[string]contextprep.Status{}
	for _, s := range p.Modules {
		byPath[s.Path] = s
	}
	assert.True(t, byPath["context.d/mine"].Applies)
	assert.False(t, byPath["context.d/repo"].Trusted)
	assert.True(t, byPath["library/go"].Enabled, "[context] modules turns it on")
	assert.Contains(t, p.Main, "\n## mine\nThe user's note.")
	assert.NotContains(t, p.Main, "The project's note.")
	assert.Contains(t, p.Main, "- "+filepath.Join(e.Workspace, "AGENTS.md"))
	assert.Contains(t, p.Main, "(default 40000)")
	if _, err := (sandbox.Policy{Mode: sandbox.ReadOnly, Workspace: e.Workspace}).Wrap([]string{"/bin/sh"}); err == nil {
		assert.Contains(t, p.Subagent, "Sandbox: read-only.")
		assert.Contains(t, p.Main, "Sandbox: workspace-write.")
	}

	paths, err := app.TrustContextModules(in)
	require.NoError(t, err)
	assert.Equal(t, []string{repo}, paths)
	p, err = app.PreviewContext(ctx, in, os.Getenv)
	require.NoError(t, err)
	assert.Contains(t, p.Main, "\n## repo\nThe project's note.")

	writeFile(t, repo, "---\nid: repo\ndescription: x\n---\nA changed note.\n")
	p, err = app.PreviewContext(ctx, in, os.Getenv)
	require.NoError(t, err)
	assert.NotContains(t, p.Main, "A changed note.", "a changed module needs trust again")

	in.NoInstructions = true
	p, err = app.PreviewContext(ctx, in, os.Getenv)
	require.NoError(t, err)
	assert.Contains(t, p.Main, "Loading instruction files (AGENTS.md) is turned off", "as a session with --no-instructions gets it")
	assert.NotContains(t, p.Main, "none to search for")
	assert.Contains(t, p.Subagent, "Loading instruction files (AGENTS.md) is turned off")
}

// TestContextCheck runs a module's check in the read-only sandbox: it can
// run a command, and cannot write the workspace.
func TestContextCheck(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	check := app.ContextCheck(ws, os.Getenv)
	if check == nil {
		t.Skip("no sandbox here")
	}
	ctx := context.Background()
	require.NoError(t, check(ctx, []string{"true"}, ws))
	require.Error(t, check(ctx, []string{"touch", filepath.Join(ws, "written")}, ws))
	_, err := os.Stat(filepath.Join(ws, "written"))
	assert.ErrorIs(t, err, os.ErrNotExist)
}
