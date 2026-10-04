package sandbox_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/sandbox"
)

func TestBwrapArgs(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, base string) sandbox.Policy
	}{
		{"read-only", func(t *testing.T, base string) sandbox.Policy {
			t.Helper()

			return sandbox.Policy{Mode: sandbox.ReadOnly, Workspace: mkdir(t, base, "ws", ".git")}
		}},
		{"read-only-tempdir", func(t *testing.T, base string) sandbox.Policy {
			t.Helper()

			return sandbox.Policy{Mode: sandbox.ReadOnly, Workspace: mkdir(t, base, "ws", ".git"), TempDir: mkdir(t, base, "session-tmp")}
		}},
		{"workspace-write-tempdir", func(t *testing.T, base string) sandbox.Policy {
			t.Helper()

			return sandbox.Policy{Mode: sandbox.WorkspaceWrite, Workspace: mkdir(t, base, "ws", ".git"), TempDir: mkdir(t, base, "session-tmp")}
		}},
		{"workspace-write-git", func(t *testing.T, base string) sandbox.Policy {
			t.Helper()

			return sandbox.Policy{Mode: sandbox.WorkspaceWrite, Workspace: mkdir(t, base, "ws", ".git")}
		}},
		{"workspace-write-nogit", func(t *testing.T, base string) sandbox.Policy {
			t.Helper()

			return sandbox.Policy{Mode: sandbox.WorkspaceWrite, Workspace: mkdir(t, base, "ws")}
		}},
		{"workspace-write-network", func(t *testing.T, base string) sandbox.Policy {
			t.Helper()

			return sandbox.Policy{Mode: sandbox.WorkspaceWrite, Workspace: mkdir(t, base, "ws", ".git"), Network: true}
		}},
		{"workspace-write-worktree", func(t *testing.T, base string) sandbox.Policy {
			t.Helper()
			main := mkdir(t, base, "main", ".git/worktrees/ws")
			ws := mkdir(t, base, "ws")
			require.NoError(t, os.WriteFile(filepath.Join(ws, ".git"), []byte("gitdir: ../main/.git/worktrees/ws\n"), 0o600))

			return sandbox.Policy{Mode: sandbox.WorkspaceWrite, Workspace: ws, WritableRoots: []string{main, filepath.Join(base, "missing")}}
		}},
		{"workspace-write-readonly", func(t *testing.T, base string) sandbox.Policy {
			t.Helper()
			// The sandbox scripts' directory under $TMPDIR, with the
			// session's temporary directory next to it.
			scripts := mkdir(t, base, "tmpdir/home/state/sandbox")
			temp := mkdir(t, base, "tmpdir/home/state/sessions/s/tmp")

			return sandbox.Policy{Mode: sandbox.WorkspaceWrite, Workspace: mkdir(t, base, "ws", ".git"), TempDir: temp, ReadOnly: []string{scripts}}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			base := goldenBase(t)
			t.Setenv("TMPDIR", mkdir(t, base, "tmpdir"))
			got := sandbox.BwrapArgs(c.setup(t, base))
			bwrapGolden(t, "bwrap-"+c.name, render(got, base))
		})
	}
}

func TestBwrapLeavesMissingNamesAlone(t *testing.T) {
	base := goldenBase(t)
	t.Setenv("TMPDIR", "")
	ws := mkdir(t, base, "ws", ".agents")
	args := sandbox.BwrapArgs(sandbox.Policy{Mode: sandbox.WorkspaceWrite, Workspace: ws})
	assert.Contains(t, args, filepath.Join(ws, ".agents"), "an existing protected directory is bound read-only")
	assert.NotContains(t, args, filepath.Join(ws, ".git"), "a missing one creates no mount point on the host")
	assert.NotContains(t, args, "--tmpfs")
}

// goldenBase returns a directory outside /tmp, so /tmp stays a separate
// writable root on every platform and the golden files do not depend on
// where the system keeps temporary files.
func goldenBase(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp(".", ".bwrap-golden-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	abs, err := filepath.Abs(dir)
	require.NoError(t, err)
	abs, err = filepath.EvalSymlinks(abs)
	require.NoError(t, err)

	return abs
}

// mkdir creates base/name and, inside it, each of sub, and returns base/name.
func mkdir(t *testing.T, base, name string, sub ...string) string {
	t.Helper()
	dir := filepath.Join(base, name)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	for _, s := range sub {
		require.NoError(t, os.MkdirAll(filepath.Join(dir, s), 0o700))
	}

	return dir
}

// render prints one mount or option, with its operands, per line, with the
// test's paths replaced by $BASE and the resolved /tmp by $SYSTMP.
func render(args []string, base string) string {
	systmp, err := filepath.EvalSymlinks("/tmp")
	if err != nil {
		systmp = "/tmp"
	}
	var lines []string
	for _, a := range args {
		switch {
		case strings.HasPrefix(a, base):
			a = "$BASE" + strings.TrimPrefix(a, base)
		case a == systmp || strings.HasPrefix(a, systmp+"/"):
			a = "$SYSTMP" + strings.TrimPrefix(a, systmp)
		}
		// --perms applies to the next mount, so keep "--perms 555 --tmpfs P
		// --remount-ro P" on one line.
		last := len(lines) - 1
		grouped := last >= 0 && strings.HasPrefix(lines[last], "--perms") && (a == "--tmpfs" || a == "--remount-ro")
		if (strings.HasPrefix(a, "--") && !grouped) || last < 0 {
			lines = append(lines, a)
		} else {
			lines[len(lines)-1] += " " + a
		}
	}

	// Whether /tmp/.git and the like exist depends on the machine (bwrap
	// leaves empty mount points behind), so only the /tmp bind is compared.
	lines = slices.DeleteFunc(lines, func(l string) bool { return strings.Contains(l, "$SYSTMP/") })

	return strings.Join(lines, "\n") + "\n"
}

func bwrapGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		require.NoError(t, os.MkdirAll("testdata", 0o700))
		require.NoError(t, os.WriteFile(path, []byte(got), 0o600))
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "run go test ./internal/sandbox -run TestBwrapArgs -update")
	assert.Equal(t, string(want), got)
}

func TestBwrapArgsNetwork(t *testing.T) {
	ws := t.TempDir()
	off := sandbox.BwrapArgs(sandbox.Policy{Mode: sandbox.WorkspaceWrite, Workspace: ws})
	on := sandbox.BwrapArgs(sandbox.Policy{Mode: sandbox.WorkspaceWrite, Workspace: ws, Network: true})
	assert.True(t, slices.Contains(off, "--unshare-net"))
	assert.False(t, slices.Contains(on, "--unshare-net"))
}
