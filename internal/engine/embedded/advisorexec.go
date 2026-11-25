package embedded

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/viktordanov/uah/internal/engine"
)

// The command backend runs [advisor] command, a program and its
// arguments, in the session's workspace, outside the sandbox, with the
// user's environment and UAH_ADVISOR=off, so a command that runs uah does
// not advise itself. The transcript goes to its stdin as text or JSON
// (advisorinput.go), written by os/exec's goroutine while the command's
// output is read, so a large one cannot deadlock; a command that exits
// without reading it all is not an error for that. Its stdout is the
// advice. The command runs in a process group of its own, which is killed
// when the call times out or is canceled (Esc, the run's end), before its
// leader is reaped. Once the leader has exited the group is not
// signalled, as a hook's is not, since its ID may be another's then: a
// process it left that holds its output past advisorWaitDelay no longer
// holds the call, and goes on.

const (
	// advisorMaxAdvice is how much of a command's stdout is kept: less
	// than a tool output's limit (operation.DefaultMaxOutputLength), so the
	// runner does not cut the advice and its note again.
	advisorMaxAdvice = 32_000
	// advisorStderrTail is how much of the end of a command's stderr a
	// failed call reports.
	advisorStderrTail = 2048
	// advisorWaitDelay bounds the wait for a command's pipes once it has
	// exited or been killed: a process it left behind may hold them open.
	advisorWaitDelay = time.Second
)

// advisorCommand is the command backend.
type advisorCommand struct {
	argv []string
	// program is the path argv runs as: argv[0] found in PATH and pinned
	// when the session opened ("": not found, so nothing runs).
	program   string
	input     string
	maxTokens int64
	dir       string
	// searches are the session's recorded web searches, which go back
	// into the transcript next to their anchors.
	searches []searchRecord
	// tag marks the text transcript's entry lines ("": a random one).
	tag string
	// changeable reports whether the session's commands could change the
	// program now (nil: not checked).
	changeable func(path string) bool
}

// advise runs the command once on the transcript. The advice it returns
// names the command for the advisor log, with no usage: a command's
// tokens are not known. A program the session's commands could change now
// (a grant since the session opened), or a transcript that cannot be
// built, runs nothing, and names none.
func (c advisorCommand) advise(ctx context.Context, t advisorTranscript) (advice, error) {
	stdin, err := c.stdin(t)
	if err != nil {
		return advice{}, err // no command ran: the log leaves it out
	}
	// Checked last before it starts: a grant since the session opened, or
	// while the transcript was built, may have made it changeable.
	if c.changeable != nil && filepath.IsAbs(c.program) && c.changeable(c.program) {
		return advice{}, fmt.Errorf("the command %s is now where the agent's commands can write, or leads elsewhere, so it does not run outside the sandbox", c.program)
	}
	text, err := runAdvisorCommand(ctx, c.program, c.argv, c.dir, stdin)
	if _, ok := errors.AsType[notStartedError](err); ok {
		return advice{}, err // nothing ran: the log leaves it out
	}

	return advice{text: text, model: engine.AdvisorCommandPrefix + c.argv[0]}, err
}

// notStartedError is the error of a command that did not start.
type notStartedError struct{ error }

func (e notStartedError) Unwrap() error { return e.error }

// stdin is the transcript as the command reads it.
func (c advisorCommand) stdin(t advisorTranscript) ([]byte, error) {
	entries := advisorEntries(t, c.searches)
	if c.input == engine.AdvisorInputJSON {
		return renderJSON(t, entries, c.maxTokens)
	}
	text, err := renderText(t, entries, c.maxTokens, cmp.Or(c.tag, advisorTag()))

	return []byte(text), err
}

// commandError is a command that ran and gave no advice: it failed, or
// printed none. Its first line has the last line of the command's stderr;
// the detail has the stderr's last advisorStderrTail bytes.
type commandError struct {
	msg    string
	stderr string
}

func (e *commandError) Error() string {
	if i := strings.LastIndexByte(e.stderr, '\n'); i >= 0 {
		return e.msg + ": " + e.stderr[i+1:]
	}
	if e.stderr != "" {
		return e.msg + ": " + e.stderr
	}

	return e.msg
}

func (e *commandError) detail() string {
	if !strings.Contains(e.stderr, "\n") {
		return "" // its one line is in Error
	}

	return "The end of the command's stderr:\n" + e.stderr
}

// runAdvisorCommand runs program with argv, argv[0] as its name, in dir
// with stdin, until it exits or ctx ends, and returns its stdout,
// trimmed: the advice. A non-zero exit or no output is a commandError.
// The program must be an absolute path: a name is not looked up in PATH
// again.
func runAdvisorCommand(ctx context.Context, program string, argv []string, dir string, stdin []byte) (string, error) {
	if !filepath.IsAbs(program) {
		// app pins the program to an absolute path when the session opens,
		// once it found it in PATH and checked the agent cannot change it.
		return "", notStartedError{fmt.Errorf("the command %s was not in PATH when the session opened", argv[0])}
	}
	cmd := exec.CommandContext(ctx, program, argv[1:]...)
	cmd.Args[0] = argv[0] // a multicall program dispatches on its name
	cmd.Dir = dir
	cmd.Env = append(cmd.Environ(), engine.EnvAdvisor+"=off") // with PWD set to dir
	cmd.Stdin = bytes.NewReader(stdin)
	stdout, stderr := &capped{max: advisorMaxAdvice}, &tailBuffer{max: advisorStderrTail}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = advisorWaitDelay
	if err := cmd.Start(); err != nil {
		return "", notStartedError{fmt.Errorf("failed to start the command %s: %w", argv[0], err)}
	}
	err := cmd.Wait()
	if ctx.Err() != nil {
		return "", ctx.Err() // Cancel killed the group before Wait reaped its leader
	}
	if errors.Is(err, exec.ErrWaitDelay) {
		// It exited 0, and a process it left held its output open past
		// advisorWaitDelay: what the command wrote by then is the advice.
		err = nil
	}
	if exit, ok := errors.AsType[*exec.ExitError](err); ok {
		return "", &commandError{msg: fmt.Sprintf("the command %s failed with %s", argv[0], exit), stderr: stderr.String()}
	}
	if err != nil {
		return "", fmt.Errorf("the command %s failed: %w", argv[0], err)
	}
	text := strings.TrimSpace(strings.ToValidUTF8(stdout.String(), ""))
	if text == "" {
		return "", &commandError{msg: fmt.Sprintf("the command %s printed no advice", argv[0]), stderr: stderr.String()}
	}
	if stdout.cut {
		text += fmt.Sprintf("\n\n(The advice was cut at %d bytes.)", advisorMaxAdvice)
	}

	return text, nil
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	max int
	buf []byte
	cut bool
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	if len(p) >= t.max {
		t.cut = t.cut || len(p) > t.max || len(t.buf) > 0
		t.buf = append(t.buf[:0], p[len(p)-t.max:]...)

		return len(p), nil
	}
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - t.max; over > 0 {
		t.buf, t.cut = t.buf[over:], true
	}

	return len(p), nil
}

// String is the tail, trimmed, with "…" before it when earlier bytes were
// dropped.
func (t *tailBuffer) String() string {
	s := strings.TrimSpace(strings.ToValidUTF8(string(t.buf), ""))
	if t.cut && s != "" {
		return "…" + s
	}

	return s
}
