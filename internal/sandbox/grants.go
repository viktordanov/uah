package sandbox

import (
	"os"
	"path/filepath"
	"slices"
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

// Grants are a session's grants, shared with its subagents. Grants only
// grow while the session lives; nothing writes them to a configuration
// file. It is safe for concurrent use.
type Grants struct {
	// Worktrees finds the worktrees of the workspace's repository.
	Worktrees *Worktrees

	mu      sync.Mutex
	grants  []Grant
	version uint64
	notify  func(Grant)
}

// NewGrants returns no grants for the workspace. notify, when set, hears
// each new grant, on the goroutine that adds it.
func NewGrants(workspace string, notify func(Grant)) *Grants {
	return &Grants{Worktrees: NewWorktrees(workspace), notify: notify}
}

// Add makes the directory writable for the session and reports whether it
// is new. A path that is not an existing directory, is not resolved, or
// fails Grantable is refused; so is one a grant already covers.
func (g *Grants) Add(path string, reason GrantReason) bool {
	if g == nil || !Grantable(path) || resolveDir(path) != path {
		return false
	}
	g.mu.Lock()
	for _, have := range g.grants {
		if within(path, have.Path) {
			g.mu.Unlock()

			return false
		}
	}
	added := Grant{Path: path, Reason: reason}
	g.grants = append(g.grants, added)
	g.version++
	notify := g.notify
	g.mu.Unlock()
	if notify != nil {
		notify(added)
	}

	return true
}

// Keep adds a grant kept from before a resume when it is still Valid,
// without telling notify, and reports whether it did.
func (g *Grants) Keep(gr Grant) bool {
	if g == nil || !g.Valid(gr) {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if slices.Contains(g.grants, gr) {
		return false
	}
	g.grants = append(g.grants, gr)
	g.version++

	return true
}

// List returns the grants in the order they were made.
func (g *Grants) List() []Grant {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()

	return slices.Clone(g.grants)
}

// Roots returns the granted directories.
func (g *Grants) Roots() []string {
	var roots []string
	for _, gr := range g.List() {
		roots = append(roots, gr.Path)
	}

	return roots
}

// Version changes whenever a grant is added, so a sandboxing shell built
// for the earlier roots is built again.
func (g *Grants) Version() uint64 {
	if g == nil {
		return 0
	}
	g.mu.Lock()
	defer g.mu.Unlock()

	return g.version
}

// Valid reports whether a grant kept from before a resume still holds: a
// worktree grant that is still a worktree of the workspace's repository,
// rooted at the same directory, and still Grantable. An approved grant is
// never kept: it rests on the user's answer alone, which nothing on disk
// can prove, and a sidecar a sandboxed command could write must not widen
// what the session may write.
func (g *Grants) Valid(gr Grant) bool {
	if gr.Reason != GrantWorktree || !Grantable(gr.Path) || resolveDir(gr.Path) != gr.Path {
		return false
	}
	root, ok := g.Worktrees.Of(gr.Path)

	return ok && root == gr.Path
}

// GrantFor returns the directory an approval may offer to make writable for
// writes to paths (resolved, as ResolvePath returns them): the top of the
// git working tree that holds them all, else their nearest common existing
// directory; "" when that directory is not Grantable.
func GrantFor(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
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

	return dir
}

// Grantable reports whether a directory may be granted at all: an absolute
// path that is not the file system's root and neither is nor holds the
// user's home directory, so no grant opens the whole home. Names are
// compared without case, and a directory that is the home directory or
// one above it under another name counts too.
func Grantable(dir string) bool {
	if !filepath.IsAbs(dir) || filepath.Dir(dir) == dir {
		return false
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
