// Package clipboard reads an image from the system clipboard through the
// system's own tools, without cgo: osascript on macOS, and wl-paste or
// xclip on Linux. It also reads and writes text there, for the TUI's
// ctrl+v and copy: pbpaste and pbcopy, wl-paste and wl-copy, or xclip. The commands run through Exec and Input, so tests use
// fakes and never touch a real clipboard.
package clipboard

import (
	"context"
	"errors"
	"os"
	"os/exec"
)

// ErrNoImage means the clipboard holds no image and no image file.
var ErrNoImage = errors.New("no image on the clipboard")

// Content is what the clipboard held: image bytes, or the path of an image
// file copied in a file manager (Finder's copy, a URI list).
type Content struct {
	Data []byte
	Path string
}

// Reader reads an image from the clipboard.
type Reader interface {
	ReadImage(ctx context.Context) (Content, error)
}

// Exec runs a command and returns its standard output.
type Exec func(ctx context.Context, name string, args ...string) ([]byte, error)

// System is the host's reader: New with the real commands and environment.
func System() Reader {
	return New(runCommand, exec.LookPath, os.Getenv)
}

func runCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output() // the reader explains the failure
}

// unsupported is the reader where no clipboard tool is known.
type unsupported struct{ os string }

func (u unsupported) ReadImage(context.Context) (Content, error) {
	return Content{}, errors.New("pasting an image is not supported on " + u.os + "; paste or drop the image file's path instead")
}
