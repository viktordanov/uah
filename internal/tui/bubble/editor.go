package bubble

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"mvdan.cc/sh/v3/shell"

	"github.com/viktordanov/uah/internal/home"
	"github.com/viktordanov/uah/internal/sandbox"
	"github.com/viktordanov/uah/internal/tui/state"
	"github.com/viktordanov/uah/internal/tui/term"
)

var (
	errNoEditor = errors.New("the editor command is empty")
	errDraftDir = errors.New("the draft directory is not a plain directory")
)

// editDraft runs the editor on the draft with the terminal released, as
// ctrl+g in Claude Code and Codex does: term leaves the alt screen,
// the mouse, and bracketed paste while it runs, and restores them after.
func (m Model) editDraft(text string) term.Cmd {
	args, err := editorCommand(os.Getenv)
	if err != nil {
		return func() term.Msg { return state.DraftEdited{Err: err} }
	}
	dir := filepath.Join(home.Dir(), "editor")
	policy := sandbox.Policy{Mode: m.st.Settings.Mode.Sandbox(), Workspace: m.st.Settings.Workspace, WritableRoots: m.deps.WritableRoots}
	if draftDirExposed(policy, dir) {
		err := fmt.Errorf("sandboxed commands can write %s, so the draft is not written there; "+
			"move uah's home ($%s) out of the workspace, /tmp, $TMPDIR, and the writable roots", dir, home.Env)

		return func() term.Msg { return state.DraftEdited{Err: err} }
	}
	run := &editorRun{ctx: m.ctx, dir: dir, args: args, text: text}
	start := m.deps.Exec
	if start == nil {
		start = term.Exec
	}

	return start(run, func(err error) term.Msg {
		if err != nil {
			return state.DraftEdited{Err: err}
		}

		return state.DraftEdited{Text: run.saved}
	})
}

// editorCommand is $VISUAL, else $EDITOR, split into words as a shell
// would (`code --wait`, `nvim -f`), else vim, or vi without vim.
func editorCommand(getenv func(string) string) ([]string, error) {
	raw := strings.TrimSpace(getenv("VISUAL"))
	if raw == "" {
		raw = strings.TrimSpace(getenv("EDITOR"))
	}
	if raw == "" {
		if _, err := exec.LookPath("vim"); err == nil {
			return []string{"vim"}, nil
		}

		return []string{"vi"}, nil
	}
	args, err := shell.Fields(raw, getenv)
	if err != nil {
		return nil, fmt.Errorf("cannot read the editor command %q: %w", raw, err)
	}
	if len(args) == 0 || args[0] == "" {
		return nil, errNoEditor
	}

	return args, nil
}

// draftDirExposed reports whether a command in the sandbox could read or
// change the draft file in dir, as Codex's editor_directory checks: when it
// can write dir or its parent, or a writable root lies inside dir. uah's
// home is never writable in read-only mode, nor in workspace-write unless
// $UAH_HOME is in a writable root under another name than .uah, which
// stays protected there. Yolo mode has no sandbox to keep out.
func draftDirExposed(p sandbox.Policy, dir string) bool {
	if p.Mode == sandbox.FullAccess {
		return false
	}
	if p.CanWrite(dir) || p.CanWrite(filepath.Dir(dir)) {
		return true
	}
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	for _, root := range p.Writable() {
		if rel, err := filepath.Rel(dir, root); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}

	return false
}

// editorRun is one edit, a term.ExecCommand: it writes the draft to a .md
// file (0600) in dir, <uah home>/editor (0700), runs the editor on it, reads
// it back into saved, and removes it.
type editorRun struct {
	ctx            context.Context // a term.ExecCommand's Run takes none
	dir            string
	args           []string
	text           string
	saved          string
	stdin          io.Reader
	stdout, stderr io.Writer
}

func (r *editorRun) SetStdin(in io.Reader)   { r.stdin = in }
func (r *editorRun) SetStdout(out io.Writer) { r.stdout = out }
func (r *editorRun) SetStderr(out io.Writer) { r.stderr = out }

func (r *editorRun) Run() error {
	if err := draftDir(r.dir); err != nil {
		return err
	}
	f, err := os.CreateTemp(r.dir, "prompt-*.md") // CreateTemp makes it 0600
	if err != nil {
		return fmt.Errorf("failed to create the file to edit: %w", err)
	}
	path := f.Name()
	defer func() { _ = os.Remove(path) }()
	_, err = f.WriteString(r.text)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("failed to write the file to edit: %w", err)
	}
	// The user's own editor, from $VISUAL or $EDITOR.
	cmd := exec.CommandContext(r.ctx, r.args[0], append(r.args[1:], path)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = r.stdin, r.stdout, r.stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", filepath.Base(r.args[0]), err)
	}
	saved, err := os.ReadFile(path) // the file this run made
	if err != nil {
		return fmt.Errorf("failed to read the edited file: %w", err)
	}
	r.saved = string(saved)

	return nil
}

// draftDir makes the draft directory, 0700, and refuses a symbolic link or
// a file in its place, which could lead the draft elsewhere.
func draftDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("failed to create the draft directory: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("failed to read the draft directory: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s: %w", dir, errDraftDir)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("failed to make the draft directory private: %w", err)
	}

	return nil
}
