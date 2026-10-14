package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/urfave/cli/v3"

	"github.com/viktordanov/uah/internal/app"
	"github.com/viktordanov/uah/internal/config"
	"github.com/viktordanov/uah/internal/home"
	"github.com/viktordanov/uah/internal/hooks"
)

// hooksCommand is `uah hooks`: list the hooks for a workspace and trust project hooks.
func hooksCommand() *cli.Command {
	flags := []cli.Flag{
		&cli.StringFlag{Name: flagWorkspace, Aliases: []string{"C"}, Usage: "the workspace whose hooks to show", DefaultText: "the current directory", TakesFile: true},
		&cli.StringFlag{Name: flagConfig, Usage: "user configuration file", Value: config.UserFile(), Sources: cli.EnvVars(home.EnvConfig), TakesFile: true},
	}

	return &cli.Command{
		Name:  "hooks",
		Usage: "list the hooks that apply to a workspace",
		Description: "Hooks come from [[hooks.<Event>]] entries in the user configuration, the layers in\n" +
			"~/.uah/config.d and UAH_EXTRA_CONFIG, and, for trusted workspaces, <workspace>/.uah/config.toml.\n" +
			"User and layer hooks run as written. Project hooks run only after `uah hooks trust`\n" +
			"records their exact commands, and the content of a local script a command runs;\n" +
			"a changed command or script needs trust again.",
		Flags:        flags,
		OnUsageError: onUsageError,
		Action:       listHooks,
		Commands: []*cli.Command{{
			Name:         "trust",
			Usage:        "allow this workspace's project hooks to run",
			Flags:        flags,
			OnUsageError: onUsageError,
			Action:       trustHooks,
		}},
	}
}

func loadHooks(cmd *cli.Command) (string, *hooks.Runner, *hooks.Trust, error) {
	dir := cmd.String(flagWorkspace)
	if dir == "" {
		dir = "."
	}
	workspace, err := filepath.Abs(dir)
	if err != nil {
		return "", nil, nil, fmt.Errorf("failed to resolve workspace: %w", err)
	}
	cfg, _, err := config.Load(cmd.String(flagConfig), workspace)
	if err != nil {
		return "", nil, nil, cli.Exit(err.Error(), exitUsage)
	}
	list, err := cfg.HookList()
	if err != nil {
		return "", nil, nil, cli.Exit(err.Error(), exitUsage)
	}
	trust, err := hooks.LoadTrust(app.HookTrustFile())
	if err != nil {
		return "", nil, nil, err
	}
	runner, err := hooks.New(list, trust, workspace)
	if err != nil {
		return "", nil, nil, cli.Exit(err.Error(), exitUsage)
	}

	return workspace, runner, trust, nil
}

func listHooks(_ context.Context, cmd *cli.Command) error {
	workspace, runner, _, err := loadHooks(cmd)
	if err != nil {
		return err
	}
	list := runner.Hooks()
	if len(list) == 0 {
		fmt.Fprintln(os.Stderr, "no hooks for "+workspace)

		return nil
	}
	var t table
	t.header("EVENT", "MATCHER", "SOURCE", "STATE", "COMMAND")
	for _, h := range list {
		state := trustLabel(runner, h)
		if ok, _ := runner.TrustState(h); ok {
			state = cliStyles.ok.Render(state)
		} else {
			state = cliStyles.warn.Render(state)
		}
		t.row(nil, string(h.Event), h.Matcher, string(h.Source), state, oneLine(h.Command, 80))
	}

	return t.write(os.Stdout)
}

// trustLabel is a hook's STATE column.
func trustLabel(runner *hooks.Runner, h hooks.Hook) string {
	switch ok, why := runner.TrustState(h); {
	case ok:
		return "runs"
	case why == hooks.ReasonScriptChanged:
		return "untrusted (the script changed)"
	case why == hooks.ReasonScriptNew:
		return "untrusted (script not recorded)"
	default:
		return untrusted
	}
}

func trustHooks(_ context.Context, cmd *cli.Command) error {
	workspace, runner, trust, err := loadHooks(cmd)
	if err != nil {
		return err
	}
	var commands []string
	for _, h := range runner.Hooks() {
		if !runner.Trusted(h) {
			commands = append(commands, h.Command)
			fmt.Printf("trusting %s hook: %s\n", h.Event, h.Command)
		}
	}
	if len(commands) == 0 {
		fmt.Fprintln(os.Stderr, "no untrusted project hooks for "+workspace)

		return nil
	}

	return trust.Allow(workspace, commands...)
}
