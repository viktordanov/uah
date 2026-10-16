// Command uah is a general-purpose agent harness built on uah-core: long-lived
// sessions, a terminal UI, and headless runs with uagent's guards.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime/debug"
	"slices"
	"syscall"

	"github.com/urfave/cli/v3"

	"github.com/viktordanov/uah/internal/home"
	"github.com/viktordanov/uah/internal/home/migrate"
)

const (
	exitOK     = 0
	exitFailed = 1
	exitUsage  = 2
)

// version is set with -ldflags "-X main.version=..."; go install builds use the module version.
var version = "dev"

func main() {
	// `uah tool` runs inside a session's commands, maybe in a read-only
	// sandbox: it skips startup, which may move files, and every
	// configuration.
	if len(os.Args) > 1 && os.Args[1] == "tool" {
		os.Exit(runTool(context.Background(), os.Args[2:], os.Stdin, os.Stdout, os.Stderr, os.Getenv))
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	ctx = context.WithValue(ctx, startupKey{}, startup(ctx, os.Stderr, os.Args))
	err := newApp().Run(ctx, os.Args)
	stop()
	os.Exit(exitCode(err))
}

func newApp() *cli.Command {
	return withCompletion(&cli.Command{
		Name:    "uah",
		Usage:   "a general-purpose agent harness built on uah-core",
		Version: buildVersion(),
		// Errors are printed once, by main, with the right exit code.
		ExitErrHandler: func(context.Context, *cli.Command, error) {},
		OnUsageError:   onUsageError,
		ArgsUsage:      "[prompt]",
		Description: "Without a command, uah opens the terminal UI: a live session you can steer.\n" +
			"enter sends (while the agent works, before its next model request), tab queues for the\n" +
			"end of the run, esc esc interrupts,\n" +
			"/help lists commands. Resume with `uah resume`, --session <id or prefix>, or ctrl+s inside.",
		Flags:  sessionFlags(),
		Action: tuiAction,
		// `uah completion bash|zsh|fish` prints the script; the scripts ask
		// uah itself for completions.
		EnableShellCompletion: true,
		ConfigureShellCompletionCommand: func(c *cli.Command) {
			c.Hidden = false
			c.Usage = "print the shell completion script: bash, zsh, fish, or pwsh"
		},
		Commands: []*cli.Command{runCommand(), reviewCommand(), resumeCommand(), sessionsCommand(), statsCommand(), hooksCommand(), contextCommand(), configCommand(), doctorCommand(), modelsCommand(), usageCommand(), mcpCommand(), promptsCommand(), compactionCommand(), toolCommand()},
	})
}

// startupKey carries startup's lines to the TUI, whose screen hides stderr.
type startupKey struct{}

// startup warns about variables uah no longer reads and, once, copies the
// old folders into ~/.uah, before any command reads or creates a file. It
// prints each line to w and returns them. Shell completion migrates quietly.
func startup(ctx context.Context, w io.Writer, args []string) []string {
	lines := home.Warnings(os.Getenv)
	migrated, err := migrate.Auto(ctx, os.Getenv)
	switch {
	case err != nil:
		lines = append(lines, "could not move your config and sessions to ~/.uah (the old folders are untouched): "+err.Error())
	case migrated:
		lines = append(lines, migrate.Done)
	}
	if slices.Contains(args, completeFlag) {
		return nil
	}
	for _, line := range lines {
		fmt.Fprintf(w, "uah: %s\n", line)
	}

	return lines
}

// exitCode maps the app error to a process exit code and prints it once.
func exitCode(err error) int {
	if err == nil {
		return exitOK
	}
	code := exitFailed
	if exitErr, ok := errors.AsType[cli.ExitCoder](err); ok {
		code = exitErr.ExitCode()
	}
	if msg := err.Error(); msg != "" {
		fmt.Fprintf(os.Stderr, "uah: %s\n", msg)
	}

	return code
}

func onUsageError(_ context.Context, _ *cli.Command, err error, _ bool) error {
	return cli.Exit(fmt.Sprintf("%v (see --help)", err), exitUsage)
}

func buildVersion() string {
	if version != "dev" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}

	return version
}
