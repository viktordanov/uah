package bubble

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"

	"github.com/viktordanov/uah/internal/tui/render"
	"github.com/viktordanov/uah/internal/tui/state"
	"github.com/viktordanov/uah/internal/tui/term"
)

// File links (state/links.go): the words of the agent's messages looked up
// as files, and a link opened in the peek overlay, the editor, or the
// system's default app. Every path is checked before it is used: it must
// lead, through any symlinks, to a regular file.

var (
	errNotFile = errors.New("not a regular file")
	errSpecial = errors.New("a device or system file, not read")
	// errRunnable refuses to hand the opener a file it would run.
	errRunnable = errors.New("a file the system would run, not opened; peek at it or open it in the editor")
)

// Limits of what the overlay reads: past them it shows the start and says
// so. A longer line is cut, so drawing it stays cheap.
const (
	maxPeekBytes     = 2 << 20
	maxPeekLines     = 20_000
	maxPeekLineBytes = 4 << 10
	// binaryProbe is how much of a file is looked at for a NUL byte.
	binaryProbe = 8 << 10
)

// runLinks runs the file links' effects; ok is false for any other.
func (m Model) runLinks(e state.Effect) (term.Cmd, bool) {
	switch e := e.(type) {
	case state.EffResolveLinks:
		return func() term.Msg { return resolveLinks(e) }, true
	case state.EffPeekFile:
		styles, rows := m.cache.Styles(), render.PeekRows(m.w, m.h)

		return func() term.Msg { return readPeek(e.Link, styles, rows) }, true
	case state.EffEditFile:
		return m.editFile(e.Link), true
	case state.EffOpenFile:
		return m.openFile(e.Link), true
	}

	return nil, false
}

// resolveLinks finds which words of each message name a regular file in
// the workspace.
func resolveLinks(e state.EffResolveLinks) state.LinksResolved {
	root, err := filepath.EvalSymlinks(e.Workspace)
	if err != nil {
		return state.LinksResolved{}
	}
	var out state.LinksResolved
	for _, msg := range e.Messages {
		links := map[string]state.FileLink{}
		for _, w := range msg.Words {
			p, line, end := state.SplitLines(w)
			path := expand(p, e.Workspace, e.Home)
			resolved, err := regularFile(path)
			if err != nil || !within(root, resolved) {
				continue
			}
			links[w] = state.FileLink{Path: path, Line: line, End: end}
		}
		out.Messages = append(out.Messages, state.ResolvedLinks{Key: msg.Key, Text: msg.Text, Links: links})
	}

	return out
}

// expand makes a word's path absolute: ~ under home, a relative path under
// the workspace.
func expand(p, workspace, home string) string {
	switch {
	case (p == "~" || strings.HasPrefix(p, "~/")) && home != "":
		return filepath.Join(home, p[1:])
	case filepath.IsAbs(p):
		return filepath.Clean(p)
	}

	return filepath.Join(workspace, p)
}

// within reports whether path is root or under it.
func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)

	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// regularFile resolves path's symlinks and checks that it ends at a
// regular file outside the system's device and process trees; it returns
// the resolved path.
func regularFile(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err //nolint:wrapcheck // the error names the path
	}
	for _, special := range []string{"/dev", "/proc", "/sys"} {
		if within(special, resolved) {
			return "", fmt.Errorf("%s: %w", path, errSpecial)
		}
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err //nolint:wrapcheck // the error names the path
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s: %w", path, errNotFile)
	}

	return resolved, nil
}

// readPeek reads a file for the overlay: at most maxPeekBytes and
// maxPeekLines, with control characters shown as U+FFFD so nothing in it
// reaches the terminal as a command, highlighted by its name. A binary
// file, or one that cannot be read, is a note.
func readPeek(l state.FileLink, styles *render.Styles, rows int) state.PeekLoaded {
	out := state.PeekLoaded{Link: l, Rows: rows}
	data, cut, err := readHead(l.Path)
	if err != nil {
		out.Note = err.Error()

		return out
	}
	if bytes.IndexByte(data[:min(len(data), binaryProbe)], 0) >= 0 {
		out.Note = "binary file, not shown"

		return out
	}
	text := strings.TrimSuffix(string(data), "\n")
	lines := strings.Split(text, "\n")
	if len(lines) > maxPeekLines {
		lines, cut = lines[:maxPeekLines], true
	}
	for i, line := range lines {
		lines[i] = printable(line)
	}
	if cut {
		out.Note = fmt.Sprintf("showing the first %d lines; the file is larger", len(lines))
	}
	out.Lines = styles.Highlight(filepath.Base(l.Path), lines)

	return out
}

// readHead reads up to maxPeekBytes of a regular file, opened without
// blocking (a FIFO swapped in after the check does not hang it) and
// checked again once open; cut says there was more, which is dropped at
// the last whole line.
func readHead(path string) (data []byte, cut bool, err error) {
	resolved, err := regularFile(path)
	if err != nil {
		return nil, false, err
	}
	f, err := os.OpenFile(resolved, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, false, err //nolint:wrapcheck // the error names the path
	}
	defer func() { _ = f.Close() }()
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		return nil, false, fmt.Errorf("%s: %w", path, errNotFile)
	}
	data, err = io.ReadAll(io.LimitReader(f, maxPeekBytes+1))
	if err != nil {
		return nil, false, fmt.Errorf("failed to read %s: %w", path, err)
	}
	if len(data) > maxPeekBytes {
		data = data[:maxPeekBytes]
		if i := bytes.LastIndexByte(data, '\n'); i > 0 {
			data = data[:i]
		}

		return data, true, nil
	}

	return data, false, nil
}

// printable is a file's line as the overlay draws it: no carriage return
// at its end, invalid UTF-8 and control characters other than tab as
// U+FFFD, and cut after maxPeekLineBytes.
func printable(line string) string {
	line = strings.TrimSuffix(line, "\r")
	if len(line) > maxPeekLineBytes {
		cut := maxPeekLineBytes
		for cut > 0 && !utf8.RuneStart(line[cut]) {
			cut--
		}
		line = line[:cut] + "…"
	}
	clean := func(r rune) bool { return r != '\t' && unicode.IsControl(r) }
	if utf8.ValidString(line) && !strings.ContainsFunc(line, clean) {
		return line
	}

	return strings.Map(func(r rune) rune {
		if clean(r) {
			return utf8.RuneError
		}

		return r
	}, strings.ToValidUTF8(line, string(utf8.RuneError)))
}

// editFile opens a link in $VISUAL or $EDITOR at its line: a terminal
// editor with the terminal released, as ctrl+g runs it, a windowed one
// (code, cursor, zed, subl) on its own.
func (m Model) editFile(l state.FileLink) term.Cmd {
	editor, err := editorCommand(os.Getenv)
	if err == nil {
		_, err = regularFile(l.Path)
	}
	if err != nil {
		return func() term.Msg { return state.FileOpened{Link: l, Err: err} }
	}
	args, windowed := editorAt(editor, l.Path, l.Line)
	if windowed {
		return m.launch(l, args)
	}
	run := &fileEditorRun{ctx: m.ctx, args: args}
	start := m.deps.Exec
	if start == nil {
		start = term.Exec
	}

	return start(run, func(err error) term.Msg {
		if err != nil {
			return state.FileOpened{Link: l, Err: err}
		}

		return nil
	})
}

// openFile opens a link with the system's default app: open on macOS,
// xdg-open elsewhere. A file the opener would run rather than show (one
// that may be executed, or a launcher such as a .command or a .desktop
// file) is refused: the agent may name any file, and a click must not run
// one.
func (m Model) openFile(l state.FileLink) term.Cmd {
	resolved, err := regularFile(l.Path)
	if err == nil {
		err = runnable(resolved)
	}
	if err != nil {
		return func() term.Msg { return state.FileOpened{Link: l, Err: err} }
	}

	return m.launch(l, []string{opener(), l.Path})
}

// launchers are the extensions of files an opener runs: macOS's Terminal
// and script files, and the desktop's launchers.
var launchers = []string{".command", ".tool", ".terminal", ".app", ".scpt", ".applescript", ".workflow", ".desktop", ".jar", ".webloc", ".inetloc", ".fileloc"}

// runnable refuses a file the system's opener would run: executable, or a
// launcher by its extension.
func runnable(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err //nolint:wrapcheck // the error names the path
	}
	if info.Mode().Perm()&0o111 != 0 || slices.Contains(launchers, strings.ToLower(filepath.Ext(path))) {
		return fmt.Errorf("%s: %w", path, errRunnable)
	}

	return nil
}

// opener is the system's command that opens a file with its default app.
func opener() string {
	if isDarwin {
		return "open"
	}

	return "xdg-open"
}

// launch starts a program of its own, off the update loop, and reports a
// failure.
func (m Model) launch(l state.FileLink, args []string) term.Cmd {
	ctx, start := m.ctx, m.deps.Launch
	if start == nil {
		start = detached
	}

	return func() term.Msg {
		if err := start(ctx, args); err != nil {
			return state.FileOpened{Link: l, Err: err}
		}

		return nil
	}
}

// detached runs a program in a process group of its own, with no terminal
// and outliving uah, and waits for it: open, xdg-open, and the editors'
// commands return at once, and an error is theirs.
func detached(ctx context.Context, args []string) error {
	cmd := exec.CommandContext(context.WithoutCancel(ctx), args[0], args[1:]...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("%s: %w: %s", filepath.Base(args[0]), err, firstLine(msg))
		}

		return fmt.Errorf("%s: %w", filepath.Base(args[0]), err)
	}

	return nil
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")

	return line
}

// Editors by how they take a line: "+12 path", "--goto path:12", or
// "path:12"; the windowed ones start on their own.
var (
	plusLine  = []string{"vi", "vim", "nvim", "nano", "emacs", "emacsclient", "micro", "kak"}
	gotoLine  = []string{"code", "code-insiders", "codium", "cursor", "windsurf"}
	colonLine = []string{"zed", "subl", "hx", "helix"}
	windowed  = []string{"code", "code-insiders", "codium", "cursor", "windsurf", "zed", "subl"}
)

// editorAt is the editor command, as $VISUAL or $EDITOR gives it, opening
// path at line (0: the top), and whether the editor is a window of its
// own. A windowed editor's --wait goes, since uah does not wait for it.
func editorAt(editor []string, path string, line int) (args []string, window bool) {
	name := strings.TrimSuffix(strings.ToLower(filepath.Base(editor[0])), ".exe")
	window = slices.Contains(windowed, name)
	args = slices.Clone(editor)
	if window {
		args = slices.DeleteFunc(args[1:], func(a string) bool { return a == "--wait" || a == "-w" })
		args = append([]string{editor[0]}, args...)
	}
	if line <= 0 {
		return append(args, path), window
	}
	at := fmt.Sprintf("%s:%d", path, line)
	switch {
	case slices.Contains(plusLine, name):
		return append(args, fmt.Sprintf("+%d", line), path), window
	case slices.Contains(gotoLine, name):
		return append(args, "--goto", at), window
	case slices.Contains(colonLine, name):
		return append(args, at), window
	}

	return append(args, path), window
}

// fileEditorRun runs a terminal editor on a file, a term.ExecCommand.
type fileEditorRun struct {
	ctx            context.Context
	args           []string
	stdin          io.Reader
	stdout, stderr io.Writer
}

func (r *fileEditorRun) SetContext(ctx context.Context) { r.ctx = ctx }
func (r *fileEditorRun) SetStdin(in io.Reader)          { r.stdin = in }
func (r *fileEditorRun) SetStdout(out io.Writer)        { r.stdout = out }
func (r *fileEditorRun) SetStderr(out io.Writer)        { r.stderr = out }

func (r *fileEditorRun) Run() error {
	// The user's own editor, from $VISUAL or $EDITOR.
	cmd := exec.CommandContext(r.ctx, r.args[0], r.args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = r.stdin, r.stdout, r.stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", filepath.Base(r.args[0]), err)
	}

	return nil
}

// Mouse and keys while the overlay is open.

// onPeekMouse scrolls the overlay with the wheel; a click outside it
// closes it.
func (m Model) onPeekMouse(msg term.Msg) (term.Model, term.Cmd) {
	rows := render.PeekRows(m.w, m.h)
	switch msg := msg.(type) {
	case term.MouseWheelMsg:
		switch msg.Button {
		case term.MouseWheelUp:
			return m.dispatch(state.PeekScroll{Delta: -wheelLines, Rows: rows})
		case term.MouseWheelDown:
			return m.dispatch(state.PeekScroll{Delta: wheelLines, Rows: rows})
		}
	case term.MouseClickMsg:
		x, y, w, h := render.PeekRect(m.w, m.h)
		if msg.X < x || msg.X >= x+w || msg.Y < y || msg.Y >= y+h {
			return m.dispatch(state.PeekClose{})
		}
	}

	return m, nil
}

// onPeekKey: ↑↓ and j k scroll a line, pgup and pgdn (and space) a page,
// g and G (home, end) go to the ends, e and o open the file in the editor
// or with the system, and esc, q, or ctrl+c close the overlay. Every other
// key does nothing while it is open.
func (m Model) onPeekKey(msg term.KeyPressMsg) (term.Model, term.Cmd) {
	rows := render.PeekRows(m.w, m.h)
	scroll := func(d int) (term.Model, term.Cmd) { return m.dispatch(state.PeekScroll{Delta: d, Rows: rows}) }
	switch msg.String() {
	case keyUp, "k", "ctrl+p":
		return scroll(-1)
	case keyDown, "j", keyCtrlN:
		return scroll(1)
	case "pgup", "b", "shift+space":
		return scroll(-max(rows-1, 1))
	case "pgdown", "space", " ":
		return scroll(max(rows-1, 1))
	case "g", "home":
		return scroll(-maxPeekLines)
	case "G", "shift+g", "end":
		return scroll(maxPeekLines)
	case "e":
		return m.dispatch(state.PeekEdit{})
	case "o":
		return m.dispatch(state.PeekOpen{})
	case keyEsc, "q", keyCtrlC:
		return m.dispatch(state.PeekClose{})
	}

	return m, nil
}

// isDarwin picks macOS's opener.
const isDarwin = runtime.GOOS == "darwin"
