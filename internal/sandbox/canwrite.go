package sandbox

import (
	"os"
	"path/filepath"
	"strings"
)

// CanWrite reports whether the policy lets a sandboxed command write path,
// as Codex's can_write_path does for apply_patch: anything in FullAccess,
// and otherwise a path under a writable root (only the TempDir in ReadOnly)
// that is not a protected path. Symlinks in the path's existing part are
// resolved first, so a link cannot lead out of a root.
func (p Policy) CanWrite(path string) bool {
	return p.CanWriteResolved(ResolvePath(path))
}

// CanWriteResolved is CanWrite for a path ResolvePath returned: it takes
// path as it is, without following its symlinks again, for a caller that
// then writes that exact path without following symlinks.
func (p Policy) CanWriteResolved(path string) bool {
	if p.Mode == FullAccess {
		return true
	}
	roots := p.Writable()
	inside := false
	for _, r := range roots {
		if within(path, r) {
			inside = true
		}
		for _, protected := range p.protectedIn(r) {
			if protects(ResolvePath(protected), path) {
				return false
			}
		}
	}

	return inside
}

// Protects reports whether path is, or is inside, a path the policy keeps
// read-only: a protected path of one of its writable roots, or one of its
// ReadOnly paths, also under another name. A writable root there would
// open part of it again, as Seatbelt's rule for the inner root and
// bubblewrap's later bind would, so the engine grants no such directory.
func (p Policy) Protects(path string) bool {
	if p.insideReadOnly(path) {
		return true
	}
	for _, r := range p.Writable() {
		for _, protected := range p.protectedIn(r) {
			if protects(ResolvePath(protected), path) {
				return true
			}
		}
	}

	return false
}

// Holds reports whether dir is, or holds, one of the policy's writable
// roots, by name without case or by identity. A grant there would take in
// that root's protected paths under a name the sandbox may not compare
// them by, so the engine grants no such directory.
func (p Policy) Holds(dir string) bool {
	for _, r := range p.Writable() {
		if holds(dir, r) {
			return true
		}
	}

	return false
}

// InRoot reports whether path is one of the policy's writable roots or
// inside one, by name without case or by identity. A grant there adds
// nothing it may write, and under another spelling than the root's it
// could keep its protected paths out of the root's rule in Seatbelt, which
// compares names as strings, so the engine leaves it out.
func (p Policy) InRoot(path string) bool {
	for _, r := range p.Writable() {
		if holds(r, path) {
			return true
		}
	}

	return false
}

// protects reports whether the protected path covers path: path is it or
// inside it, its names compared without case, as macOS's default file
// system and Linux's casefold directories compare them; or path, or one of
// its existing directories, is the protected file itself under another
// name. A case alias such as .GIT or SANDBOX then cannot lead a write
// into a protected directory.
func protects(protected, path string) bool {
	if withinFold(path, protected) {
		return true
	}
	target, err := os.Stat(protected)
	if err != nil {
		return false
	}
	for dir := path; ; dir = filepath.Dir(dir) {
		if info, err := os.Stat(dir); err == nil && os.SameFile(info, target) {
			return true
		}
		if filepath.Dir(dir) == dir {
			return false
		}
	}
}

// withinFold is within with each name compared without case.
func withinFold(path, root string) bool {
	names := strings.Split(filepath.Clean(path), string(filepath.Separator))
	rootNames := strings.Split(filepath.Clean(root), string(filepath.Separator))
	if len(names) < len(rootNames) {
		return false
	}
	for i, name := range rootNames {
		if !strings.EqualFold(name, names[i]) {
			return false
		}
	}

	return true
}

// ResolvePath makes path absolute and resolves the symlinks of its deepest
// existing ancestor; a dangling link is followed to its target.
func ResolvePath(path string) string {
	return resolveDepth(path, 0)
}

func resolveDepth(path string, depth int) string {
	path, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	rest := ""
	for dir := path; ; {
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(resolved, rest)
		}
		if target, err := os.Readlink(dir); err == nil && depth < maxLinks {
			if !filepath.IsAbs(target) {
				target = filepath.Join(filepath.Dir(dir), target)
			}

			return resolveDepth(filepath.Join(target, rest), depth+1)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return path
		}
		rest = filepath.Join(filepath.Base(dir), rest)
		dir = parent
	}
}

// maxLinks bounds how many dangling links ResolvePath follows.
const maxLinks = 40

// within reports whether path is root or under it.
func within(path, root string) bool {
	rel, err := filepath.Rel(root, path)

	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
