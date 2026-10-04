package sandbox

import (
	"os"
	"path/filepath"
	"strings"
)

// Worktrees finds the git worktrees of one repository, the workspace's,
// without running git: it reads the .git entries, gitdir files, and
// commondir files git writes. A target is in a worktree of the same
// repository when the nearest .git entry above it leads to the same common
// directory as the workspace's (git rev-parse --git-common-dir, resolved),
// and the repository itself lists that worktree, so a .git file planted in
// any directory cannot claim it. The workspace must pass the same check,
// and NewWorktrees reads its repository at once, before the session runs
// a command that could plant a .git file in it. Each target is read
// afresh, so a worktree removed or replaced since is not taken from a
// cache. It is safe for concurrent use.
type Worktrees struct {
	common string // the workspace's common directory, resolved; "" outside a repository
	top    string // the workspace's working tree, resolved
}

// NewWorktrees returns the finder for the workspace's repository.
func NewWorktrees(workspace string) *Worktrees {
	w := &Worktrees{}
	if t, ok := findTree(ResolvePath(workspace)); ok && t.listed() != "" {
		w.top, w.common = t.top, t.common
	}

	return w
}

// Of returns the root of the worktree of the workspace's repository that
// holds path, resolved, when it is another worktree than the workspace's
// own: a linked worktree (git worktree add), or the main checkout of a
// workspace that is a linked worktree. A submodule, a nested repository,
// and an unrelated repository are not the same repository, and neither is
// a worktree whose directory holds the common directory other than as its
// own .git.
func (w *Worktrees) Of(path string) (string, bool) {
	root, _, ok := w.check(path)

	return root, ok
}

// check is Of with the worktree's directory as it was checked: the same
// directory before and after its files were read, so a grant can be held
// to it (Grants.AddWorktree).
func (w *Worktrees) check(path string) (string, os.FileInfo, bool) {
	if w.common == "" {
		return "", nil, false
	}
	t, ok := findTree(ResolvePath(path))
	if !ok || sameFile(t.top, w.top) {
		return "", nil, false
	}
	before, err := os.Lstat(t.top)
	if common := t.listed(); err != nil || common == "" || !sameFile(common, w.common) {
		return "", nil, false
	}
	if holds(t.top, w.common) && !sameFile(w.common, t.dotgit) {
		// The repository's hooks and configuration would be writable
		// under the grant, and git runs them outside the sandbox.
		return "", nil, false
	}
	if after, err := os.Lstat(t.top); err != nil || !os.SameFile(before, after) {
		return "", nil, false
	}

	return t.top, before, true
}

// holds reports whether dir is path or one of its directories, by name
// without case or by identity, so a case alias or another name for dir
// counts.
func holds(dir, path string) bool {
	if withinFold(path, dir) {
		return true
	}
	info, err := os.Stat(dir)
	if err != nil {
		return false
	}
	for d := path; ; d = filepath.Dir(d) {
		if di, err := os.Stat(d); err == nil && os.SameFile(di, info) {
			return true
		}
		if filepath.Dir(d) == d {
			return false
		}
	}
}

func sameFile(a, b string) bool {
	ai, aerr := os.Stat(a)
	bi, berr := os.Stat(b)

	return aerr == nil && berr == nil && os.SameFile(ai, bi)
}

// tree is a git working tree: its top directory, the .git entry there, the
// git directory it leads to, and the repository's common directory, all
// resolved.
type tree struct {
	top, dotgit, gitdir, common string
	// linked is a .git file (a linked worktree, a submodule, or a separate
	// git directory); otherwise .git is the directory itself.
	linked bool
}

// findTree returns the working tree whose top is the nearest directory at
// or above path with a .git entry. The nearest entry decides, so a nested
// repository or a submodule is never taken for the repository around it;
// an entry that is neither a directory nor a regular file (a symlink) is
// no tree.
func findTree(path string) (tree, bool) {
	for dir := path; ; dir = filepath.Dir(dir) {
		dotgit := filepath.Join(dir, ".git")
		info, err := os.Lstat(dotgit)
		if err == nil {
			return treeAt(dir, dotgit, info)
		}
		if filepath.Dir(dir) == dir {
			return tree{}, false
		}
	}
}

func treeAt(top, dotgit string, info os.FileInfo) (tree, bool) {
	t := tree{top: top, dotgit: dotgit}
	switch {
	case info.IsDir():
		t.gitdir = dotgit
	case info.Mode().IsRegular():
		target := gitdirTarget(dotgit)
		if target == "" {
			return tree{}, false
		}
		t.gitdir, t.linked = resolveDir(target), true
	default:
		return tree{}, false
	}
	t.common = t.gitdir
	if dir, ok := readPath(filepath.Join(t.gitdir, "commondir")); ok {
		t.common = resolveDir(dir)
	}

	return t, t.gitdir != "" && t.common != ""
}

// listed returns the tree's common directory when the repository lists the
// tree as its own, else "". Directories are compared by identity, so
// another spelling of one, such as another case, is the same. A linked worktree's git directory must be
// <common>/worktrees/<name>, and its gitdir file must point back at the
// tree's .git file, as git writes both on git worktree add; a .git file
// that only names the directory, from anywhere, is not enough. The main
// checkout's .git must be the common directory itself, a real directory.
// A submodule's or a separate git directory has no commondir, so its
// common directory is its own and never the workspace's.
func (t tree) listed() string {
	if !t.linked {
		if t.common != t.gitdir || resolveDir(t.dotgit) != t.common {
			return ""
		}

		return t.common
	}
	if sameFile(t.common, t.gitdir) || !sameFile(filepath.Dir(t.gitdir), filepath.Join(t.common, "worktrees")) {
		return ""
	}
	back, ok := readPath(filepath.Join(t.gitdir, "gitdir"))
	if !ok || !sameFile(back, t.dotgit) || !sameFile(filepath.Dir(back), t.top) {
		// The directory counts, not only the file: a hard link to a
		// worktree's .git file elsewhere is the same file.
		return ""
	}

	return t.common
}

// readPath reads a file git writes with one path in it, such as commondir
// or gitdir, made absolute against the file's directory.
func readPath(file string) (string, bool) {
	data, err := os.ReadFile(file)
	if err != nil {
		return "", false
	}
	p := strings.TrimSpace(string(data))
	if p == "" || strings.ContainsRune(p, '\n') {
		return "", false
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(filepath.Dir(file), p)
	}

	return filepath.Clean(p), true
}

// resolveDir is an existing directory's real path, or "".
func resolveDir(path string) string {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return ""
	}
	if info, err := os.Stat(resolved); err != nil || !info.IsDir() {
		return ""
	}

	return resolved
}
