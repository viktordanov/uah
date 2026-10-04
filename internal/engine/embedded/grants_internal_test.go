package embedded

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/patch"
	"github.com/viktordanov/uah/internal/sandbox"
	"github.com/viktordanov/uah/testing/harnesstest"
)

// TestCommandPaths pins the paths an escalated command names: words with a
// slash or a dot directory, in arguments, redirections, flag values, and
// substitutions, a glob's directory, resolved against the working
// directory; not plain words.
func TestCommandPaths(t *testing.T) {
	cwd := sandbox.ResolvePath(t.TempDir())
	got := commandPaths(`cd ../wt && gofmt -w ./pkg/*.go > "/abs/out log" --file=rel/x $(cat /etc/conf) ~/y plain`, cwd)
	assert.Equal(t, []string{
		filepath.Join(filepath.Dir(cwd), "wt"),
		filepath.Join(cwd, "pkg"),
		filepath.Join(cwd, "rel", "x"),
		sandbox.ResolvePath("/etc/conf"),
		filepath.Join(cwd, "~", "y"),
		"/abs/out log",
	}, got)
	assert.Equal(t, []string{cwd}, commandPaths("ls . .", cwd), "each once")
	assert.Empty(t, commandPaths("echo 'unclosed", cwd), "a command that does not parse names none")
}

// TestBoxes_FailClosed: when the grants change and a shell cannot be built
// again, no command gets a shell, not even the one built before, and the
// next command tries again.
func TestBoxes_FailClosed(t *testing.T) {
	grants := sandbox.NewGrants(t.TempDir(), nil)
	fail := true
	built := 0
	b := &boxes{
		build: func(sandbox.Mode) (sandboxShell, error) {
			if fail {
				return sandboxShell{}, errors.New("disk full")
			}
			built++

			return sandboxShell{shell: fmt.Sprintf("sh-%d", built)}, nil
		},
		grants: grants, byMode: map[sandbox.Mode]sandboxShell{sandbox.WorkspaceWrite: {shell: "sh-0"}}, built: map[string]sandbox.Mode{},
	}
	box, ok, err := b.get(sandbox.WorkspaceWrite)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "sh-0", box.shell)

	require.True(t, grants.Add(sandbox.ResolvePath(t.TempDir()), sandbox.GrantApproved))
	_, _, err = b.get(sandbox.WorkspaceWrite)
	require.ErrorContains(t, err, "disk full")
	_, _, err = b.get(sandbox.WorkspaceWrite)
	require.Error(t, err, "still failing, tried again")

	fail = false
	box, ok, err = b.get(sandbox.WorkspaceWrite)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "sh-1", box.shell)
}

// TestWithGrants pins which grants a policy takes, in any order: not one
// that holds the workspace, also under a case alias, and not one inside a
// protected path of another grant.
func TestWithGrants(t *testing.T) {
	base := sandbox.ResolvePath(t.TempDir())
	ws := filepath.Join(base, "Repo", "child")
	outer := filepath.Join(base, "outer")
	inner := filepath.Join(outer, ".agents", "inner")
	for _, d := range []string{ws, inner} {
		require.NoError(t, os.MkdirAll(d, 0o755))
	}
	p := sandbox.Policy{Mode: sandbox.WorkspaceWrite, Workspace: ws}

	for _, order := range [][]string{{outer, inner}, {inner, outer}} {
		assert.Equal(t, []string{outer}, withGrants(p, order).WritableRoots, "inside the other grant's .agents")
	}
	assert.Empty(t, withGrants(p, []string{filepath.Join(base, "Repo")}).WritableRoots, "holds the workspace")
	if alias := filepath.Join(base, "repo"); sandbox.ResolvePath(alias) == alias {
		if _, err := os.Stat(alias); err == nil {
			assert.Empty(t, withGrants(p, []string{alias}).WritableRoots, "holds the workspace under another case")
		}
	}
}

// TestPatchGate_Recheck: when the patch starts, a path the policy let it
// write when it was checked, and not approved, must still be writable.
func TestPatchGate_Recheck(t *testing.T) {
	ws := sandbox.ResolvePath(t.TempDir())
	outside := sandbox.ResolvePath(harnesstest.OutsideDir(t, "uah-recheck-"))
	writable := true
	g := patchGate{policy: func() sandbox.Policy {
		p := sandbox.Policy{Mode: sandbox.WorkspaceWrite, Workspace: ws}
		if writable {
			p.WritableRoots = []string{outside}
		}

		return p
	}}
	targets := patch.Targets{"a": filepath.Join(ws, "a"), "b": filepath.Join(outside, "b")}

	assert.Empty(t, g.recheck(targets, nil))
	writable = false
	assert.Contains(t, g.recheck(targets, nil), "b is no longer inside the writable roots")
	assert.Empty(t, g.recheck(targets, []string{"b"}), "approved")
}
