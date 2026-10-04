package clipboard

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
)

// ErrNoReadTool means no tool to read the clipboard's text can be run:
// pbpaste on macOS, wl-paste (Wayland) or xclip (X11) on Linux.
var ErrNoReadTool = errors.New("no clipboard tool: pbpaste, wl-paste, or xclip")

// TextReader reads the clipboard's text with the system's own tool, for
// ctrl+v when the clipboard holds no image.
type TextReader struct {
	Exec     Exec
	LookPath func(string) (string, error)
	Getenv   func(string) string
	GOOS     string
}

// SystemTextReader is the host's reader, with the real commands and
// environment.
func SystemTextReader() TextReader {
	return TextReader{Exec: runCommand, LookPath: exec.LookPath, Getenv: os.Getenv, GOOS: runtime.GOOS}
}

// ReadText returns the clipboard's text, or ErrNoReadTool.
func (r TextReader) ReadText(ctx context.Context) (string, error) {
	name, args, ok := r.tool()
	if !ok {
		return "", ErrNoReadTool
	}
	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	out, err := r.Exec(ctx, name, args...)
	if err != nil {
		return "", fmt.Errorf("failed to paste with %s: %w", name, err)
	}

	return string(out), nil
}

// tool picks pbpaste on macOS, and wl-paste in a Wayland session or xclip
// in an X11 one on Linux, when installed.
func (r TextReader) tool() (name string, args []string, ok bool) {
	has := func(name string) bool { _, err := r.LookPath(name); return err == nil }
	switch {
	case r.GOOS == "darwin" && has("pbpaste"):
		return "pbpaste", nil, true
	case r.GOOS != "linux":
		return "", nil, false
	case r.Getenv("WAYLAND_DISPLAY") != "" && has("wl-paste"):
		return "wl-paste", []string{"--no-newline", "--type", "text/plain"}, true
	case r.Getenv("DISPLAY") != "" && has("xclip"):
		return xclip.name, xclipArgs("-o"), true
	}

	return "", nil, false
}
