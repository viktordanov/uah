package main

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/viktordanov/uah/internal/app"
	"github.com/viktordanov/uah/internal/approval"
	"github.com/viktordanov/uah/internal/home"
	"github.com/viktordanov/uah/internal/models"
	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/internal/store"
)

// completeFlag is the shell-completion flag urfave/cli adds to every call
// the completion scripts make.
const completeFlag = "--generate-shell-completion"

// withCompletion sets every command's completer: flag values and session
// IDs, and otherwise urfave/cli's commands and flags.
func withCompletion(cmd *cli.Command) *cli.Command {
	cmd.ShellComplete = complete
	for _, sub := range cmd.Commands {
		withCompletion(sub)
	}

	return cmd
}

func complete(ctx context.Context, cmd *cli.Command) {
	if values := completionValues(ctx, cmd, previousWord(os.Args)); values != nil {
		for _, v := range values {
			fmt.Fprintln(cmd.Root().Writer, v)
		}

		return
	}
	cli.DefaultCompleteWithFlags(ctx, cmd)
}

// previousWord is the word before the one being completed: the argument just
// before the completion flag.
func previousWord(args []string) string {
	i := slices.Index(args, completeFlag)
	if i < 2 {
		return ""
	}

	return args[i-1]
}

// completionValues lists the values the word after prev can take, or nil
// when prev is not a flag with known values.
func completionValues(ctx context.Context, cmd *cli.Command, prev string) []string {
	switch strings.TrimLeft(prev, "-") {
	case "provider":
		return session.Providers
	case "effort", "e":
		return session.Efforts
	case "sandbox":
		return sandboxModes()
	case "ask":
		return approval.Policies
	case "log-level":
		return slices.Sorted(maps.Keys(app.LogLevels))
	case "session", "s":
		return sessionIDs(ctx, cmd)
	case "model", "m":
		return modelIDs(cmd)
	}

	return argValues(ctx, cmd)
}

// argValues lists a show command's arguments: the built-in prompts for
// `uah prompts show`, else session IDs.
func argValues(ctx context.Context, cmd *cli.Command) []string {
	if cmd.Name != subShow {
		return nil
	}
	if lineage := cmd.Lineage(); len(lineage) > 1 && lineage[1].Name == promptsName {
		return promptNames()
	}

	return sessionIDs(ctx, cmd)
}

// completionFlag is a flag's value in completion mode, which skips flag
// sources, so the variable is read directly.
func completionFlag(cmd *cli.Command, name, env, fallback string) string {
	v := cmd.String(name)
	if e := os.Getenv(env); e != "" && !cmd.IsSet(name) {
		v = e
	}
	if v == "" {
		v = fallback
	}

	return v
}

// completionStateDir is the state directory, absolute ("" when it cannot be).
func completionStateDir(cmd *cli.Command) string {
	abs, err := filepath.Abs(completionFlag(cmd, "state-dir", home.EnvStateDir, home.Dir()))
	if err != nil {
		return ""
	}

	return abs
}

// modelIDs are the provider's models from the cache at any age, else the
// bundled list: no credentials are read and no request is made.
func modelIDs(cmd *cli.Command) []string {
	provider := completionFlag(cmd, "provider", app.EnvProvider, app.CodexProvider)
	dir := completionStateDir(cmd)
	if dir != "" {
		dir = models.CacheDir(dir)
	}
	m := models.New(models.Options{Dir: dir})
	ids := m.Cached(provider).IDs()
	if ids == nil {
		return []string{}
	}

	return ids
}

// sessionIDs are the known sessions' short IDs, most recent first.
func sessionIDs(ctx context.Context, cmd *cli.Command) []string {
	abs := completionStateDir(cmd)
	if abs == "" {
		return []string{}
	}
	infos, err := store.List(ctx, abs)
	if err == nil {
		infos, err = session.WithHistory(abs, infos)
	}
	if err != nil {
		return []string{}
	}
	ids := make([]string, 0, len(infos))
	for _, in := range infos {
		ids = append(ids, short(in.ID))
	}

	return ids
}
