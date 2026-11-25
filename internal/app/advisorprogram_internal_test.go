package app

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/engine/embedded"
	"github.com/viktordanov/uah/internal/sandbox"
)

// TestPinAdvisorProgram: an advisor program the agent's commands could
// change turns the command backend off with a notice, by path, found in
// PATH, or through a link the agent could change, whatever mode the
// session starts in; one outside is pinned to its real path, with the
// command's name kept; a missing one (the other notice's) has no program;
// an advisor that is off is left as it is.
func TestPinAdvisorProgram(t *testing.T) {
	ws := t.TempDir()
	bin := filepath.Join(ws, "bin")
	require.NoError(t, os.MkdirAll(bin, 0o700))
	script := filepath.Join(bin, "my-advisor")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\necho advice\n"), 0o700))
	other := t.TempDir()
	policy := func(mode sandbox.Mode, roots ...string) sandbox.Policy {
		return sandbox.Policy{Mode: mode, Workspace: ws, WritableRoots: roots}
	}
	cmd := func(argv ...string) embedded.Advisor { return embedded.Advisor{Command: argv} }
	checkAdvisorProgram := func(a embedded.Advisor, p sandbox.Policy) error {
		pinned, refused := pinAdvisorProgram(a, p)
		if refused != "" {
			if pinned.Command != nil {
				return errors.New("refused, but the command stays")
			}

			return errors.New(refused)
		}

		return nil
	}

	for _, mode := range []sandbox.Mode{sandbox.WorkspaceWrite, sandbox.ReadOnly, sandbox.FullAccess} {
		err := checkAdvisorProgram(cmd(script), policy(mode))
		require.Error(t, err, mode)
		assert.Contains(t, err.Error(), "is where the agent's commands can write")
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	require.Error(t, checkAdvisorProgram(cmd("my-advisor"), policy(sandbox.WorkspaceWrite)), "found in PATH, in the workspace")
	temp := filepath.Join(other, "advise.sh") // under TMPDIR, which sandboxed commands write
	require.NoError(t, os.WriteFile(temp, []byte("#!/bin/sh\n"), 0o700))
	require.Error(t, checkAdvisorProgram(cmd(temp), policy(sandbox.WorkspaceWrite)), "in the temporary directory")

	require.NoError(t, checkAdvisorProgram(cmd("/bin/sh", filepath.Join(ws, "x.sh")), policy(sandbox.WorkspaceWrite)), "only the program is checked")
	require.NoError(t, checkAdvisorProgram(cmd("/usr/bin/env"), policy(sandbox.WorkspaceWrite)))
	require.NoError(t, os.Symlink("/usr/bin", filepath.Join(ws, "tools")))
	linked, refused := pinAdvisorProgram(cmd(filepath.Join(ws, "tools", "env")), policy(sandbox.WorkspaceWrite))
	assert.Contains(t, refused, filepath.Join(ws, "tools", "env")+" is where the agent's commands can write", "the link in the workspace picks the program")
	assert.Nil(t, linked.Command)
	assert.Equal(t, "its program, "+filepath.Join(ws, "tools", "env")+", is where the agent's commands can write", linked.Refused, "for /advisor")

	t.Setenv("PATH", "/usr/bin:/bin")
	pinned, refused := pinAdvisorProgram(cmd("env", "-u", "X", "claude"), policy(sandbox.WorkspaceWrite))
	require.Empty(t, refused)
	assert.Equal(t, sandbox.ResolvePath("/usr/bin/env"), pinned.Program, "the program found in PATH, as its real path")
	assert.Equal(t, []string{"env", "-u", "X", "claude"}, pinned.Command, "the name the command sees stays")
	missing, refused := pinAdvisorProgram(cmd("no-such-advisor-program"), policy(sandbox.WorkspaceWrite))
	require.Empty(t, refused)
	assert.Equal(t, []string{"no-such-advisor-program"}, missing.Command)
	assert.Empty(t, missing.Program, "no program, so the engine runs nothing")
	require.NoError(t, checkAdvisorProgram(embedded.Advisor{Command: []string{script}, Off: true}, policy(sandbox.WorkspaceWrite)))
	require.NoError(t, checkAdvisorProgram(embedded.Advisor{Model: "m"}, policy(sandbox.WorkspaceWrite)))
}
