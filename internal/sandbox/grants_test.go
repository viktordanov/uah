package sandbox_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/sandbox"
	"github.com/viktordanov/uah/testing/harnesstest"
)

// TestGrantable pins that no grant opens the file system's root or the
// home directory, or a directory that holds it, also in another case, or a
// protected directory such as .uah or .git, and that a directory inside
// the home can be granted.
func TestGrantable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	inside := filepath.Join(home, "code")
	require.NoError(t, os.MkdirAll(inside, 0o755))

	assert.False(t, sandbox.Grantable("/"))
	assert.False(t, sandbox.Grantable(home))
	assert.False(t, sandbox.Grantable(filepath.Dir(home)))
	assert.False(t, sandbox.Grantable(strings.ToUpper(filepath.Dir(home))), "another case")
	assert.False(t, sandbox.Grantable(realPath(t, home)), "the home's real path")
	assert.False(t, sandbox.Grantable("relative/dir"))
	assert.False(t, sandbox.Grantable(filepath.Join(home, ".uah")), "uah's home, with its hooks")
	assert.False(t, sandbox.Grantable(filepath.Join(home, ".codex")), "Codex's home")
	assert.False(t, sandbox.Grantable(filepath.Join(inside, ".GIT", "hooks")), "inside a repository's .git, in any case")
	assert.False(t, sandbox.Grantable(filepath.Join(home, ".uah", "sessions")), "inside uah's home")
	assert.True(t, sandbox.Grantable(filepath.Join(home, ".codex", "worktrees", "a1", "repo")), "a worktree Codex keeps")
	assert.True(t, sandbox.Grantable(inside))
}

// TestGrants_Add pins what Add takes: a resolved, existing directory that
// is Grantable and not inside an earlier grant, each told to notify once,
// with a new Version.
func TestGrants_Add(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := realPath(t, t.TempDir())
	var told []sandbox.Grant
	g := sandbox.NewGrants(dir, func(gr sandbox.Grant) { told = append(told, gr) })
	file := filepath.Join(dir, "f")
	require.NoError(t, os.WriteFile(file, nil, 0o644))
	link := filepath.Join(t.TempDir(), "link")
	require.NoError(t, os.Symlink(dir, link))

	assert.False(t, g.Add(file, sandbox.GrantApproved), "a file")
	assert.False(t, g.Add(link, sandbox.GrantApproved), "an unresolved path")
	assert.False(t, g.Add(filepath.Join(dir, "missing"), sandbox.GrantApproved), "a missing directory")
	assert.False(t, g.Add(os.Getenv("HOME"), sandbox.GrantApproved), "the home directory")
	assert.Zero(t, g.Version())

	assert.True(t, g.Add(dir, sandbox.GrantApproved))
	assert.False(t, g.Add(dir, sandbox.GrantWorktree), "already granted")
	sub := filepath.Join(dir, "sub")
	require.NoError(t, os.MkdirAll(sub, 0o755))
	assert.False(t, g.Add(sub, sandbox.GrantApproved), "inside a grant")
	assert.Equal(t, uint64(1), g.Version())
	assert.Equal(t, []sandbox.Grant{{Path: dir, Reason: sandbox.GrantApproved}}, told)
	assert.Equal(t, []string{dir}, g.Roots())

	var none *sandbox.Grants
	assert.Empty(t, none.Roots())
	assert.Zero(t, none.Version())
	assert.False(t, none.Add(dir, sandbox.GrantApproved))
}

// TestGrants_Valid pins the check on resume: a worktree grant holds while
// it is a worktree of the workspace's repository, at the same real path;
// an approved grant never holds, since nothing on disk proves it.
func TestGrants_Valid(t *testing.T) {
	r := newRepo(t)
	g := sandbox.NewGrants(r.main, nil)
	bar := realPath(t, r.bar)
	plain := realPath(t, t.TempDir())

	assert.True(t, g.Valid(sandbox.Grant{Path: bar, Reason: sandbox.GrantWorktree}))
	assert.False(t, g.Valid(sandbox.Grant{Path: bar, Reason: sandbox.GrantApproved}), "an approval is not kept")
	assert.False(t, g.Valid(sandbox.Grant{Path: plain, Reason: sandbox.GrantWorktree}), "not a worktree")
	assert.False(t, g.Valid(sandbox.Grant{Path: filepath.Join(bar, "sub"), Reason: sandbox.GrantWorktree}), "not its root")
	assert.False(t, g.Valid(sandbox.Grant{Path: bar, Reason: "other"}))

	assert.True(t, g.Keep(sandbox.Grant{Path: bar, Reason: sandbox.GrantWorktree}))
	assert.False(t, g.Keep(sandbox.Grant{Path: plain, Reason: sandbox.GrantApproved}))
	assert.Equal(t, []string{bar}, g.Roots())

	harnesstest.Git(t, r.main, "worktree", "remove", r.bar)
	require.NoError(t, os.MkdirAll(r.bar, 0o755))
	assert.False(t, sandbox.NewGrants(r.main, nil).Valid(sandbox.Grant{Path: bar, Reason: sandbox.GrantWorktree}), "removed, a plain directory in its place")
}

// TestGrantFor pins the directory an approval offers: the working tree
// that holds every path, else their common directory, never the home.
func TestGrantFor(t *testing.T) {
	r := newRepo(t)
	t.Setenv("HOME", t.TempDir())
	bar := realPath(t, r.bar)
	plain := realPath(t, r.base)
	require.NoError(t, os.MkdirAll(filepath.Join(plain, "loose"), 0o755))

	assert.Equal(t, bar, sandbox.GrantFor([]string{filepath.Join(bar, "a.txt"), filepath.Join(bar, "x", "new.go")}))
	assert.Equal(t, plain, sandbox.GrantFor([]string{filepath.Join(plain, "x", "a"), filepath.Join(bar, "a.txt")}), "two trees: their common directory")
	assert.Equal(t, filepath.Join(plain, "loose"), sandbox.GrantFor([]string{filepath.Join(plain, "loose", "a"), filepath.Join(plain, "loose", "missing", "b")}))
	assert.Empty(t, sandbox.GrantFor(nil))

	t.Setenv("HOME", filepath.Join(plain, "loose"))
	assert.Empty(t, sandbox.GrantFor([]string{filepath.Join(plain, "loose", "a")}), "the home directory")
	assert.Empty(t, sandbox.GrantFor([]string{filepath.Join(plain, "x", "a"), filepath.Join(bar, "a.txt")}), "a directory that holds the home")
}
