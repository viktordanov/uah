package sandbox

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
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
	return newSpeller().canonical(path)
}

// speller spells paths for one decision. It remembers each directory's
// listing of a name for as long as it lives, so every path of one policy
// snapshot is spelled from one reading of each directory, and nothing is
// kept between decisions.
type speller struct {
	listed map[[2]string]string
}

func newSpeller() *speller { return &speller{listed: map[[2]string]string{}} }

// canonical is Canonical with this speller's readings.
func (s *speller) canonical(path string) (string, bool) {
	return s.spell(ResolvePath(path))
}

// spell is canonical for a path whose symlinks are already resolved, or
// must not be followed: each existing name is spelled as listed, a symlink
// itself included, without following it.
func (s *speller) spell(path string) (string, bool) {
	path = filepath.Clean(path)
	if !filepath.IsAbs(path) {
		return path, false
	}

	return s.under(string(filepath.Separator), strings.TrimPrefix(path, string(filepath.Separator)))
}

// under spells rel under dir, which is spelled already: a protected path
// inside a spelled root needs only its own names.
func (s *speller) under(dir, rel string) (string, bool) {
	out := dir
	names := strings.Split(rel, string(filepath.Separator))
	for i, name := range names {
		if name == "" {
			continue
		}
		if _, err := os.Lstat(filepath.Join(out, name)); err != nil {
			if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
				// The rest does not exist yet: nothing lists it.
				return filepath.Join(append([]string{out}, names[i:]...)...), true
			}

			return filepath.Join(dir, rel), false // it may exist, under a spelling unknown
		}
		stored, ok := s.storedName(out, name)
		if !ok {
			return filepath.Join(dir, rel), false
		}
		out = filepath.Join(out, stored)
	}

	return out, true
}

// storedName is the name dir lists for name, which exists in it: name
// itself, else the entry that is the same file. It reads the listing in
// chunks, unsorted, and stops at an exact match.
func (s *speller) storedName(dir, name string) (string, bool) {
	key := [2]string{dir, name}
	if stored, ok := s.listed[key]; ok {
		return stored, true
	}
	stored, ok := listedName(dir, name)
	if ok {
		s.listed[key] = stored
	}

	return stored, ok
}

func listedName(dir, name string) (string, bool) {
	f, err := os.Open(dir)
	if err != nil {
		return "", false
	}
	defer f.Close()
	var others []string
	for {
		names, err := f.Readdirnames(512)
		for _, n := range names {
			if n == name {
				return name, true
			}
			if strings.EqualFold(n, name) || !isASCII(n) || !isASCII(name) {
				others = append(others, n)
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", false
		}
	}
	want, err := os.Lstat(filepath.Join(dir, name))
	if err != nil {
		return "", false
	}
	for _, n := range others {
		if info, err := os.Lstat(filepath.Join(dir, n)); err == nil && os.SameFile(info, want) {
			return n, true
		}
	}

	return "", false
}

// isASCII reports whether a name is plain ASCII: another spelling of it is
// then only another case, and Unicode normalization cannot apply.
func isASCII(name string) bool {
	for i := range len(name) {
		if name[i] >= 0x80 {
			return false
		}
	}

	return true
}
