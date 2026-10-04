package sandbox_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/sandbox"
	"github.com/viktordanov/uah/testing/harnesstest"
)

// repo is a repository with one commit at <base>/foo and a linked worktree
// of it at <base>/foo-worktrees/bar, as `git worktree add` makes them.
type repo struct {
	base, main, bar string
}

func newRepo(t *testing.T) repo {
	t.Helper()
	base := t.TempDir()
	r := repo{base: base, main: filepath.Join(base, "foo"), bar: filepath.Join(base, "foo-worktrees", "bar")}
	initRepo(t, r.main)
	harnesstest.Git(t, r.main, "worktree", "add", "-q", "-b", "bar", r.bar)

	return r
}

// initRepo makes a repository with one commit at dir.
func initRepo(t *testing.T, dir string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	harnesstest.Git(t, dir, "init", "-q")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644))
	harnesstest.Git(t, dir, "add", "a.txt")
	harnesstest.Git(t, dir, "commit", "-q", "-m", "first")
}

func realPath(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	require.NoError(t, err)

	return resolved
}

// TestWorktrees_SameRepository pins what counts as a worktree of the
// workspace's repository: a linked worktree, from the main checkout and
// from a subdirectory of it, and the main checkout from a linked worktree,
// each as its real path, also for a file that does not exist yet and a
// path through a symlink; not the workspace's own worktree.
func TestWorktrees_SameRepository(t *testing.T) {
	r := newRepo(t)
	bar := realPath(t, r.bar)

	root, ok := sandbox.NewWorktrees(r.main).Of(filepath.Join(r.bar, "a.txt"))
	assert.True(t, ok)
	assert.Equal(t, bar, root, "the real path of the linked worktree")

	require.NoError(t, os.MkdirAll(filepath.Join(r.main, "pkg"), 0o755))
	w := sandbox.NewWorktrees(filepath.Join(r.main, "pkg"))
	root, ok = w.Of(filepath.Join(r.bar, "new", "dir", "x.go"))
	assert.True(t, ok, "a file to be created")
	assert.Equal(t, bar, root)
	_, ok = w.Of(filepath.Join(r.main, "a.txt"))
	assert.False(t, ok, "the workspace's own worktree is not another one")

	link := filepath.Join(r.base, "link")
	require.NoError(t, os.Symlink(r.bar, link))
	root, ok = w.Of(filepath.Join(link, "a.txt"))
	assert.True(t, ok, "through a symlink")
	assert.Equal(t, bar, root)

	root, ok = sandbox.NewWorktrees(r.bar).Of(filepath.Join(r.main, "a.txt"))
	assert.True(t, ok, "the main checkout, from a linked worktree")
	assert.Equal(t, realPath(t, r.main), root)
}

// TestWorktrees_OtherRepositories pins what is not the same repository: an
// unrelated repository, a directory outside any, a repository nested in a
// worktree, a submodule, and any path when the workspace is no repository.
func TestWorktrees_OtherRepositories(t *testing.T) {
	r := newRepo(t)
	w := sandbox.NewWorktrees(r.main)

	other := filepath.Join(r.base, "other")
	initRepo(t, other)
	_, ok := w.Of(filepath.Join(other, "a.txt"))
	assert.False(t, ok, "an unrelated repository")

	plain := filepath.Join(r.base, "plain")
	require.NoError(t, os.MkdirAll(plain, 0o755))
	_, ok = w.Of(filepath.Join(plain, "x"))
	assert.False(t, ok, "not a repository")
	_, ok = sandbox.NewWorktrees(plain).Of(filepath.Join(r.bar, "a.txt"))
	assert.False(t, ok, "a workspace outside any repository")

	nested := filepath.Join(r.bar, "nested")
	initRepo(t, nested)
	_, ok = w.Of(filepath.Join(nested, "a.txt"))
	assert.False(t, ok, "a repository nested in the worktree")

	harnesstest.Git(t, r.bar, "submodule", "add", "-q", other, "sub")
	_, ok = w.Of(filepath.Join(r.bar, "sub", "a.txt"))
	assert.False(t, ok, "a submodule is another repository")
	_, ok = w.Of(filepath.Join(r.bar, "a.txt"))
	assert.True(t, ok, "the worktree around it still is")
}

// TestWorktrees_BareRepository: worktrees of a bare repository share its
// directory as their common directory, and the bare directory itself is
// no worktree.
func TestWorktrees_BareRepository(t *testing.T) {
	r := newRepo(t)
	bare := filepath.Join(r.base, "r.git")
	harnesstest.Git(t, r.base, "clone", "-q", "--bare", r.main, bare)
	one, two := filepath.Join(r.base, "one"), filepath.Join(r.base, "two")
	harnesstest.Git(t, bare, "worktree", "add", "-q", "-b", "one", one)
	harnesstest.Git(t, bare, "worktree", "add", "-q", "-b", "two", two)
	w := sandbox.NewWorktrees(one)

	root, ok := w.Of(filepath.Join(two, "a.txt"))
	assert.True(t, ok)
	assert.Equal(t, realPath(t, two), root)
	_, ok = w.Of(filepath.Join(bare, "hooks", "pre-commit"))
	assert.False(t, ok, "the bare repository's own directory")
	_, ok = w.Of(filepath.Join(r.bar, "a.txt"))
	assert.False(t, ok, "a worktree of the repository it was cloned from")
}

// TestWorktrees_RelativePaths: git worktree add --relative-paths writes
// both links relative, and they resolve the same.
func TestWorktrees_RelativePaths(t *testing.T) {
	r := newRepo(t)
	rel := filepath.Join(r.base, "foo-worktrees", "rel")
	if out, err := exec.Command("git", "-C", r.main, "worktree", "add", "-q", "--relative-paths", "-b", "rel", rel).CombinedOutput(); err != nil {
		t.Skipf("this git has no --relative-paths: %s", out)
	}
	root, ok := sandbox.NewWorktrees(r.main).Of(filepath.Join(rel, "a.txt"))
	assert.True(t, ok)
	assert.Equal(t, realPath(t, rel), root)
}

// TestWorktrees_CraftedGitFiles pins that a .git entry alone claims
// nothing: a .git file in any directory that names a worktree's git
// directory, or the common directory, or a git directory of its own that
// points back at it and at the common directory, and a .git symlink to
// the common directory, are not worktrees of the repository. Only git's
// own list under <common>/worktrees, which a sandboxed command cannot
// write, decides.
func TestWorktrees_CraftedGitFiles(t *testing.T) {
	r := newRepo(t)
	common := filepath.Join(r.main, ".git")
	w := sandbox.NewWorktrees(r.main)
	crafted := func(name, gitfile string) string {
		dir := filepath.Join(r.base, name)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, ".git"), []byte(gitfile), 0o644))

		return filepath.Join(dir, "x.go")
	}

	for name, gitfile := range map[string]string{
		"steal-admin":  "gitdir: " + filepath.Join(common, "worktrees", "bar") + "\n",
		"steal-common": "gitdir: " + common + "\n",
	} {
		_, ok := w.Of(crafted(name, gitfile))
		assert.False(t, ok, name)
	}

	admin := filepath.Join(r.base, "fake-admin")
	require.NoError(t, os.MkdirAll(admin, 0o755))
	target := crafted("own-admin", "gitdir: "+admin+"\n")
	require.NoError(t, os.WriteFile(filepath.Join(admin, "commondir"), []byte(common+"\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(admin, "gitdir"), []byte(filepath.Join(filepath.Dir(target), ".git")+"\n"), 0o644))
	_, ok := w.Of(target)
	assert.False(t, ok, "a git directory outside <common>/worktrees")

	linked := filepath.Join(r.base, "symlinked")
	require.NoError(t, os.MkdirAll(linked, 0o755))
	require.NoError(t, os.Symlink(common, filepath.Join(linked, ".git")))
	_, ok = w.Of(filepath.Join(linked, "x.go"))
	assert.False(t, ok, "a .git symlink to the common directory")

	_, ok = w.Of(filepath.Join(r.bar, "a.txt"))
	assert.True(t, ok, "the real worktree still is one")
}

// TestWorktrees_HoldingTheCommonDirectory: a worktree whose directory holds
// the repository's common directory, other than as its own .git, is not
// granted: the repository's hooks and configuration would be writable.
func TestWorktrees_HoldingTheCommonDirectory(t *testing.T) {
	base := realPath(t, t.TempDir())
	outer := filepath.Join(base, "outer")
	common := filepath.Join(outer, "inner.git")
	ws, side := filepath.Join(base, "ws"), filepath.Join(base, "side")
	for name, top := range map[string]string{"outer": outer, "ws": ws, "side": side} {
		admin := filepath.Join(common, "worktrees", name)
		require.NoError(t, os.MkdirAll(admin, 0o755))
		require.NoError(t, os.MkdirAll(top, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(top, ".git"), []byte("gitdir: "+admin+"\n"), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(admin, "gitdir"), []byte(filepath.Join(top, ".git")+"\n"), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(admin, "commondir"), []byte("../..\n"), 0o644))
	}
	w := sandbox.NewWorktrees(ws)

	_, ok := w.Of(filepath.Join(outer, "a.txt"))
	assert.False(t, ok, "it holds the common directory")
	root, ok := w.Of(filepath.Join(side, "a.txt"))
	assert.True(t, ok, "a worktree beside it is one")
	assert.Equal(t, side, root)

	alias := filepath.Join(base, "OUTER", "inner.git")
	if _, err := os.Stat(alias); err != nil {
		return // a file system that tells case apart has no alias
	}
	for _, name := range []string{"outer", "ws", "side"} {
		require.NoError(t, os.WriteFile(filepath.Join(common, "worktrees", name, "commondir"), []byte(alias+"\n"), 0o644))
	}
	w = sandbox.NewWorktrees(ws)
	_, ok = w.Of(filepath.Join(outer, "a.txt"))
	assert.False(t, ok, "the common directory named in another case")
	_, ok = w.Of(filepath.Join(side, "a.txt"))
	assert.True(t, ok, "and the worktree beside it still is one")
}

// TestWorktrees_ForgedWorkspace: a workspace outside any repository cannot
// become one by a .git entry a command plants in it, as bubblewrap leaves
// a missing .git writable: the workspace must pass the same check as a
// target, and its repository is read when the finder is made, before any
// command runs.
func TestWorktrees_ForgedWorkspace(t *testing.T) {
	r := newRepo(t)
	common := filepath.Join(r.main, ".git")

	early := filepath.Join(r.base, "early")
	require.NoError(t, os.MkdirAll(early, 0o755))
	w := sandbox.NewWorktrees(early)
	require.NoError(t, os.WriteFile(filepath.Join(early, ".git"), []byte("gitdir: "+common+"\n"), 0o644))
	_, ok := w.Of(filepath.Join(r.main, "a.txt"))
	assert.False(t, ok, "planted after the finder read the workspace")

	for name, plant := range map[string]func(dir string){
		"a .git file naming the common directory": func(dir string) {
			require.NoError(t, os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: "+common+"\n"), 0o644))
		},
		"a .git file naming a worktree's git directory": func(dir string) {
			require.NoError(t, os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: "+filepath.Join(common, "worktrees", "bar")+"\n"), 0o644))
		},
		"a .git directory whose commondir names it": func(dir string) {
			require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git"), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(dir, ".git", "commondir"), []byte(common+"\n"), 0o644))
		},
	} {
		ws := filepath.Join(r.base, strings.ReplaceAll(name, " ", "-"))
		require.NoError(t, os.MkdirAll(ws, 0o755))
		plant(ws)
		w := sandbox.NewWorktrees(ws)
		_, ok := w.Of(filepath.Join(r.main, "a.txt"))
		assert.False(t, ok, name+": the main checkout")
		_, ok = w.Of(filepath.Join(r.bar, "a.txt"))
		assert.False(t, ok, name+": a linked worktree")
	}
}
