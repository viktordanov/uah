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
// absolute and with symlinks resolved where they exist: the TempDir in
// ReadOnly; the workspace, the writable roots, /tmp, uah's own $TMPDIR, and
// the TempDir in WorkspaceWrite; none in FullAccess, which has no sandbox.
func (p Policy) Writable() []string {
	var roots []string
	switch p.Mode {
	case ReadOnly:
		roots = []string{p.TempDir}
	case WorkspaceWrite:
		roots = append([]string{p.Workspace}, p.WritableRoots...)
		roots = append(roots, "/tmp")
		if tmp := os.Getenv("TMPDIR"); tmp != "" {
			roots = append(roots, tmp)
		}
		roots = append(roots, p.TempDir)
	case FullAccess:
		return nil
	}
	var out []string
	for _, r := range roots {
		if r == "" {
			continue
		}
		if resolved, err := filepath.EvalSymlinks(r); err == nil {
			r = resolved
		}
		r = filepath.Clean(r)
		if !slices.Contains(out, r) {
			out = append(out, r)
		}
	}

	return out
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

// protectedIn returns the paths inside root that stay read-only: Protected
// and the policy's ReadOnly paths at or under root, resolved.
func (p Policy) protectedIn(root string) []string {
	out := Protected(root)
	for _, path := range p.readOnlyIn(root) {
		if !slices.Contains(out, path) {
			out = append(out, path)
		}
	}

	return out
}

// readOnlyIn returns the policy's ReadOnly paths at or under root, resolved.
func (p Policy) readOnlyIn(root string) []string {
	var out []string
	for _, path := range p.ReadOnly {
		if path == "" {
			continue
		}
		if path = ResolvePath(path); within(path, root) && !slices.Contains(out, path) {
			out = append(out, path)
		}
	}

	return out
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
