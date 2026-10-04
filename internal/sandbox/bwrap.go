// Adapted from openai/codex rust-v0.156.1 (Apache-2.0):
// codex-rs/linux-sandbox/src/bwrap.rs, create_bwrap_flags and
// create_filesystem_args.

package sandbox

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// BwrapArgs returns the bubblewrap arguments that come before "--" for the
// policy. The layout follows Codex:
//
//	--new-session --die-with-parent
//	--ro-bind / / --dev /dev                   the whole disk read-only, a minimal /dev
//	--bind R R                                 each existing writable root, shallowest first
//	  --bind D D                               each directory between R and a ReadOnly path in it
//	  --ro-bind P P                            each existing protected path in R, and ReadOnly path
//	--ro-bind G G                              a worktree's gitdir inside a writable root, after every bind
//	--unshare-user --unshare-pid --unshare-ipc
//	--unshare-net                              unless the policy has network
//	--proc /proc --cap-drop ALL
//
// ReadOnly, and FullAccess if asked, have no writable roots.
//
// Unlike Codex, a protected name that does not exist yet, such as .git in a
// workspace that is not a repository root, stays unprotected. bwrap can only
// mount on an existing path, and Codex creates an empty directory on the host
// for it: an empty .git breaks git in a subdirectory of a repository, and
// removing those directories while parallel commands run would drop another
// sandbox's mount. Seatbelt on macOS protects missing names without this.
func BwrapArgs(p Policy) []string {
	return bwrapLayout(p, true)
}

// bwrapLayout builds BwrapArgs. Without mountProc it leaves out "--proc
// /proc", Codex's fallback for containers that forbid mounting procfs.
func bwrapLayout(p Policy, mountProc bool) []string {
	args := []string{
		"--new-session",
		"--die-with-parent",
		"--ro-bind", "/", "/",
		"--dev", "/dev",
	}
	var layouts []rootLayout
	for _, r := range p.layout().roots {
		// bwrap needs every bind source to exist; Codex skips missing roots.
		if exists(r.root) {
			layouts = append(layouts, r)
		}
	}
	slices.SortStableFunc(layouts, func(a, b rootLayout) int { return byDepth(a.root, b.root) })
	var roots []string
	for _, r := range layouts {
		roots = append(roots, r.root)
	}
	// A protected path outside its own root, such as a worktree's gitdir,
	// goes after every bind so a later root bind cannot cover it.
	var later []string
	for _, r := range layouts {
		root := r.root
		args = append(args, "--bind", root, root)
		protected := slices.Clone(r.protected)
		slices.SortStableFunc(protected, byDepth)
		// A directory between the root and a ReadOnly path is bound over
		// itself first: a mount point cannot be renamed (EBUSY), so a
		// command cannot move the protected path away and put its own in
		// its place. Each comes before every read-only bind, which it
		// would otherwise cover.
		for _, dir := range readOnlyAncestors(root, r.readOnly) {
			args = append(args, "--bind", dir, dir)
		}
		for _, path := range protected {
			if under(path, []string{root}) {
				args = protect(args, path)
			} else if under(path, roots) && !slices.Contains(later, path) {
				// Outside every root it is already read-only.
				later = append(later, path)
			}
		}
	}
	slices.SortStableFunc(later, byDepth)
	for _, path := range later {
		args = protect(args, path)
	}
	args = append(args, "--unshare-user", "--unshare-pid", "--unshare-ipc")
	if !p.Network {
		args = append(args, "--unshare-net")
	}
	if mountProc {
		args = append(args, "--proc", "/proc")
	}
	args = append(args, "--cap-drop", "ALL")

	return args
}

// readOnlyAncestors returns the existing directories strictly between root
// and each of paths, shallowest first and without duplicates.
func readOnlyAncestors(root string, paths []string) []string {
	var out []string
	for _, path := range paths {
		for dir := filepath.Dir(path); dir != root && under(dir, []string{root}); dir = filepath.Dir(dir) {
			if exists(dir) && !slices.Contains(out, dir) {
				out = append(out, dir)
			}
		}
	}
	slices.SortStableFunc(out, byDepth)

	return out
}

// protect binds an existing path read-only over itself inside a writable
// bind; a missing path is left alone (see BwrapArgs).
func protect(args []string, path string) []string {
	if exists(path) {
		args = append(args, "--ro-bind", path, path)
	}

	return args
}

func exists(path string) bool {
	_, err := os.Lstat(path)

	return err == nil
}

// under reports whether path is inside one of roots.
func under(path string, roots []string) bool {
	for _, r := range roots {
		if rel, err := filepath.Rel(r, path); err == nil && rel != ".." && !strings.HasPrefix(rel, "../") {
			return true
		}
	}

	return false
}

func byDepth(a, b string) int {
	return strings.Count(filepath.Clean(a), "/") - strings.Count(filepath.Clean(b), "/")
}
