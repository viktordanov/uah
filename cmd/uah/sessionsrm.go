package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/urfave/cli/v3"

	"github.com/viktordanov/uagent/harness"

	"github.com/viktordanov/uah/internal/app"
	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/internal/store"
)

// sessionsRmCommand is `uah sessions rm`: delete a session, its run
// records, its index rows, and its subagents.
func sessionsRmCommand(stateDir cli.Flag) *cli.Command {
	return &cli.Command{
		Name:      "rm",
		Usage:     "delete a session with its runs and subagents",
		ArgsUsage: "<id or unique prefix>",
		Description: "Removes the session's files in sessions/, its tool output, its run records, its rows\n" +
			"in the index, and the same for each subagent it started. It refuses while a run holds\n" +
			"the session's lock; --force removes it anyway.",
		Flags: []cli.Flag{
			stateDir,
			&cli.BoolFlag{Name: flagJSON, Usage: "print the removed paths as JSON"},
			&cli.BoolFlag{Name: "force", Usage: "remove the session even while a run holds its lock"},
			&cli.BoolFlag{Name: flagDryRun, Usage: "print what would be removed, and remove nothing"},
		},
		OnUsageError: onUsageError,
		Action:       removeSession,
	}
}

func removeSession(ctx context.Context, cmd *cli.Command) error {
	if cmd.Args().Len() != 1 {
		return cli.Exit("usage: uah sessions rm <id or unique prefix>", exitUsage)
	}
	stateDir, err := filepath.Abs(cmd.String("state-dir"))
	if err != nil {
		return fmt.Errorf("failed to resolve state dir: %w", err)
	}
	r, err := planRemoval(ctx, stateDir, cmd.Args().First())
	if err != nil {
		return exitError(err)
	}
	unlock, err := r.Lock(cmd.Bool("force"))
	if errors.Is(err, harness.ErrSessionBusy) {
		return cli.Exit(fmt.Sprintf("%v; --force removes it anyway", err), exitFailed)
	}
	if err != nil {
		return err
	}
	defer unlock()
	dry := cmd.Bool(flagDryRun)
	if !dry {
		if err := r.Remove(); err != nil {
			return err
		}
		if err := store.ForgetIn(ctx, stateDir, r.IDs); err != nil {
			fmt.Fprintf(os.Stderr, "uah: the index drops the removed sessions when it next opens: %v\n", err)
		}
	}
	if cmd.Bool(flagJSON) {
		return writeJSON(os.Stdout, struct {
			Sessions []string `json:"sessions"`
			Paths    []string `json:"paths"`
			DryRun   bool     `json:"dry_run"`
		}{r.IDs, r.Paths, dry})
	}
	for _, p := range r.Paths {
		fmt.Println(p)
	}
	verb := "removed"
	if dry {
		verb = "would remove"
	}
	fmt.Fprintf(os.Stderr, "%s session %s with %d subagent(s): %d path(s)\n", verb, r.IDs[0], len(r.IDs)-1, len(r.Paths))

	return nil
}

// planRemoval plans the removal of the session with this exact ID, or else
// of the one session whose ID starts with ref.
func planRemoval(ctx context.Context, stateDir, ref string) (session.Removal, error) {
	r, err := session.PlanRemoval(stateDir, ref)
	if !errors.Is(err, session.ErrNoSession) {
		return r, err
	}
	info, err := app.FindSession(ctx, stateDir, ref)
	if err != nil {
		return session.Removal{}, err
	}

	return session.PlanRemoval(stateDir, info.ID)
}
