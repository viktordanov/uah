package sandbox_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/sandbox"
)

func TestPolicy_CanWrite(t *testing.T) {
	ws := t.TempDir()
	extra := t.TempDir()
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	outside := filepath.Join(home, "uah-canwrite-never-created", "x.txt")
	link := filepath.Join(ws, "out")
	require.NoError(t, os.Symlink(filepath.Dir(outside), link))

	p := sandbox.Policy{Mode: sandbox.WorkspaceWrite, Workspace: ws, WritableRoots: []string{extra}}
	assert.True(t, p.CanWrite(filepath.Join(ws, "a/b.txt")), "a new file in the workspace")
	assert.True(t, p.CanWrite(filepath.Join(extra, "c.txt")), "a writable root")
	assert.False(t, p.CanWrite(outside), "outside every root")
	assert.False(t, p.CanWrite(filepath.Join(link, "x.txt")), "a symlink out of the workspace")
	for _, name := range sandbox.ProtectedNames {
		assert.False(t, p.CanWrite(filepath.Join(ws, name, "config")), name+" stays protected")
	}

	p.Mode = sandbox.ReadOnly
	assert.False(t, p.CanWrite(filepath.Join(ws, "a.txt")))
	assert.False(t, p.CanWrite(filepath.Join(extra, "c.txt")))
	p.TempDir = t.TempDir()
	assert.True(t, p.CanWrite(filepath.Join(p.TempDir, "a.txt")), "read-only can write the temp dir")
	assert.False(t, p.CanWrite(filepath.Join(p.TempDir, ".git", "config")), "except its protected paths")
	assert.False(t, p.CanWrite(filepath.Join(ws, "a.txt")))
	p.Mode = sandbox.FullAccess
	assert.True(t, p.CanWrite(outside))
}

// TestCanWriteReadOnly checks that a ReadOnly path, such as the sandbox
// scripts' directory, stays read-only inside a writable root.
func TestCanWriteReadOnly(t *testing.T) {
	ws, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	scripts := filepath.Join(ws, "home", "state", "sandbox")
	p := sandbox.Policy{Mode: sandbox.WorkspaceWrite, Workspace: ws, ReadOnly: []string{scripts}}
	assert.False(t, p.CanWrite(scripts))
	assert.False(t, p.CanWrite(filepath.Join(scripts, "sh-0123")))
	assert.True(t, p.CanWrite(filepath.Join(ws, "home", "state", "other")))
	p.Mode = sandbox.FullAccess
	assert.True(t, p.CanWrite(filepath.Join(scripts, "sh-0123")), "full access has no sandbox")
}
