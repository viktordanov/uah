package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

const maxOutput = 1 << 20

// exec runs one hook in the workspace with the event on stdin.
func (r *Runner) exec(ctx context.Context, h Hook, in Input) Result {
	timeout := h.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	if h.Event == SessionEnd {
		timeout = min(timeout, sessionEndTimeout)
	}
	res := Result{Hook: h}
	payload, err := json.Marshal(in)
	if err != nil {
		res.Outcome, res.Reason = OutcomeError, fmt.Sprintf("failed to encode the hook input: %v", err)

		return res
	}
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", h.Command)
	cmd.Dir = r.workspace
	cmd.Env = append(os.Environ(), "UAH_HOOK_EVENT="+string(h.Event), "UAH_PROJECT_DIR="+r.workspace)
	cmd.Stdin = bytes.NewReader(payload)
	var stdout, stderr limitedBuffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	started := time.Now()
	err = cmd.Run()
	if errors.Is(err, exec.ErrWaitDelay) {
		// The hook exited 0, but a process it left behind kept its output
		// open past WaitDelay: what it wrote by then is its answer, so a
		// deny it printed still blocks.
		err = nil
	}
	res.Duration = time.Since(started)
	res.Stdout = stdout.String()
	res.ExitCode = cmd.ProcessState.ExitCode()
	switch {
	case parent.Err() != nil:
		res.Outcome, res.Reason = OutcomeError, "stopped before it finished"
	case ctx.Err() != nil:
		res.Outcome, res.Reason = OutcomeError, fmt.Sprintf("timed out after %s", timeout)
	case err == nil:
		res.Outcome = OutcomeOK
		if out := strings.TrimSpace(res.Stdout); strings.HasPrefix(out, "{") {
			if jerr := json.Unmarshal([]byte(out), &res.Output); jerr != nil {
				res.Outcome, res.Reason = OutcomeError, fmt.Sprintf("invalid JSON output: %v", jerr)
			}
		}
	case res.ExitCode == 2:
		res.Outcome, res.Reason = OutcomeBlocked, firstNonEmpty(strings.TrimSpace(stderr.String()), "blocked by a hook")
	default:
		if _, ok := errors.AsType[*exec.ExitError](err); !ok {
			res.Outcome, res.Reason = OutcomeError, err.Error()

			break
		}
		res.Outcome, res.Reason = OutcomeError, firstNonEmpty(strings.TrimSpace(stderr.String()), fmt.Sprintf("exit %d", res.ExitCode))
	}

	return res
}

// limitedBuffer keeps the first maxOutput bytes and discards the rest.
type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := maxOutput - b.Len(); room > 0 {
		b.Buffer.Write(p[:min(len(p), room)])
	}

	return len(p), nil
}
