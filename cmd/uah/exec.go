package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/app"
	"github.com/viktordanov/uah/internal/citations"
)

const (
	flagEphemeral   = "ephemeral"
	flagLastMessage = "output-last-message"
)

// execPrompt is the first message: the arguments, or all of stdin for a
// prompt of "-", as `codex exec -` reads it.
func execPrompt(cmd *cli.Command, stdin io.Reader) (string, error) {
	args := cmd.Args().Slice()
	if len(args) != 1 || args[0] != "-" {
		return strings.TrimSpace(strings.Join(args, " ")), nil
	}
	if cmd.Bool("stdin") {
		return "", cli.Exit("a prompt of - reads all of stdin; it cannot be used with --stdin", exitUsage)
	}
	data, err := io.ReadAll(stdin)
	if err != nil {
		return "", fmt.Errorf("failed to read the prompt from stdin: %w", err)
	}
	prompt := strings.TrimSpace(string(data))
	if prompt == "" {
		return "", cli.Exit("no prompt on stdin", exitUsage)
	}

	return prompt, nil
}

// execInputs are the session flags with the session to resume (--session
// or --last). With --ephemeral, the session's files go to a temporary
// directory that cleanup removes, so nothing is kept.
func execInputs(ctx context.Context, cmd *cli.Command) (app.Inputs, func(), error) {
	in := inputs(cmd)
	ephemeral := cmd.Bool(flagEphemeral)
	if ephemeral && (in.SessionRef != "" || cmd.Bool("last")) {
		// Checked first: finding a session updates the index.
		return in, nil, cli.Exit("--ephemeral starts a new session; it cannot resume one", exitUsage)
	}
	if cmd.Bool("last") {
		info, err := latestSession(ctx, cmd, false)
		if err != nil {
			return in, nil, err
		}
		in.SessionRef = info.ID
	}
	if !ephemeral {
		return in, func() {}, nil
	}
	dir, err := os.MkdirTemp("", "uah-ephemeral-")
	if err != nil {
		return in, nil, fmt.Errorf("failed to create a temporary directory: %w", err)
	}
	in.RunStateDir = dir

	return in, func() { _ = os.RemoveAll(dir) }, nil
}

// writeLastMessage writes the last run's answer to path, as `codex exec -o`
// does: an empty file, with a warning, when no run answered. It returns
// runErr, or the write's error when the run succeeded.
func writeLastMessage(path string, last *core.Result, stderr io.Writer, runErr error) error {
	if path == "" {
		return runErr
	}
	answer := ""
	if last != nil {
		answer = citations.Strip(last.Answer)
	}
	if answer == "" {
		fmt.Fprintf(stderr, "uah: no final answer; wrote an empty %s\n", path)
	}
	if err := os.WriteFile(path, []byte(answer), 0o600); err != nil && runErr == nil {
		return fmt.Errorf("failed to write the last message: %w", err)
	}

	return runErr
}
