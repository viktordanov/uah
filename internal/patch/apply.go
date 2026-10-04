// Adapted from openai/codex rust-v0.156.1 (Apache License 2.0, Copyright
// 2025 OpenAI): codex-rs/apply-patch/src/lib.rs (apply_hunks_to_files and
// print_summary).

package patch

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
)

// Change is what one hunk does, computed against the files before it is
// written: an added file's content, a deleted file's old content, or an
// update's old and new content.
type Change struct {
	Op Op
	// Path and MovePath are as the patch names them; Abs and MoveAbs are
	// resolved against the working directory.
	Path, MovePath string
	Abs, MoveAbs   string
	Old, New       string
}

// Resolve makes a patch path absolute against cwd.
func Resolve(cwd, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}

	return filepath.Join(cwd, path)
}

// Paths are the absolute paths the hunks write: each file, and each move's
// destination.
func Paths(cwd string, hunks []Hunk) []string {
	var out []string
	for _, h := range hunks {
		out = append(out, Resolve(cwd, h.Path))
		if h.MovePath != "" {
			out = append(out, Resolve(cwd, h.MovePath))
		}
	}

	return out
}

// Compute works out every change without writing anything, reading the
// files as the earlier hunks of the same patch leave them. Its errors are
// Codex's messages.
func Compute(cwd string, hunks []Hunk) ([]Change, error) {
	return compute(hostFiles{}, cwd, hunks)
}

func compute(fsys files, cwd string, hunks []Hunk) ([]Change, error) {
	if len(hunks) == 0 {
		return nil, errors.New("No files were modified.") //nolint:staticcheck // Codex's message
	}
	files := overlay{fsys: fsys, files: map[string]*string{}}
	out := make([]Change, 0, len(hunks))
	for _, h := range hunks {
		c := Change{Op: h.Op, Path: h.Path, Abs: Resolve(cwd, h.Path)}
		switch h.Op {
		case Add:
			c.New = h.Contents
			files.set(c.Abs, c.New)
		case Delete:
			old, err := files.read(c.Abs)
			if err != nil {
				return nil, fmt.Errorf("Failed to delete file %s: %w", c.Abs, err) //nolint:staticcheck // Codex's message
			}
			c.Old = old
			files.remove(c.Abs)
		case Update:
			old, err := files.read(c.Abs)
			if err != nil {
				return nil, fmt.Errorf("Failed to read file to update %s: %w", c.Abs, err) //nolint:staticcheck // Codex's message
			}
			c.New, err = updated(old, c.Abs, h.Chunks)
			if err != nil {
				return nil, err
			}
			c.Old = old
			if h.MovePath != "" {
				c.MovePath, c.MoveAbs = h.MovePath, Resolve(cwd, h.MovePath)
				files.remove(c.Abs)
				files.set(c.MoveAbs, c.New)
			} else {
				files.set(c.Abs, c.New)
			}
		}
		out = append(out, c)
	}

	return out, nil
}

// overlay is the files as the patch has changed them so far, over fsys; a
// nil entry is a deleted file.
type overlay struct {
	fsys  files
	files map[string]*string
}

func (o overlay) set(path, text string) { o.files[path] = &text }
func (o overlay) remove(path string)    { o.files[path] = nil }

func (o overlay) read(path string) (string, error) {
	if text, ok := o.files[path]; ok {
		if text == nil {
			return "", fs.ErrNotExist
		}

		return *text, nil
	}
	info, err := o.fsys.stat(path)
	if err != nil {
		return "", err // the caller names the file
	}
	if info.IsDir() {
		return "", errors.New("it is a directory")
	}
	data, err := o.fsys.readFile(path)

	return string(data), err // the caller names the file
}

// writeAll applies the changes to the files in order, creating missing
// parent directories. It is all or nothing: at the first failure it puts
// back every file the changes touch as it was before (a directory it
// created stays), and returns that failure.
func writeAll(fsys files, changes []Change) error {
	before, err := snapshot(fsys, changes)
	if err != nil {
		return err
	}
	for _, c := range changes {
		if err := write(fsys, c); err != nil {
			return errors.Join(err, restore(fsys, before))
		}
	}

	return nil
}

// original is a file as it was before Write: its content and mode, or
// absent when there was no file.
type original struct {
	path   string
	absent bool
	text   string
	perm   fs.FileMode
}

// snapshot reads every file the changes touch, each once.
func snapshot(fsys files, changes []Change) ([]original, error) {
	var out []original
	seen := map[string]bool{}
	for _, c := range changes {
		for _, path := range []string{c.Abs, c.MoveAbs} {
			if path == "" || seen[path] {
				continue
			}
			seen[path] = true
			info, err := fsys.stat(path)
			if err != nil {
				// No file to keep; writing there reports why.
				out = append(out, original{path: path, absent: true})

				continue
			}
			if !info.Mode().IsRegular() {
				continue // a directory: writing there fails and changes nothing
			}
			data, err := fsys.readFile(path)
			if err != nil {
				return nil, fmt.Errorf("Failed to read file %s: %w", path, err) //nolint:staticcheck // Codex's style
			}
			out = append(out, original{path: path, text: string(data), perm: info.Mode().Perm()})
		}
	}

	return out, nil
}

// restore puts the files back as snapshot found them.
func restore(fsys files, before []original) error {
	var errs []error
	for _, o := range before {
		if o.absent {
			if _, err := fsys.lstat(o.path); err != nil {
				continue // never written
			}
			if err := fsys.remove(o.path); err != nil {
				errs = append(errs, fmt.Errorf("failed to remove %s while undoing the patch: %w", o.path, err))
			}

			continue
		}
		if err := writeFile(fsys, o.path, o.text, o.perm); err != nil {
			errs = append(errs, fmt.Errorf("failed to restore %s while undoing the patch: %w", o.path, err))
		}
	}

	return errors.Join(errs...)
}

func write(fsys files, c Change) error {
	switch c.Op {
	case Add:
		return writeFile(fsys, c.Abs, c.New, 0o644)
	case Delete:
		if err := fsys.remove(c.Abs); err != nil {
			return fmt.Errorf("Failed to delete file %s: %w", c.Abs, err) //nolint:staticcheck // Codex's message
		}
	case Update:
		perm := fs.FileMode(0o644)
		if info, err := fsys.stat(c.Abs); err == nil {
			perm = info.Mode().Perm()
		}
		if c.MoveAbs == "" {
			return writeFile(fsys, c.Abs, c.New, perm)
		}
		if err := writeFile(fsys, c.MoveAbs, c.New, perm); err != nil {
			return err
		}
		if err := fsys.remove(c.Abs); err != nil {
			return fmt.Errorf("Failed to remove original %s: %w", c.Abs, err) //nolint:staticcheck // Codex's message
		}
	}

	return nil
}

func writeFile(fsys files, path, text string, perm fs.FileMode) error {
	if err := fsys.mkdirParents(path); err != nil {
		return fmt.Errorf("Failed to create parent directories for %s: %w", path, err) //nolint:staticcheck // Codex's message
	}
	if err := fsys.writeFile(path, []byte(text), perm); err != nil {
		return fmt.Errorf("Failed to write file %s: %w", path, err) //nolint:staticcheck // Codex's message
	}

	return nil
}

// Summary is Codex's output for an applied patch: the files added, then
// modified (by their destination when moved), then deleted.
func Summary(changes []Change) string {
	var added, modified, deleted []string
	for _, c := range changes {
		switch c.Op {
		case Add:
			added = append(added, c.Path)
		case Update:
			modified = append(modified, cmp.Or(c.MovePath, c.Path))
		case Delete:
			deleted = append(deleted, c.Path)
		}
	}
	var b strings.Builder
	b.WriteString("Success. Updated the following files:\n")
	for _, group := range []struct {
		mark  string
		paths []string
	}{{"A", added}, {"M", modified}, {"D", deleted}} {
		for _, p := range group.paths {
			fmt.Fprintf(&b, "%s %s\n", group.mark, p)
		}
	}

	return b.String()
}
