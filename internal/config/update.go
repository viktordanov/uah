package config

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/BurntSushi/toml"

	"github.com/viktordanov/uah/internal/config/tomledit"
)

// AddNewSettings is `uah config update`: it appends to the user file at
// path, as TOML comments, each new setting that the file neither sets nor
// has the whole block of, and returns them. Only comments go in, so the
// file means to any uah, an older one too, what it meant before. It opens
// the file once, through a symlink to its target, refuses anything but a
// regular file (opened without blocking, so a FIFO cannot hold it), and
// holds a lock on it from the read to the append, so updates that overlap
// add a block once. It appends and rewrites nothing: a save another
// program makes meanwhile stays, the file keeps its permissions, and a
// reader that comes in between reads comments up to a line's end. A
// missing file is created with 0600. ref is where the blocks link the
// reference: this uah's release tag or commit. With dry, it writes nothing.
func AddNewSettings(path, ref string, dry bool) ([]NewSetting, error) {
	if err := CheckUserFilePath(path); err != nil {
		return nil, err
	}
	var f *os.File
	for try := 0; ; try++ {
		added, first, err := readMissing(path, ref) // a file that needs nothing is only read
		if err != nil || len(added) == 0 || dry {
			return added, err
		}
		f, err = openToAppend(path, first)
		if err == nil {
			break
		}
		if first != nil || !errors.Is(err, fs.ErrExist) || try == 2 {
			return nil, err
		}
		// another run made the file meanwhile: read it again
	}
	defer f.Close()
	data, err := readUserFile(f, path)
	if err != nil {
		return nil, err
	}
	added, out, err := missing(path, ref, data) // again, under the lock: another run may have added them
	if err != nil || len(added) == 0 {
		return added, err
	}
	if _, err := f.Write(out); err != nil {
		return nil, fmt.Errorf("failed to add to %s: %w", path, err)
	}
	opened, err := f.Stat()
	if err == nil {
		err = f.Close()
	}
	if err != nil {
		return nil, fmt.Errorf("failed to add to %s: %w", path, err)
	}
	// An editor that saved by a rename meanwhile left the block in the old file.
	if now, err := os.Stat(path); err != nil || !os.SameFile(opened, now) {
		return nil, fmt.Errorf("%s was replaced while uah config update added to it; run it again", path)
	}

	return added, nil
}

// CheckUserFilePath refuses a path `uah config update` would not write
// to, before anything is read or made: one that names a folder, itself or
// where its symlinks lead, or that goes back out of a folder that does not
// exist, which only the write would make. The new settings notice offers
// the command only for a path it accepts.
func CheckUserFilePath(path string) error {
	end, err := linkTarget(path)
	if err != nil {
		return err
	}
	for _, p := range []string{path, end} {
		if last := p[strings.LastIndex(p, string(filepath.Separator))+1:]; last == "" || last == "." || last == ".." {
			return fmt.Errorf("%q names a folder, not a file", p)
		}
	}

	return pathThroughMissing(path, 40)
}

// readMissing reads the user file, opened to read only, and returns what
// missing does, and the file it read: nil when there was none.
func readMissing(path, ref string) ([]NewSetting, os.FileInfo, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if errors.Is(err, fs.ErrNotExist) {
		added, _, err := missing(path, ref, nil)

		return added, nil, err
	}
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	info, err := regular(f, path)
	if err != nil {
		return nil, nil, err
	}
	data, err := readUserFile(f, path)
	if err != nil {
		return nil, nil, err
	}
	added, _, err := missing(path, ref, data)

	return added, info, err
}

// maxUserFile bounds what uah config update reads: a configuration is far
// smaller, and a path to a disk image must not fill the memory.
const maxUserFile = 16 << 20

// readUserFile reads an open user file, up to maxUserFile.
func readUserFile(f *os.File, path string) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(f, maxUserFile+1))
	switch {
	case err != nil:
		return nil, fmt.Errorf("failed to read %s: %w", path, err)
	case len(data) > maxUserFile:
		return nil, fmt.Errorf("%s is over %d MiB, too large for a configuration, so it is left as it is", path, maxUserFile>>20)
	}

	return data, nil
}

// regular refuses an open file that is not a regular file.
func regular(f *os.File, path string) (os.FileInfo, error) {
	info, err := f.Stat()
	if err == nil && !info.Mode().IsRegular() {
		err = fmt.Errorf("%s is not a regular file, so it is left as it is", path)
	}

	return info, err
}

// missing returns the new settings the file's data neither sets nor has
// the whole block of, and their blocks, to append to it.
func missing(path, ref string, data []byte) ([]NewSetting, []byte, error) {
	var doc map[string]any
	if _, err := toml.Decode(string(data), &doc); err != nil {
		return nil, nil, fmt.Errorf("%s is not valid TOML, so it is left as it is: %w", path, err)
	}
	var added []NewSetting
	var out []byte
	for _, n := range NewSettings {
		if hasKey(doc, n.Key) {
			continue
		}
		has, err := hasBlock(data, n)
		if err != nil {
			return nil, nil, fmt.Errorf("%s could not be read for its update blocks, so it is left as it is: %w", path, err)
		}
		if has {
			continue
		}
		if len(data)+len(out) > 0 {
			if len(out) == 0 && !bytes.HasSuffix(data, []byte("\n")) {
				out = append(out, '\n')
			}
			out = append(out, '\n')
		}
		out = append(out, n.Comment(ref)...)
		added = append(added, n)
	}

	if len(data)+len(out) > maxUserFile {
		return nil, nil, fmt.Errorf("%s would be over %d MiB with the blocks added, too large for a configuration, so it is left as it is", path, maxUserFile>>20)
	}

	return added, out, nil
}

// openToAppend opens the user file to append to, under an exclusive lock
// that closing it releases: the file first, the one read before, or, when
// there was none, a new one, made only where nothing is yet. A file that
// changed since, replaced or removed, is refused: run it again.
func openToAppend(path string, first os.FileInfo) (*os.File, error) {
	flags, open := os.O_RDWR|os.O_APPEND, path
	if first == nil {
		if err := makeParent(path); err != nil {
			return nil, err
		}
		end, err := linkTarget(path) // O_EXCL refuses a link, even one to nothing: make the file it names
		if err != nil {
			return nil, err
		}
		flags, open = flags|os.O_CREATE|os.O_EXCL, end
	}
	f, err := os.OpenFile(open, flags|syscall.O_NONBLOCK, 0o600)
	if err != nil {
		return nil, fmt.Errorf("%s changed while uah config update read it, so it is left as it is; run it again: %w", path, err) // %w keeps fs.ErrExist for a file another run made
	}
	info, err := regular(f, path)
	if err == nil && first != nil && !os.SameFile(info, first) {
		err = fmt.Errorf("%s was replaced while uah config update read it; run it again", path)
	}
	if err == nil {
		err = flockWait(f)
	}
	if err != nil {
		_ = f.Close()

		return nil, err
	}

	return f, nil
}

// makeParent makes the folder the user file goes in: the link's target's,
// for a symlink to a file not made yet, else path's own, taken as written,
// not with filepath.Dir, as a .. after a symlink is the link's.
func makeParent(path string) error {
	path, err := linkTarget(path)
	if err != nil {
		return err
	}
	if dir := parentOf(path); dir != "" {
		return mkdirThroughLinks(dir, 40)
	}

	return nil
}

// pathThroughMissing is dotDotThroughMissing for path and for where its
// dangling symlinks lead, a folder's on the way too, so a dry run and the
// write see the same file: the first part of path that does not exist, if
// it is a link, is followed.
func pathThroughMissing(path string, hops int) error {
	if err := dotDotThroughMissing(path); err != nil || hops == 0 {
		return err
	}
	for p := path; p != ""; p = parentOf(p) {
		if _, err := os.Stat(p); err == nil {
			return nil // the rest resolves
		}
		if info, err := os.Lstat(p); err == nil && info.Mode()&fs.ModeSymlink != 0 {
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			if dir := parentOf(p); dir != "" && !filepath.IsAbs(target) {
				target = dir + string(filepath.Separator) + target
			}

			return pathThroughMissing(target+path[len(p):], hops-1)
		}
		if p == string(filepath.Separator) {
			break
		}
	}

	return nil
}

// dotDotThroughMissing refuses a path whose ".." follows a folder that
// does not exist: the write would make that folder and come back out of
// it, so a dry run, which makes nothing, could not see the same file.
func dotDotThroughMissing(path string) error {
	sep := string(filepath.Separator)
	parts := strings.Split(path, sep)
	for i, p := range parts {
		if p != ".." || i == 0 {
			continue
		}
		prefix := cmp.Or(strings.Join(parts[:i], sep), sep)
		if _, err := os.Stat(prefix); errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("%q goes back out of %s, which does not exist", path, prefix)
		}
	}

	return nil
}

// linkTarget is where a chain of symlinks at path ends, as written: path
// itself when it is no link.
func linkTarget(path string) (string, error) {
	for range 40 { // a chain of links, as the kernel follows one
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&fs.ModeSymlink == 0 {
			return path, nil
		}
		target, err := os.Readlink(path)
		if err != nil {
			return "", err
		}
		if dir := parentOf(path); dir != "" && !filepath.IsAbs(target) {
			target = dir + string(filepath.Separator) + target // not joined: Join would clean a link's ..
		}
		path = target
	}

	return path, nil
}

// mkdirThroughLinks makes dir and the folders above it, as MkdirAll does,
// also where one of them is a symlink to a folder not made yet: it makes
// the folder the link leads to, and the link stays.
func mkdirThroughLinks(dir string, hops int) error {
	if info, err := os.Stat(dir); err == nil {
		if !info.IsDir() {
			return fmt.Errorf("%s is not a folder", dir)
		}

		return nil
	}
	if info, err := os.Lstat(dir); err == nil && info.Mode()&fs.ModeSymlink != 0 && hops > 0 {
		target, err := os.Readlink(dir)
		if err != nil {
			return err
		}
		if parent := parentOf(dir); parent != "" && !filepath.IsAbs(target) {
			target = parent + string(filepath.Separator) + target
		}

		return mkdirThroughLinks(target, hops-1)
	}
	if parent := parentOf(dir); parent != "" {
		if err := mkdirThroughLinks(parent, hops); err != nil {
			return err
		}
	}
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}

	return nil
}

// parentOf is path without its last element, as written: the root for a
// file in it, "" for a bare name.
func parentOf(path string) string {
	switch i := strings.LastIndex(path, string(filepath.Separator)); {
	case i > 0:
		return path[:i]
	case i == 0:
		return string(filepath.Separator)
	}

	return ""
}

// hasKey reports whether the decoded file sets the dotted key, matching
// each part in any case, as the loader does ([Advisor] sets advisor).
func hasKey(doc map[string]any, key string) bool {
	var node any = doc
	for part := range strings.SplitSeq(key, ".") {
		table, ok := node.(map[string]any)
		if !ok {
			return false
		}
		found := false
		for k, v := range table {
			if strings.EqualFold(k, part) {
				node, found = v, true

				break
			}
		}
		if !found {
			return false
		}
	}

	return true
}

// hasBlock reports whether data has the setting's whole block: its marker
// and, after it, its end marker, both as comments. An error says data could
// not be read for them, so nothing is added, rather than added again.
func hasBlock(data []byte, n NewSetting) (bool, error) {
	starts, err := tomledit.CommentLines(data, n.Marker())
	if err != nil || len(starts) == 0 {
		return false, err
	}
	ends, err := tomledit.CommentLines(data, n.EndMarker())

	return slices.ContainsFunc(ends, func(end int) bool { return end > starts[0] }), err
}
