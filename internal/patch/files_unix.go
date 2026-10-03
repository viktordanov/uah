//go:build unix

package patch

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// confinedFiles reads and writes each path at its target through directory
// handles: every directory is opened from / with O_NOFOLLOW, one name at a
// time, and the file itself is opened, created, or removed relative to its
// directory's handle, also with O_NOFOLLOW. Swapping a directory for a
// symlink after the patch was approved then makes the patch fail instead
// of writing where the symlink leads.
type confinedFiles Targets

// opOpen names opening a path in its errors.
const opOpen = "open"

// errSymlink is why a confined path does not open.
var errSymlink = errors.New("a symlink or not a directory, and the patch does not follow symlinks")

func (c confinedFiles) target(path string) (string, error) {
	t, ok := c[path]
	if !ok || !filepath.IsAbs(t) {
		return "", fmt.Errorf("%s was not approved for this patch", path)
	}

	return filepath.Clean(t), nil
}

// openDir opens the directory dir without following a symlink in it; with
// create it makes the missing directories (mode 0755).
func openDir(dir string, create bool) (int, error) {
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, &fs.PathError{Op: opOpen, Path: "/", Err: err}
	}
	at := "/"
	for name := range strings.SplitSeq(strings.TrimPrefix(dir, "/"), "/") {
		if name == "" {
			continue
		}
		at = filepath.Join(at, name)
		next, err := openSub(fd, name)
		if errors.Is(err, unix.ENOENT) && create {
			if err = unix.Mkdirat(fd, name, 0o755); err == nil || errors.Is(err, unix.EEXIST) {
				next, err = openSub(fd, name)
			}
		}
		_ = unix.Close(fd)
		if err != nil {
			return -1, &fs.PathError{Op: opOpen, Path: at, Err: nofollowErr(err)}
		}
		fd = next
	}

	return fd, nil
}

// openSub opens the directory name in the directory fd, not a symlink.
func openSub(fd int, name string) (int, error) {
	for {
		next, err := unix.Openat(fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if !errors.Is(err, unix.EINTR) {
			return next, err
		}
	}
}

// nofollowErr names a refused symlink: O_NOFOLLOW reports one as ELOOP,
// and O_DIRECTORY as ENOTDIR on some systems.
func nofollowErr(err error) error {
	if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) {
		return errSymlink
	}

	return err
}

// inDir runs fn with the handle of path's target's directory and the
// target's name.
func (c confinedFiles) inDir(path string, create bool, fn func(dir int, name string) error) error {
	t, err := c.target(path)
	if err != nil {
		return err
	}
	dir, name := filepath.Split(t)
	if name == "" {
		return &fs.PathError{Op: opOpen, Path: t, Err: fs.ErrInvalid}
	}
	fd, err := openDir(dir, create)
	if err != nil {
		return err
	}
	defer unix.Close(fd)

	return fn(fd, name)
}

func (c confinedFiles) lstat(path string) (fs.FileInfo, error) {
	var info fs.FileInfo
	err := c.inDir(path, false, func(dir int, name string) error {
		var st unix.Stat_t
		if err := unix.Fstatat(dir, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return &fs.PathError{Op: "lstat", Path: path, Err: err}
		}
		info = statInfo{name: name, st: st}

		return nil
	})

	return info, err
}

// stat is lstat: a target is resolved already, so a symlink there is one a
// command put in, and the patch does not follow it.
func (c confinedFiles) stat(path string) (fs.FileInfo, error) { return c.lstat(path) }

func (c confinedFiles) readFile(path string) ([]byte, error) {
	var data []byte
	err := c.inDir(path, false, func(dir int, name string) error {
		f, err := openFile(dir, name, path, unix.O_RDONLY, 0)
		if err != nil {
			return err
		}
		defer f.Close()
		data, err = io.ReadAll(f)

		return err
	})

	return data, err
}

func (c confinedFiles) mkdirParents(path string) error {
	return c.inDir(path, true, func(int, string) error { return nil })
}

func (c confinedFiles) writeFile(path string, data []byte, perm fs.FileMode) error {
	return c.inDir(path, true, func(dir int, name string) error {
		f, err := openFile(dir, name, path, unix.O_WRONLY|unix.O_CREAT|unix.O_TRUNC, uint32(perm.Perm()))
		if err != nil {
			return err
		}
		_, werr := f.Write(data)

		return errors.Join(werr, f.Close())
	})
}

func (c confinedFiles) remove(path string) error {
	return c.inDir(path, false, func(dir int, name string) error {
		if err := unix.Unlinkat(dir, name, 0); err != nil {
			return &fs.PathError{Op: "remove", Path: path, Err: err}
		}

		return nil
	})
}

// openFile opens the regular file name in dir without following a symlink
// and without blocking on a FIFO.
func openFile(dir int, name, path string, flags int, perm uint32) (*os.File, error) {
	var fd int
	var err error
	for {
		fd, err = unix.Openat(dir, name, flags|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, perm)
		if !errors.Is(err, unix.EINTR) {
			break
		}
	}
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			err = errSymlink
		}

		return nil, &fs.PathError{Op: opOpen, Path: path, Err: err}
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG {
		_ = unix.Close(fd)

		return nil, &fs.PathError{Op: opOpen, Path: path, Err: errors.New("not a regular file")}
	}
	if err := unix.SetNonblock(fd, false); err != nil {
		_ = unix.Close(fd)

		return nil, &fs.PathError{Op: opOpen, Path: path, Err: err}
	}

	return os.NewFile(uintptr(fd), path), nil
}

// statInfo is an fs.FileInfo from fstatat.
type statInfo struct {
	name string
	st   unix.Stat_t
}

func (s statInfo) Name() string { return s.name }
func (s statInfo) Size() int64  { return s.st.Size }
func (s statInfo) Mode() fs.FileMode {
	mode := fs.FileMode(s.st.Mode & 0o777)
	switch s.st.Mode & unix.S_IFMT {
	case unix.S_IFDIR:
		mode |= fs.ModeDir
	case unix.S_IFLNK:
		mode |= fs.ModeSymlink
	case unix.S_IFREG:
	default:
		mode |= fs.ModeIrregular
	}

	return mode
}
func (s statInfo) ModTime() time.Time { return time.Unix(s.st.Mtim.Unix()) }
func (s statInfo) IsDir() bool        { return s.Mode().IsDir() }
func (s statInfo) Sys() any           { return &s.st }
