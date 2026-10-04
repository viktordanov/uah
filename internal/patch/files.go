package patch

import (
	"io/fs"
	"os"
	"path/filepath"
)

// files is where a patch reads and writes its files, by their absolute
// paths.
type files interface {
	// key is where path is read and written: its target when the files
	// are confined, so two spellings of one target are one file.
	key(path string) string
	stat(path string) (fs.FileInfo, error)
	lstat(path string) (fs.FileInfo, error)
	readFile(path string) ([]byte, error)
	// mkdirParents creates the missing directories path is in.
	mkdirParents(path string) error
	// writeFile writes a file, creating it with perm when it is missing.
	writeFile(path string, data []byte, perm fs.FileMode) error
	remove(path string) error
}

// hostFiles is the file system as the os package sees it, symlinks
// followed.
type hostFiles struct{}

func (hostFiles) key(path string) string                 { return filepath.Clean(path) }
func (hostFiles) stat(path string) (fs.FileInfo, error)  { return os.Stat(path) }
func (hostFiles) lstat(path string) (fs.FileInfo, error) { return os.Lstat(path) }
func (hostFiles) readFile(path string) ([]byte, error)   { return os.ReadFile(path) }
func (hostFiles) mkdirParents(path string) error         { return os.MkdirAll(filepath.Dir(path), 0o755) }
func (hostFiles) remove(path string) error               { return os.Remove(path) }

func (hostFiles) writeFile(path string, data []byte, perm fs.FileMode) error {
	return os.WriteFile(path, data, perm)
}

// Targets confine a patch to the paths it was approved for: each path the
// changes name (Change.Abs and MoveAbs) maps to the absolute path that was
// checked when the patch was approved, with its symlinks already resolved.
// Compute and Write read and write each file only at its target, and never
// follow a symlink in it, neither in its directories nor in the file
// itself: one that a command put in since fails the patch, so a write
// cannot be led out of the directories that were checked. A path without
// a target fails too.
type Targets map[string]string

// Compute is Compute with the files read at their targets.
func (t Targets) Compute(cwd string, hunks []Hunk) ([]Change, error) {
	return compute(confinedFiles(t), cwd, hunks)
}

// Write applies the changes to the files at their targets, in order,
// creating missing parent directories. It is all or nothing: at the first
// failure it puts back every file the changes touch as it was before (a
// directory it created stays), and returns that failure.
func (t Targets) Write(changes []Change) error {
	return writeAll(confinedFiles(t), changes)
}

func (t confinedFiles) key(path string) string {
	if target, ok := t[path]; ok {
		return filepath.Clean(target)
	}

	return filepath.Clean(path)
}
