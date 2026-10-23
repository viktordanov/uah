package main

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/internal/store"
)

// flagAll turns off the current-directory filter.
const (
	flagAll       = "all"
	flagWorkspace = "workspace"
	flagJSON      = "json"
)

// resumeCommand is `uah resume`, modeled on `codex resume`: a picker of this
// directory's sessions, a session by ID, or the most recent with --last.
func resumeCommand() *cli.Command {
	return &cli.Command{
		Name:      "resume",
		Usage:     "resume a session (picker by default; --last continues the most recent)",
		ArgsUsage: "[session id or prefix] [prompt]",
		Description: "Without an ID, opens the session picker for the current directory.\n" +
			"--last continues this directory's most recent session. --all includes every directory.",
		Flags: append(sessionFlags(),
			&cli.BoolFlag{Name: "last", Usage: "continue the most recent session without the picker"},
			&cli.BoolFlag{Name: flagAll, Usage: "include sessions from every directory"},
		),
		OnUsageError: onUsageError,
		Action:       resumeAction,
	}
}

func resumeAction(ctx context.Context, cmd *cli.Command) error {
	if cmd.String(flagSessionID) != "" {
		return cli.Exit("uah resume continues a session; --session-id starts a new one", exitUsage)
	}
	args := cmd.Args().Slice()
	launch := tuiLaunch{all: cmd.Bool(flagAll)}
	switch {
	case cmd.Bool("last"):
		info, err := latestSession(ctx, cmd, true)
		if err != nil {
			return err
		}
		launch.sessionRef, launch.prompt = info.ID, strings.Join(args, " ")
	case len(args) > 0:
		launch.sessionRef, launch.prompt = args[0], strings.Join(args[1:], " ")
	default:
		launch.picker = true
	}

	return openTUI(ctx, cmd, launch)
}

// latestSession is the most recent session in the current directory, or in
// any directory with --all.
//
// A subagent's or reviewer's session is never the latest. interactiveOnly
// also skips sessions started by `uah exec`, as `codex resume` skips
// `codex exec` sessions.
func latestSession(ctx context.Context, cmd *cli.Command, interactiveOnly bool) (session.Info, error) {
	stateDir, err := filepath.Abs(cmd.String("state-dir"))
	if err != nil {
		return session.Info{}, fmt.Errorf("failed to resolve state dir: %w", err)
	}
	infos, err := store.List(ctx, stateDir)
	if err == nil {
		infos, err = session.WithHistory(stateDir, infos) // history with no run yet, such as a converted session's
	}
	if err != nil {
		return session.Info{}, err
	}
	cwd, err := currentDir(cmd)
	if err != nil {
		return session.Info{}, err
	}
	if !cmd.Bool(flagAll) {
		infos = session.InDir(infos, cwd)
	}
	if interactiveOnly {
		infos = session.Interactive(infos)
	} else {
		infos = slices.DeleteFunc(infos, func(in session.Info) bool { return in.Source == session.SourceSubagent })
	}
	if len(infos) == 0 {
		where := "in " + cwd
		if cmd.Bool(flagAll) {
			where = "at all"
		}

		return session.Info{}, cli.Exit(fmt.Sprintf("no session to resume %s (try --all)", where), exitUsage)
	}

	return infos[0], nil
}
