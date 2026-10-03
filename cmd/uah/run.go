package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/app"
	"github.com/viktordanov/uah/internal/session"
)

const (
	exitDiskLimit = 3
	exitInterrupt = 130
)

// runCommand is `uah exec`, also `uah run`: a headless session that prints
// progress and answers, named as `codex exec`.
func runCommand() *cli.Command {
	flags := append(sessionFlags(),
		&cli.BoolFlag{Name: "stdin", Usage: "after the prompt, read more messages from stdin, one per line; they queue while the agent works"},
		&cli.BoolFlag{Name: "stream", Aliases: []string{"json"}, Usage: "write JSONL events (runs and session) to stdout instead of answers"},
		&cli.BoolFlag{Name: "quiet", Aliases: []string{"q"}, Usage: "no progress on stderr"},
		&cli.BoolFlag{Name: "verbose", Usage: "also show reasoning summaries"},
		&cli.BoolFlag{Name: "last", Usage: "continue this directory's most recent session"},
		&cli.BoolFlag{Name: flagAll, Usage: "with --last: the most recent session in any directory"},
		&cli.BoolFlag{Name: flagEphemeral, Usage: "keep no session: nothing in sessions/, runs/, or the index"},
		&cli.StringFlag{Name: flagLastMessage, Aliases: []string{"o"}, Usage: "write the final answer to this file", TakesFile: true},
	)

	return &cli.Command{
		Name:      "exec",
		Aliases:   []string{"run"},
		Usage:     "run a headless session",
		ArgsUsage: "[prompt | -]",
		Description: "Sends the prompt, prints progress on stderr and each answer on stdout, and exits when\n" +
			"the session is idle. A prompt of - reads all of stdin as one message. With --stdin,\n" +
			"each further line of stdin is another message: it starts a run when the agent is idle\n" +
			"and queues while it works.\n" +
			"Resume a session with --session <id or prefix>, or --last for this directory's most recent.",
		Flags:        flags,
		OnUsageError: onUsageError,
		Action:       runAction,
	}
}

func runAction(ctx context.Context, cmd *cli.Command) error {
	prompt, err := execPrompt(cmd, os.Stdin)
	if err != nil {
		return err
	}
	followStdin := cmd.Bool("stdin")
	if prompt == "" && !followStdin {
		return cli.Exit("no prompt: pass one as an argument, - to read stdin, or use --stdin", exitUsage)
	}
	in, cleanup, err := execInputs(ctx, cmd)
	if err != nil {
		return err
	}
	defer cleanup()
	st, err := app.Setup(ctx, in, os.Stderr)
	if err != nil {
		return exitError(err)
	}
	st.Options.Source = session.SourceRun
	// Only --stream prints the answer as it arrives; otherwise it prints once.
	st.Options.Stream = cmd.Bool("stream")
	// The session gets its own context so Ctrl+C can close it gracefully.
	s, err := session.Open(context.WithoutCancel(ctx), st.Engine, st.Options)
	if err != nil {
		return cli.Exit(err.Error(), exitUsage)
	}

	out := runOutput{stdout: os.Stdout}
	switch {
	case cmd.Bool("stream"):
		out.jsonl = newJSONLWriter(os.Stdout)
	case !cmd.Bool("quiet"):
		out.progress = newPrinter(os.Stderr, cmd.Bool("verbose"))
	}
	var lines <-chan stdinLine
	if followStdin {
		lines = readLines(os.Stdin)
	}
	if prompt != "" {
		if _, err := s.Submit(prompt); err != nil {
			return fmt.Errorf("failed to send the prompt: %w", err)
		}
	}
	err = drive(ctx, s, lines, prompt != "", &out)

	return writeLastMessage(cmd.String(flagLastMessage), out.last, os.Stderr, err)
}

// drive feeds stdin lines into the session and prints its events until the
// session is idle with no more input, or the context ends. A line that
// cannot be read or sent fails the command; the work already sent goes on.
func drive(ctx context.Context, s *session.Session, lines <-chan stdinLine, busy bool, out *runOutput) error {
	events := s.Events()
	closing := false
	closeSession := func() {
		if !closing {
			closing = true
			go func() { _ = s.Close() }()
		}
	}
	if !busy && lines == nil {
		closeSession()
	}
	done := ctx.Done()
	interrupted := false
	for {
		select {
		case <-done:
			done = nil // handle the signal once
			interrupted = true
			closeSession()
		case line, ok := <-lines:
			if ok && line.err != nil {
				out.fail(fmt.Sprintf("failed to read stdin: %v", line.err))
				ok = false // nothing after it can be read
			}
			if !ok {
				lines = nil
				if !busy {
					closeSession()
				}

				continue
			}
			if strings.TrimSpace(line.text) == "" || closing {
				continue
			}
			if _, err := s.Submit(line.text); err != nil {
				out.fail(fmt.Sprintf("failed to send a message from stdin: %v", err))

				continue
			}
			busy = true
		case e, ok := <-events:
			if !ok {
				return out.exit(interrupted)
			}
			out.handle(e)
			if _, idle := e.(session.Idle); idle {
				busy = false
				if lines == nil {
					closeSession()
				}
			}
		}
	}
}

// runOutput prints events and remembers what decides the exit code.
type runOutput struct {
	stdout   io.Writer
	progress *printer
	jsonl    *jsonlWriter
	// last is the newest run's result, nil when work sent after it failed
	// (failure), so its answer is not taken for the later work's.
	last *core.Result
	// status is the newest run's status, which failed does not clear: an
	// interrupt or the disk limit keeps its own exit code.
	status core.Status
	// failure is the first error of work that did not run or did not
	// finish: a message that never reached the agent, a run that did not
	// start or ended in an error, or stdin that could not be read. It
	// fails the command even when a later run succeeds.
	failure string
}

func (o *runOutput) handle(e core.Event) {
	if o.progress != nil {
		o.progress.print(e)
	}
	if o.jsonl != nil {
		o.jsonl.write(e)
	}
	switch v := e.(type) {
	case core.RunFinished:
		result := v.Result
		o.last, o.status = &result, result.Status
		if o.jsonl == nil && result.Answer != "" {
			fmt.Fprintln(o.stdout, result.Answer)
		}
	case session.InputFailed:
		o.failed("not delivered: " + v.Reason)
	case session.Notice:
		if v.Level == session.LevelError {
			o.failed(v.Message)
		}
	}
}

// failed records an error of the work sent last.
func (o *runOutput) failed(reason string) {
	o.last = nil
	if o.failure == "" {
		o.failure = reason
	}
}

// fail reports an error of the command itself, such as unreadable stdin,
// as an error notice: on stderr, or as a JSON event with --json.
func (o *runOutput) fail(reason string) {
	o.handle(session.Notice{At: time.Now(), Level: session.LevelError, Message: reason})
}

func (o *runOutput) exit(interrupted bool) error {
	if o.jsonl != nil && o.jsonl.err != nil {
		return o.jsonl.err
	}
	switch {
	case interrupted || o.status == core.StatusInterrupted:
		return cli.Exit("", exitInterrupt)
	case o.status == core.StatusDiskLimit:
		return cli.Exit("", exitDiskLimit)
	case o.failure != "":
		if o.progress != nil {
			return cli.Exit("", exitFailed) // the progress lines said why
		}

		return cli.Exit(o.failure, exitFailed)
	case o.last == nil:
		return nil
	}
	switch o.status {
	case core.StatusOK:
		return nil
	case core.StatusRunning, core.StatusTimeout, core.StatusFailed, core.StatusInterrupted, core.StatusDiskLimit:
	}

	return cli.Exit("", exitFailed)
}

// stdinLine is a line of stdin without its line ending, or the error that
// ended reading.
type stdinLine struct {
	text string
	err  error
}

// readLines streams lines from r, of any length, and closes the channel at
// EOF. A read error is the last item.
func readLines(r io.Reader) <-chan stdinLine {
	lines := make(chan stdinLine)
	go func() {
		defer close(lines)
		br := bufio.NewReader(r)
		for {
			text, err := br.ReadString('\n')
			if text != "" {
				lines <- stdinLine{text: strings.TrimSuffix(strings.TrimSuffix(text, "\n"), "\r")}
			}
			if err != nil {
				if !errors.Is(err, io.EOF) {
					lines <- stdinLine{err: err}
				}

				return
			}
		}
	}()

	return lines
}
