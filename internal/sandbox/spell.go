package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Every path that reaches a policy's decisions, as a writable root, a
// protected path, a ReadOnly path, a grant, or a path checked against
// them, is spelled once here, so two spellings of one directory, such as
// a case alias or another Unicode normalization on macOS, or a path
// through a symlink, are one string everywhere uah compares paths as
// strings: the roots' exclusions in Seatbelt's profile, the binds of
// bubblewrap, and CanWrite.

// Canonical returns path absolute, with its symlinks resolved
// (ResolvePath), and each existing name as its directory lists it; names
// past the deepest existing directory are kept as given. ok is false when
// a directory on the way cannot be listed, or does not list a name it
// holds under any spelling: the stored spelling is then unknown, and a
// caller refuses the path.
func Canonical(path string) (string, bool) {
	return spell(ResolvePath(path))
}

// spell is Canonical for a path whose symlinks are already resolved, or
// must not be followed: each existing name is spelled as listed, a symlink
// itself included, without following it.
func spell(path string) (string, bool) {
	path = filepath.Clean(path)
	if !filepath.IsAbs(path) {
		return path, false
	}

	return spellUnder(string(filepath.Separator), strings.TrimPrefix(path, string(filepath.Separator)))
}

// spellUnder spells rel under dir, which is spelled already: a protected
// path inside a root spelled by Canonical needs only its own names.
func spellUnder(dir, rel string) (string, bool) {
	out := dir
	names := strings.Split(rel, string(filepath.Separator))
	for i, name := range names {
		if name == "" {
			continue
		}
		if _, err := os.Lstat(filepath.Join(out, name)); err != nil {
			// The rest does not exist yet: nothing lists it.
			return filepath.Join(append([]string{out}, names[i:]...)...), true
		}
		stored, ok := storedName(out, name)
		if !ok {
			return filepath.Join(dir, rel), false
		}
		out = filepath.Join(out, stored)
	}

	return out, true
}

// spelled is the cache of storedName: a directory's listing of one name,
// kept while the directory is the same directory with the same
// modification time, which a rename or a new entry in it changes.
var spelled sync.Map // spellKey -> spellEntry

type spellKey struct{ dir, name string }

type spellEntry struct {
	dir    os.FileInfo
	mod    time.Time
	stored string
}

// storedName is the name dir lists for name, which exists in it: name
// itself, else the entry that is the same file.
func storedName(dir, name string) (string, bool) {
	info, err := os.Lstat(dir)
	if err != nil {
		return "", false
	}
	key := spellKey{dir, name}
	if v, ok := spelled.Load(key); ok {
		e := v.(spellEntry) //nolint:forcetypeassert // the map holds only spellEntry
		if os.SameFile(e.dir, info) && e.mod.Equal(info.ModTime()) {
			return e.stored, true
		}
	}
	stored, ok := listedName(dir, name)
	if ok {
		spelled.Store(key, spellEntry{dir: info, mod: info.ModTime(), stored: stored})
	}

	return stored, ok
}

func listedName(dir, name string) (string, bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	for _, e := range entries {
		if e.Name() == name {
			return name, true
		}
	}
	want, err := os.Lstat(filepath.Join(dir, name))
	if err != nil {
		return "", false
	}
	for _, e := range entries {
		if info, err := os.Lstat(filepath.Join(dir, e.Name())); err == nil && os.SameFile(info, want) {
			return e.Name(), true
		}
	}

	return "", false
}
