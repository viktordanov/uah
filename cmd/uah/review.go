package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/viktordanov/uagent/core"
	"github.com/viktordanov/uagent/stream"

	"github.com/viktordanov/uah/internal/app"
	"github.com/viktordanov/uah/internal/cmdparse"
	"github.com/viktordanov/uah/internal/codereview"
	"github.com/viktordanov/uah/internal/session"
)

const (
	commandReview   = "review"
	flagUncommitted = "uncommitted"
	flagBase        = "base"
	flagCommit      = "commit"
	flagTitle       = "title"
)

// reviewCommand is `uah review`: /review without the TUI, as `codex review`
// is Codex's /review without its TUI, with Codex's targets and flags.
func reviewCommand() *cli.Command {
	flags := append(sessionFlags(),
		&cli.BoolFlag{Name: flagUncommitted, Usage: "review the staged, unstaged, and untracked changes"},
		&cli.StringFlag{Name: flagBase, Usage: "review the changes against this base branch"},
		&cli.StringFlag{Name: flagCommit, Usage: "review the changes a commit introduced"},
		&cli.StringFlag{Name: flagTitle, Usage: "with --commit: the commit's title, for the review's hint"},
		&cli.BoolFlag{Name: flagJSON, Usage: "print the review as JSON: the findings, the verdict, and the model, effort, and tokens"},
		&cli.BoolFlag{Name: "quiet", Aliases: []string{"q"}, Usage: "no progress on stderr"},
		&cli.BoolFlag{Name: flagEphemeral, Usage: "keep no session: nothing in sessions/, runs/, or the index"},
		&cli.StringFlag{Name: flagLastMessage, Aliases: []string{"o"}, Usage: "write the review's text to this file", TakesFile: true},
	)

	return &cli.Command{
		Name:      commandReview,
		Usage:     "review changes with a read-only reviewer, as /review does",
		ArgsUsage: "[instructions | -]",
		Description: "Runs /review without the terminal UI: a read-only reviewer with Codex's rubric looks at\n" +
			"the uncommitted changes (--uncommitted), the changes against a base branch (--base), one\n" +
			"commit (--commit), or what custom instructions say (- reads them from stdin). It prints\n" +
			"progress on stderr and the review on stdout: the findings counted by priority, the verdict,\n" +
			"and the confidence, the explanation, then each finding by priority and confidence with its\n" +
			"place; -o writes Codex's text, and --json one JSON line. It exits 0 when the reviewer\n" +
			"answered, whatever it found.",
		Flags:        flags,
		OnUsageError: onUsageError,
		Action:       reviewAction,
	}
}

func reviewAction(ctx context.Context, cmd *cli.Command) error {
	target, err := reviewTarget(cmd, os.Stdin)
	if err != nil {
		return err
	}
	if cmd.String("session") != "" {
		return cli.Exit("uah review starts its own session; it cannot resume one", exitUsage)
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
	// The session gets its own context: Ctrl+C stops the review, which
	// then reports itself interrupted.
	s, err := session.Open(context.WithoutCancel(ctx), st.Engine, st.Options)
	if err != nil {
		return cli.Exit(err.Error(), exitUsage)
	}
	home, _ := os.UserHomeDir()
	out := reviewOutput{stdout: os.Stdout, stderr: os.Stderr, json: cmd.Bool(flagJSON), env: cmdparse.Env{Workspace: st.Options.Settings.Workspace, Home: home}}
	if !cmd.Bool("quiet") {
		out.progress = newPrinter(os.Stderr, false)
	}
	err = awaitSessionReview(ctx, s, target, &out)
	go func() {
		// Drained, so the session's loop never blocks while it closes.
		for e := range s.Events() {
			_ = e
		}
	}()
	_ = s.Close()
	if err != nil {
		return exitError(err)
	}

	return out.finish(cmd.String(flagLastMessage))
}

// reviewTarget is what the flags and arguments ask to review, refused as
// Codex refuses it: one target at most, --title only with --commit, and
// no empty instructions.
func reviewTarget(cmd *cli.Command, stdin io.Reader) (codereview.Target, error) {
	text := strings.TrimSpace(strings.Join(cmd.Args().Slice(), " "))
	if text == "-" {
		data, err := io.ReadAll(stdin)
		if err != nil {
			return codereview.Target{}, fmt.Errorf("failed to read the instructions from stdin: %w", err)
		}
		text = strings.TrimSpace(string(data))
		if text == "" {
			return codereview.Target{}, cli.Exit(codereview.ErrEmpty.Error(), exitUsage)
		}
	}
	var targets []codereview.Target
	if cmd.Bool(flagUncommitted) {
		targets = append(targets, codereview.Target{Kind: codereview.Uncommitted})
	}
	if b := cmd.String(flagBase); b != "" {
		targets = append(targets, codereview.Target{Kind: codereview.BaseBranch, Branch: b})
	}
	if c := cmd.String(flagCommit); c != "" {
		targets = append(targets, codereview.Target{Kind: codereview.Commit, SHA: c, Title: cmd.String(flagTitle)})
	}
	if text != "" {
		targets = append(targets, codereview.Target{Kind: codereview.Custom, Instructions: text})
	}
	switch {
	case cmd.String(flagTitle) != "" && cmd.String(flagCommit) == "":
		return codereview.Target{}, cli.Exit("--title needs --commit", exitUsage)
	case len(targets) > 1:
		return codereview.Target{}, cli.Exit("review one target: --uncommitted, --base, --commit, or custom review instructions", exitUsage)
	case len(targets) == 0:
		return codereview.Target{}, cli.Exit("Specify --uncommitted, --base, --commit, or provide custom review instructions", exitUsage)
	}

	return targets[0], nil
}

// awaitSessionReview runs the review in s and hands its events to out
// until it finishes. Ctrl+C (ctx) stops the review.
func awaitSessionReview(ctx context.Context, s *session.Session, target codereview.Target, out *reviewOutput) error {
	errc := make(chan error, 1)
	go func() { errc <- s.Review(ctx, target) }()
	events := s.Events()
	for {
		select {
		case err := <-errc:
			if err != nil {
				return err // refused before it started
			}
			errc = nil
		case e, ok := <-events:
			if !ok {
				return errors.New("the session closed before the review finished")
			}
			if out.handle(e) {
				return nil
			}
		}
	}
}

// reviewOutput prints a review's progress and its result.
type reviewOutput struct {
	stdout, stderr io.Writer
	progress       *printer
	json           bool
	// env is where the reviewer's commands run, for their paths.
	env cmdparse.Env
	// calls are the reviewer's running calls, by ID.
	calls    map[string]*reviewCall
	seq      int
	started  session.ReviewStarted
	finished session.ReviewFinished
}

// handle takes one session event and reports whether the review finished.
func (o *reviewOutput) handle(e core.Event) bool {
	switch e := e.(type) {
	case session.ReviewStarted:
		o.started = e
		if o.progress != nil {
			o.progress.say(fmt.Sprintf("uah review · %s · %s", e.Hint, reviewerLabel(e.Model, e.Effort)))
		}
	case session.ReviewActivity:
		if o.progress != nil {
			o.step(e.Event)
		}
	case session.Notice:
		if o.progress != nil && e.Level != session.LevelInfo {
			o.progress.print(e)
		}
	case session.ReviewFinished:
		o.finished = e
		if o.progress != nil {
			o.flush()
		}

		return true
	}

	return false
}

// finish prints the review (as text or JSON), writes its text to the -o
// file, and says how it ended in the exit code: 0 when the reviewer
// answered, 130 when it was interrupted, 1 when it failed.
func (o *reviewOutput) finish(lastMessage string) error {
	f := o.finished
	if o.progress != nil {
		o.progress.say("review: " + o.summary())
	}
	text := ""
	if f.Err == "" && !f.Interrupted {
		text = f.Output.Text()
	}
	if o.json {
		// One line, as JSON lines: agentbench reads it as a stream.
		if err := json.NewEncoder(o.stdout).Encode(o.document()); err != nil {
			return fmt.Errorf("failed to write the review: %w", err)
		}
	} else if text != "" {
		o.print(reviewWriter(o.stdout, stdoutTTY(o.stdout), os.Environ()))
	}
	if lastMessage != "" {
		if text == "" {
			fmt.Fprintf(o.stderr, "uah: no review; wrote an empty %s\n", lastMessage)
		}
		if err := os.WriteFile(lastMessage, []byte(text), 0o600); err != nil {
			return fmt.Errorf("failed to write the review: %w", err)
		}
	}
	switch {
	case f.Interrupted:
		return cli.Exit("", exitInterrupt)
	case f.Err != "":
		return cli.Exit("the review failed: "+f.Err, exitFailed)
	}

	return nil
}

// summary is the review's last progress line: its findings by priority,
// the verdict, the confidence, how long it took, what it ran on, and the
// tokens it used.
func (o *reviewOutput) summary() string {
	f := o.finished
	var parts []string
	switch {
	case f.Interrupted:
		parts = append(parts, "interrupted")
	case f.Err != "":
		parts = append(parts, "failed")
	default:
		parts = append(parts, f.Output.Counts())
		if v, _ := f.Output.Verdict(); v != "" {
			parts = append(parts, v)
		}
		if c, ok := f.Output.Confidence(); ok {
			parts = append(parts, "confidence "+codereview.Percent(c))
		}
	}
	t := f.Tokens

	return strings.Join(append(parts,
		fmt.Sprintf("%.1fs", f.At.Sub(o.started.At).Seconds()),
		reviewerLabel(o.started.Model, o.started.Effort),
		fmt.Sprintf("%s in (%s cached) · %s out tokens", commas(t.InputTokens), commas(t.CachedInputTokens), commas(t.OutputTokens)),
	), " · ")
}

// reviewJSON is `uah review --json`: Codex's review output (the findings,
// the verdict, its explanation, and the confidence), with how the review
// ended and what it ran on and used, its tokens in `usage` as uah exec
// --json reports a model response's.
type reviewJSON struct {
	codereview.Output

	Target     string           `json:"target"`
	Status     string           `json:"status"` // ok, interrupted, or failed
	Error      string           `json:"error,omitempty"`
	Model      string           `json:"model"`
	Effort     string           `json:"effort"`
	DurationMS int64            `json:"duration_ms"`
	Usage      stream.TokensDTO `json:"usage"`
}

func (o *reviewOutput) document() reviewJSON {
	f, t := o.finished, o.finished.Tokens
	doc := reviewJSON{
		Target: o.started.Hint, Status: "ok", Error: f.Err, Model: o.started.Model, Effort: o.started.Effort,
		DurationMS: f.At.Sub(o.started.At).Milliseconds(), Output: f.Output,
		Usage: stream.TokensDTO{
			Input: t.InputTokens, CachedInput: t.CachedInputTokens, CacheWriteInput: t.CacheWriteInputTokens,
			Output: t.OutputTokens, Reasoning: t.ReasoningTokens,
		},
	}
	switch {
	case f.Interrupted:
		doc.Status = "interrupted"
	case f.Err != "":
		doc.Status = "failed"
	}
	if doc.Findings == nil {
		doc.Findings = []codereview.Finding{}
	}

	return doc
}

// stdoutTTY reports whether w is a terminal.
func stdoutTTY(w io.Writer) bool {
	f, ok := w.(*os.File)

	return ok && isTerminal(f)
}

// reviewerLabel is "gpt-6-astra, effort high".
func reviewerLabel(model, effort string) string {
	if effort == "" {
		return modelLabel(model)
	}

	return modelLabel(model) + ", effort " + effort
}
