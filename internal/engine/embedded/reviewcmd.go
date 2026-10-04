package embedded

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/viktordanov/uah-core/harness/tool"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/review"
	"github.com/viktordanov/uah/internal/sandbox"
)

// reviewShell is the shell the reviewer's commands run in: POSIX, whatever
// the user's login shell is, since the model writes POSIX commands.
const reviewShell = "/bin/sh"

// maxReviewOutput is how much of a reviewer's command output is read; the
// reviewer cuts it to its own budget.
const maxReviewOutput = 1 << 20

// reviewCommands runs the auto-reviewer's commands as Codex runs its
// reviewer's: in the read-only sandbox, without network, with the
// environment policy, and with a temporary directory of the reviewer's own
// (operations/<session>/review-tmp), the only place it can write. Without a
// configured or available sandbox, or under a tool policy that does not
// allow Bash, the reviewer gets no commands.
func (w *wiring) reviewCommands(req core.Request) review.Runner {
	if w.e.cfg.Sandbox == nil || req.Workspace == "" || !w.e.cfg.Tools.Allows(tool.BashName) {
		return nil
	}
	p := sandbox.Policy{
		Mode: sandbox.ReadOnly, Workspace: req.Workspace,
		TempDir: filepath.Join(w.l.SessionsDir, "operations", req.SessionID, "review-tmp"),
	}
	shell, err := sandbox.Shell(w.e.cfg.SandboxDir, p, w.e.cfg.Env, reviewShell)
	if err != nil {
		if !errors.Is(err, sandbox.ErrUnavailable) {
			_, _ = fmt.Fprintf(w.l.Stderr, "embedded: the auto-reviewer runs no commands: %v\n", err)
		}

		return nil
	}
	workspace := req.Workspace

	return func(ctx context.Context, command, workdir string) (string, int, error) {
		return runReviewCommand(ctx, shell, command, resolveDir(workspace, workdir))
	}
}

// resolveDir is workdir, relative to the workspace, or the workspace.
func resolveDir(workspace, workdir string) string {
	switch {
	case workdir == "":
		return workspace
	case filepath.IsAbs(workdir):
		return workdir
	default:
		return filepath.Join(workspace, workdir)
	}
}

// runReviewCommand runs command with the sandboxing shell until it exits
// or ctx ends, reading at most maxReviewOutput bytes of its output.
func runReviewCommand(ctx context.Context, shell, command, dir string) (string, int, error) {
	cmd := exec.CommandContext(ctx, shell, "-c", command)
	cmd.Dir = dir
	out := &capped{max: maxReviewOutput}
	cmd.Stdout, cmd.Stderr = out, out
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	if ctx.Err() != nil {
		return out.String(), -1, fmt.Errorf("stopped: %w", ctx.Err())
	}
	if exit, ok := errors.AsType[*exec.ExitError](err); ok {
		return out.String(), exit.ExitCode(), nil
	}
	if err != nil {
		return out.String(), -1, err
	}

	return out.String(), 0, nil
}

// capped keeps the first max bytes written to it and drops the rest.
type capped struct {
	bytes.Buffer

	max int
}

func (c *capped) Write(p []byte) (int, error) {
	if room := c.max - c.Len(); room > 0 {
		c.Buffer.Write(p[:min(len(p), room)])
	}

	return len(p), nil
}
