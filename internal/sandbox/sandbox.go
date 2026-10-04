// Package sandbox runs shell commands inside the operating system's sandbox,
// as Codex does: Seatbelt (sandbox-exec) on macOS and bubblewrap on Linux.
// A Policy says what a command may write and whether it has network; Wrap
// turns a command line into one that runs under the policy, and Shell writes
// a script the runner can use as its shell. Denied recognizes a command the
// sandbox blocked, and EnvPolicy is Codex's shell_environment_policy.
package sandbox

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Mode is how much a sandboxed command may do. The names match Codex's
// sandbox_mode.
type Mode string

const (
	// ReadOnly commands can read the whole disk and write only the
	// policy's TempDir.
	ReadOnly Mode = "read-only"
	// WorkspaceWrite commands can also write the workspace, the extra
	// writable roots, /tmp, and $TMPDIR, except the protected paths.
	WorkspaceWrite Mode = "workspace-write"
	// FullAccess runs commands without a sandbox.
	FullAccess Mode = "danger-full-access"
)

// Modes are the valid modes.
var Modes = []Mode{ReadOnly, WorkspaceWrite, FullAccess}

// ParseMode checks a mode name.
func ParseMode(s string) (Mode, error) {
	if m := Mode(s); slices.Contains(Modes, m) {
		return m, nil
	}

	return "", fmt.Errorf("invalid sandbox mode %q (want read-only, workspace-write, or danger-full-access)", s)
}

// ErrUnavailable means this platform has no usable sandbox, such as Linux
// without bwrap.
var ErrUnavailable = errors.New("no sandbox is available on this system")

// ProtectedNames stay read-only inside every writable root: a sandboxed
// command could otherwise plant code that runs later outside the sandbox,
// such as a git hook. .uagent, the project directory uah read before .uah,
// stays protected until the user moves it.
var ProtectedNames = []string{".git", ".uah", ".uagent", ".agents", ".codex"}

// Policy is what a sandboxed command may do.
type Policy struct {
	Mode Mode
	// Workspace is the absolute workspace directory.
	Workspace string
	// WritableRoots are extra absolute directories writable in WorkspaceWrite.
	WritableRoots []string
	// Network allows network access; Codex's network_access.
	Network bool
	// TempDir is the session's private temporary directory, or "": writable
	// in ReadOnly and WorkspaceWrite, and TMPDIR, TMP, and TEMP for every
	// command Shell runs, in every mode, so $TMPDIR names the same directory
	// inside and outside the sandbox.
	TempDir string
	// ReadOnly are more absolute paths that stay read-only inside every
	// writable root, as the protected names do, and whose directories
	// between the root and the path cannot be renamed: the directory of
	// the sandboxing scripts, which run outside the sandbox. Shell adds
	// its own directory.
	ReadOnly []string
}

// Writable returns the directories a command may write under the policy,
// spelled by Canonical: the TempDir in ReadOnly; the workspace, the
// writable roots, /tmp, uah's own $TMPDIR, and the TempDir in
// WorkspaceWrite; none in FullAccess, which has no sandbox. A root inside
// one of the ReadOnly paths is left out, so it cannot open up part of that
// path again, and so is a root that it or one of its protected paths
// cannot spell: its spelling, and so what the sandbox compares it with,
// is unknown (layout).
func (p Policy) Writable() []string {
	var out []string
	for _, r := range p.layout().roots {
		out = append(out, r.root)
	}

	return out
}

// candidates are the roots the mode names, as given.
func (p Policy) candidates() []string {
	switch p.Mode {
	case ReadOnly:
		return []string{p.TempDir}
	case WorkspaceWrite:
		roots := append([]string{p.Workspace}, p.WritableRoots...)
		roots = append(roots, "/tmp")
		if tmp := os.Getenv("TMPDIR"); tmp != "" {
			roots = append(roots, tmp)
		}

		return append(roots, p.TempDir)
	case FullAccess:
	}

	return nil
}

// layout is one snapshot of a policy, every path in it spelled by one
// speller: the writable roots with what stays read-only inside each, and
// the ReadOnly paths. Each decision reads one snapshot, so a root is never
// accepted on one spelling and checked on another, and a path that fails
// to spell anywhere in it fails the whole decision closed.
type layout struct {
	sp       *speller
	roots    []rootLayout
	readOnly []string
}

// rootLayout is a writable root, the paths that stay read-only inside it
// or because of it (protected, ReadOnly paths spelled under it included),
// and its ReadOnly paths alone, which bubblewrap binds specially.
type rootLayout struct {
	root      string
	protected []string
	readOnly  []string
}

// layout spells the policy. A ReadOnly path that does not spell might be
// anywhere, so the layout then has no roots at all; a root that does not
// spell, or whose protected paths do not, is left out.
func (p Policy) layout() layout {
	l := layout{sp: newSpeller()}
	for _, ro := range p.ReadOnly {
		if ro == "" {
			continue
		}
		spelled, ok := l.sp.canonical(ro)
		if !ok {
			return layout{sp: l.sp}
		}
		l.readOnly = append(l.readOnly, spelled)
	}
	for _, r := range p.candidates() {
		if r == "" {
			continue
		}
		root, ok := l.sp.canonical(r)
		if !ok || slices.ContainsFunc(l.roots, func(have rootLayout) bool { return have.root == root }) || l.insideReadOnly(root) {
			continue
		}
		if rl, ok := l.rootLayout(root); ok {
			l.roots = append(l.roots, rl)
		}
	}

	return l
}

// insideReadOnly reports whether path is one of the ReadOnly paths or
// inside one, also under another name for it (protects).
func (l layout) insideReadOnly(path string) bool {
	return slices.ContainsFunc(l.readOnly, func(ro string) bool { return protects(ro, path) })
}

// rootLayout is what stays read-only in root: each of ProtectedNames,
// whether or not it exists yet, and, when it is a symlink, where it leads,
// even if nothing is there yet; the directory a .git file points to (a
// worktree's "gitdir:"); and the ReadOnly paths at or under root, spelled
// under root: a root that names one of the path's directories in another
// case, or by another name for the same directory, still holds the path.
// ok is false when one of them does not spell.
func (l layout) rootLayout(root string) (rootLayout, bool) {
	rl := rootLayout{root: root}
	add := func(path string) {
		if !slices.Contains(rl.protected, path) {
			rl.protected = append(rl.protected, path)
		}
	}
	for _, name := range ProtectedNames {
		at, ok := l.sp.under(root, name)
		if !ok {
			return rootLayout{}, false
		}
		add(at)
		if info, err := os.Lstat(at); err == nil && info.Mode()&os.ModeSymlink != 0 {
			target, ok := l.sp.canonical(at)
			if !ok {
				return rootLayout{}, false
			}
			add(target)
		}
	}
	if target := gitdirTarget(filepath.Join(root, ".git")); target != "" {
		spelled, ok := l.sp.canonical(target)
		if !ok {
			return rootLayout{}, false
		}
		add(spelled)
	}
	for _, ro := range l.readOnly {
		if path, ok := underRoot(ro, root); ok && !slices.Contains(rl.readOnly, path) {
			rl.readOnly = append(rl.readOnly, path)
			add(path)
		}
	}

	return rl, true
}

// Protected returns the paths inside root that stay read-only: each of
// ProtectedNames, whether or not it exists yet, plus the directory a .git
// file points to (a worktree's "gitdir:").
func Protected(root string) []string {
	var out []string
	for _, name := range ProtectedNames {
		out = append(out, filepath.Join(root, name))
	}
	if target := gitdirTarget(filepath.Join(root, ".git")); target != "" {
		out = append(out, target)
	}

	return out
}

// underRoot returns path spelled under root when path is root or inside
// it, lexically, without case, or because one of its directories is root
// under another name.
func underRoot(path, root string) (string, bool) {
	if within(path, root) {
		return path, true
	}
	info, err := os.Stat(root)
	for dir := path; ; dir = filepath.Dir(dir) {
		same := strings.EqualFold(dir, root)
		if !same && err == nil {
			d, derr := os.Stat(dir)
			same = derr == nil && os.SameFile(d, info)
		}
		if same {
			rel, rerr := filepath.Rel(dir, path)

			return filepath.Join(root, rel), rerr == nil
		}
		if filepath.Dir(dir) == dir {
			return "", false
		}
	}
}

// Wrap returns argv run inside the sandbox on this platform. FullAccess
// returns argv unchanged. It returns ErrUnavailable when the platform has no
// sandbox.
func (p Policy) Wrap(argv []string) ([]string, error) {
	if p.Mode == FullAccess {
		return argv, nil
	}
	if !filepath.IsAbs(p.Workspace) {
		return nil, fmt.Errorf("the sandbox workspace %q is not absolute", p.Workspace)
	}

	return wrap(p, argv)
}
