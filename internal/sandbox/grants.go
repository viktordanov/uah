package sandbox

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// GrantReason is why a session made a directory writable.
type GrantReason string

const (
	// GrantWorktree is a git worktree of the workspace's repository, made
	// writable without asking anyone (Worktrees).
	GrantWorktree GrantReason = "worktree"
	// GrantApproved is a directory the user allowed writes to for the
	// session, in an approval prompt.
	GrantApproved GrantReason = "approved"
)

// Grant is a directory a session made writable: a writable root for its
// patches and its sandboxed commands, for the rest of the session.
type Grant struct {
	// Path is absolute, with its symlinks resolved.
	Path   string      `json:"path"`
	Reason GrantReason `json:"reason"`
}

// Grants are a session's grants, shared with its subagents. A grant lasts
// while the session lives and its directory stays the same directory;
// nothing writes grants to a configuration file. It is safe for
// concurrent use.
type Grants struct {
	// Worktrees finds the worktrees of the workspace's repository.
	Worktrees *Worktrees

	mu      sync.Mutex
	grants  []granted
	version uint64
	notify  func(Grant)
}

// granted is a grant and the directory it was made for, which a later
// directory at the same path is not.
type granted struct {
	Grant

	dir os.FileInfo
}

// NewGrants returns no grants for the workspace, whose repository it reads
// at once (NewWorktrees). notify, when set, hears each new grant, on the
// goroutine that adds it.
func NewGrants(workspace string, notify func(Grant)) *Grants {
	return &Grants{Worktrees: NewWorktrees(workspace), notify: notify}
}

// Add makes the directory writable for the session and reports whether it
// is new. A path that is not an existing directory, is not resolved, or
// fails Grantable is refused; so is one a grant already covers.
func (g *Grants) Add(path string, reason GrantReason) bool {
	_, ok := g.add(Grant{Path: path, Reason: reason}, nil, true)

	return ok
}

// AddWorktree grants the worktree of the workspace's repository that holds
// path (Worktrees.Of), when allow accepts its root, and returns the root
// when it added it. The grant holds for the directory the check read, so
// one swapped in at the same path between the check and the grant is not
// granted.
func (g *Grants) AddWorktree(path string, allow func(root string) bool) (string, bool) {
	if g == nil {
		return "", false
	}
	root, dir, ok := g.Worktrees.check(path)
	if !ok || !allow(root) {
		return "", false
	}

	return g.add(Grant{Path: root, Reason: GrantWorktree}, dir, true)
}

// Keep adds a grant kept from before a resume when it is still Valid,
// without telling notify, and reports whether it did.
func (g *Grants) Keep(gr Grant) bool {
	if g == nil {
		return false
	}
	dir, ok := g.valid(gr)
	if !ok {
		return false
	}
	_, ok = g.add(gr, dir, false)

	return ok
}

// add adds the grant for the directory dir, or the one at its path now
// when dir is nil, telling notify when tell is set, and returns the path it
// keeps. The path is kept as
// the file system spells it (Canonical), so two grants, or a grant and a
// protected path, never name one directory in two ways that the sandbox,
// which compares names as strings, would take apart.
func (g *Grants) add(gr Grant, dir os.FileInfo, tell bool) (string, bool) {
	if g == nil || !Grantable(gr.Path) {
		return "", false
	}
	info, ok := sameDir(gr.Path, dir)
	if !ok {
		return "", false
	}
	canonical, ok := Canonical(gr.Path)
	if gr.Path = canonical; !ok || !Grantable(gr.Path) {
		return "", false
	}
	if _, ok := sameDir(gr.Path, info); !ok {
		return "", false
	}
	g.mu.Lock()
	for _, have := range g.grants {
		if within(gr.Path, have.Path) {
			g.mu.Unlock()

			return "", false
		}
	}
	g.grants = append(g.grants, granted{Grant: gr, dir: info})
	g.version++
	notify := g.notify
	g.mu.Unlock()
	if tell && notify != nil {
		notify(gr)
	}

	return gr.Path, true
}

// List returns the grants that still hold, in the order they were made
// (Roots).
func (g *Grants) List() []Grant {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	kept := g.grants[:0]
	for _, gr := range g.grants {
		if _, ok := sameDir(gr.Path, gr.dir); ok {
			kept = append(kept, gr)
		}
	}
	if len(kept) != len(g.grants) {
		clear(g.grants[len(kept):])
		g.version++
	}
	g.grants = kept
	out := make([]Grant, 0, len(kept))
	for _, gr := range kept {
		out = append(out, gr.Grant)
	}

	return out
}

// Roots returns the granted directories. A grant whose directory is gone,
// or was replaced by another directory or a symlink since, is dropped
// first, so it never leads the sandbox elsewhere.
func (g *Grants) Roots() []string {
	var roots []string
	for _, gr := range g.List() {
		roots = append(roots, gr.Path)
	}

	return roots
}

// Version changes whenever a grant is added or dropped, so a sandboxing
// shell built for the earlier roots is built again. It drops the grants
// that no longer hold first (Roots).
func (g *Grants) Version() uint64 {
	if g == nil {
		return 0
	}
	g.List()
	g.mu.Lock()
	defer g.mu.Unlock()

	return g.version
}

// sameDir reports whether path is a real directory, not reached through a
// symlink, and, when was is set, the same directory as was.
func sameDir(path string, was os.FileInfo) (os.FileInfo, bool) {
	if resolveDir(path) != path {
		return nil, false
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || (was != nil && !os.SameFile(info, was)) {
		return nil, false
	}

	return info, true
}

// Valid reports whether a grant kept from before a resume still holds: a
// worktree grant that is still a worktree of the workspace's repository,
// rooted at the same directory, and still Grantable. An approved grant is
// never kept: it rests on the user's answer alone, which nothing on disk
// can prove, and a sidecar a sandboxed command could write must not widen
// what the session may write.
func (g *Grants) Valid(gr Grant) bool {
	_, ok := g.valid(gr)

	return ok
}

func (g *Grants) valid(gr Grant) (os.FileInfo, bool) {
	if gr.Reason != GrantWorktree || !Grantable(gr.Path) || resolveDir(gr.Path) != gr.Path {
		return nil, false
	}
	root, dir, ok := g.Worktrees.check(gr.Path)

	return dir, ok && root == gr.Path
}

// GrantFor returns the directory an approval may offer to make writable for
// writes to paths (resolved, as ResolvePath returns them): the top of the
// git working tree that holds them all, else their nearest common existing
// directory; "" when that directory is not Grantable.
func GrantFor(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	inputs := make([]string, 0, len(paths))
	for _, p := range paths {
		s, ok := Canonical(p)
		if !ok {
			return ""
		}
		inputs = append(inputs, s)
	}
	paths = inputs
	dir := ""
	if t, ok := findTree(paths[0]); ok {
		dir = t.top
		for _, p := range paths[1:] {
			if other, ok := findTree(p); !ok || other.top != dir {
				dir = ""

				break
			}
		}
	}
	if dir == "" {
		dir = filepath.Dir(paths[0])
		for _, p := range paths[1:] {
			for !within(p, dir) {
				dir = filepath.Dir(dir)
			}
		}
		for resolveDir(dir) != dir && filepath.Dir(dir) != dir {
			dir = filepath.Dir(dir)
		}
	}
	if resolveDir(dir) != dir || !Grantable(dir) {
		return ""
	}
	spelled, ok := Canonical(dir)
	if !ok || !Grantable(spelled) {
		return ""
	}

	return spelled
}

// Grantable reports whether a directory may be granted at all: an absolute
// path that is not the file system's root, neither is nor holds the user's
// home directory, so no grant opens the whole home, is not itself one of
// ProtectedNames, such as ~/.uah or ~/.codex, and is not inside a .git,
// .uah, or .uagent directory, whose hooks and configuration run outside
// the sandbox. A directory deeper inside .codex or .agents, such as a
// worktree Codex keeps under ~/.codex/worktrees, can be granted: the
// protected directory itself stays out of the grant. Names are compared
// without case, and a directory that is the home directory or one above it
// under another name counts too.
func Grantable(dir string) bool {
	if !filepath.IsAbs(dir) || filepath.Dir(dir) == dir || isProtectedName(filepath.Base(dir), ProtectedNames) {
		return false
	}
	for name := range strings.SplitSeq(filepath.Dir(dir), string(filepath.Separator)) {
		if isProtectedName(name, hookNames) {
			return false
		}
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return false
	}
	info, statErr := os.Stat(dir)
	for _, h := range []string{filepath.Clean(home), ResolvePath(home)} {
		if withinFold(h, dir) {
			return false
		}
		for d := h; statErr == nil; d = filepath.Dir(d) {
			if di, err := os.Stat(d); err == nil && os.SameFile(di, info) {
				return false
			}
			if filepath.Dir(d) == d {
				break
			}
		}
	}

	return true
}

// hookNames are the protected directories whose whole tree holds code that
// runs outside the sandbox: a repository's hooks and configuration, and
// uah's own.
var hookNames = []string{".git", ".uah", ".uagent"}

func isProtectedName(name string, names []string) bool {
	return slices.ContainsFunc(names, func(p string) bool { return strings.EqualFold(p, name) })
}
