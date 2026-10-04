package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/viktordanov/uah/internal/app"
	"github.com/viktordanov/uah/internal/approval"
	"github.com/viktordanov/uah/internal/config"
	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/home"
	"github.com/viktordanov/uah/internal/sandbox"
	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/internal/toolpolicy"
)

// sessionFlags are shared by the TUI and `uah exec`.
func sessionFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Name: "provider", Usage: "LLM provider: " + strings.Join(session.Providers, ", "),
			DefaultText: app.CodexProvider + ", or the resumed session's", Sources: cli.EnvVars(app.EnvProvider),
			Validator: oneOf("provider", session.Providers),
		},
		&cli.StringFlag{
			Name: "model", Aliases: []string{"m"}, Usage: "model ID",
			DefaultText: app.DefaultCodexModel + " on openai-codex and openai when your login lists it, else " + app.FallbackCodexModel + " (openai: gpt-6-astra); or the resumed session's", Sources: cli.EnvVars(app.EnvModel),
		},
		&cli.StringFlag{
			Name: "effort", Aliases: []string{"e"}, Usage: "thinking level: " + strings.Join(session.Efforts, ", "),
			DefaultText: app.DefaultEffort + ", or the resumed session's", Validator: oneOf("effort", session.Efforts),
		},
		&cli.StringFlag{
			Name: flagWorkspace, Aliases: []string{"C"}, Usage: "agent workspace and Bash working directory",
			DefaultText: "the current directory, or the resumed session's", TakesFile: true,
		},
		&cli.StringFlag{Name: "session", Aliases: []string{"s"}, Usage: "resume a session by ID or unique ID prefix"},
		&cli.StringFlag{Name: flagSessionID, Usage: "start a new session with this ID, a UUID; an ID that exists is an error"},
		&cli.StringFlag{
			Name: "state-dir", Usage: "sessions, logs, and run records; must be outside the workspace",
			Value: home.Dir(), Sources: cli.EnvVars(home.EnvStateDir), TakesFile: true,
		},
		&cli.BoolFlag{Name: "fast", Usage: "priority processing (service_tier priority; openai and openai-codex)"},
		&cli.StringFlag{
			Name: "adaptive-effort", Usage: "think less on follow-up turns after tool results: off, 1-step, or 2-steps (effort levels down)",
			DefaultText: "off, or the resumed session's", Sources: cli.EnvVars(app.EnvAdaptiveEffort),
			Validator: oneOf("adaptive-effort", session.AdaptiveEfforts),
		},
		&cli.StringFlag{
			Name: "model-verbosity", Usage: "how much the model writes (the API's text.verbosity): " + strings.Join(app.Verbosities, ", ") + "; only for a model that supports it",
			DefaultText: "the model's, low on gpt-6.1-sol", Sources: cli.EnvVars(app.EnvModelVerbosity),
			Validator: oneOf("model-verbosity", app.Verbosities),
		},
		&cli.StringFlag{
			Name: "sandbox", Usage: "where commands may write: read-only or workspace-write (no sandbox is --yolo)",
			DefaultText: "workspace-write", Sources: cli.EnvVars(app.EnvSandbox), Validator: oneOf("sandbox", sandboxModes()),
		},
		&cli.BoolFlag{
			Name: flagYolo, Aliases: []string{"dangerously-bypass-approvals-and-sandbox"},
			Usage: "yolo mode: no sandbox and no approvals, so every command runs unasked (forbid rules still refuse); " +
				"shift+tab then cycles through yolo too. DANGEROUS: for machines sandboxed from outside",
		},
		&cli.StringFlag{
			Name: "ask", Usage: "approval policy: on-request (ask before running a command outside the sandbox) or never (deny such commands)",
			DefaultText: "on-request", Sources: cli.EnvVars(app.EnvAsk), Validator: oneOf("ask", approval.Policies),
		},
		&cli.StringFlag{
			Name: "base-url", Usage: "LLM base URL override", DefaultText: "provider default",
			Sources: cli.EnvVars("UAH_LLM_BASE_URL"),
		},
		&cli.StringFlag{
			Name: "max-disk", Usage: "stop a run when tool output exceeds this size, e.g. 500M (0 disables)", Value: "5G",
			Validator: func(v string) error {
				_, err := app.ParseSize(v)

				return err
			},
		},
		&cli.IntFlag{
			Name: "max-attempts", Usage: "send a model request this many times before the run fails, retrying a lost connection with backoff",
			DefaultText: strconv.Itoa(engine.DefaultMaxAttempts), Sources: cli.EnvVars(app.EnvMaxAttempts),
			Validator: func(n int) error {
				if n < 1 {
					return fmt.Errorf("invalid max-attempts %d (want 1 or more)", n)
				}

				return nil
			},
		},
		&cli.BoolFlag{Name: "allow-dotenv", Usage: "run even if the workspace .env sets risky variables"},
		&cli.StringFlag{
			Name: flagConfig, Usage: "user configuration file", Value: config.UserFile(),
			Sources: cli.EnvVars(home.EnvConfig), TakesFile: true,
		},
		&cli.BoolFlag{Name: "no-instructions", Usage: "do not load AGENTS.md or CLAUDE.md files"},
		&cli.BoolFlag{
			Name:  flagNoContextPreparation,
			Usage: "start new sessions without prepared context (the environment, sandbox, workspace, agent files, and harness); " + app.EnvContextPreparation + "=off does the same",
		},
		&cli.StringFlag{
			Name: flagTools, Usage: "the only tools the model may use, comma-separated: built-in names, mcp__<server>__<tool>, or mcp__<server>__* " +
				`("" allows none); narrows [tools] allow, never widens it`,
			DefaultText: "every tool [tools] allows",
		},
		&cli.StringFlag{Name: flagDenyTools, Usage: "tools the model may never use, comma-separated, besides [tools] deny"},
		&cli.BoolFlag{Name: "no-skills", Usage: "discover and offer no skills; [skills] enabled = false does the same"},
		&cli.StringFlag{Name: "log-level", Usage: "diagnostic log level: debug, info, warn, error", Value: "warn", Validator: oneOfMap("log-level", app.LogLevels)},
	}
}

// The tool policy's flags.
const (
	flagTools     = "tools"
	flagDenyTools = "deny-tools"
)

// flagSessionID is --session-id, the ID of a new session.
const flagSessionID = "session-id"

// flagNoContextPreparation is --no-context-preparation.
const flagNoContextPreparation = "no-context-preparation"

// flagYolo is --yolo, Codex's --dangerously-bypass-approvals-and-sandbox.
const flagYolo = "yolo"

// setupFor sets up a session for ref ("" starts a new one) from the flags,
// mapping usage errors to exitUsage.
func setupFor(ctx context.Context, cmd *cli.Command, logOutput io.Writer, ref string) (app.Result, error) {
	in := inputs(cmd)
	in.SessionRef = ref

	return setupWith(ctx, in, logOutput)
}

// setupWith is setupFor with the inputs already collected.
func setupWith(ctx context.Context, in app.Inputs, logOutput io.Writer) (app.Result, error) {
	st, err := app.Setup(ctx, in, logOutput)
	if err != nil {
		return app.Result{}, exitError(err)
	}

	return st, nil
}

// exitError maps an app.UsageError to exitUsage with the same message.
func exitError(err error) error {
	if _, ok := errors.AsType[*app.UsageError](err); ok {
		return cli.Exit(err.Error(), exitUsage)
	}

	return err
}

// inputs collects the session flags for app.Setup.
func inputs(cmd *cli.Command) app.Inputs {
	return app.Inputs{
		ConfigPath:     cmd.String(flagConfig),
		StateDir:       cmd.String("state-dir"),
		SessionRef:     cmd.String("session"),
		NewSessionID:   cmd.String(flagSessionID),
		LogLevel:       cmd.String("log-level"),
		Provider:       cmd.String("provider"),
		Model:          cmd.String("model"),
		Effort:         cmd.String("effort"),
		Workspace:      cmd.String(flagWorkspace),
		BaseURL:        cmd.String("base-url"),
		MaxDisk:        cmd.String("max-disk"),
		MaxDiskSet:     cmd.IsSet("max-disk"),
		MaxAttempts:    cmd.Int("max-attempts"),
		Fast:           cmd.Bool("fast"),
		FastSet:        cmd.IsSet("fast"),
		AdaptiveEffort: cmd.String("adaptive-effort"),
		ModelVerbosity: cmd.String("model-verbosity"),
		Sandbox:        cmd.String("sandbox"),
		Ask:            cmd.String("ask"),
		Yolo:           cmd.Bool(flagYolo),
		AllowDotenv:    cmd.Bool("allow-dotenv"),
		NoInstructions: cmd.Bool("no-instructions"),

		ContextPreparation: contextPreparation(cmd),
		EffortUpdates:      os.Getenv(app.EnvEffortUpdates),
		RequestUserInput:   os.Getenv(app.EnvRequestUserInput),
		Tools:              toolsFlag(cmd),
		DenyTools:          toolpolicy.Parse(cmd.String(flagDenyTools)),
		NoSkills:           cmd.Bool("no-skills"),
	}
}

// toolsFlag is --tools as a list: nil when not given, and empty, allowing
// no tools, for --tools "".
func toolsFlag(cmd *cli.Command) []string {
	if !cmd.IsSet(flagTools) {
		return nil
	}

	return toolpolicy.Parse(cmd.String(flagTools))
}

// contextPreparation is off with --no-context-preparation, else its
// environment variable.
func contextPreparation(cmd *cli.Command) string {
	if cmd.Bool(flagNoContextPreparation) {
		return "off"
	}

	return os.Getenv(app.EnvContextPreparation)
}

// oneOf accepts one of allowed, or empty (unset).
func oneOf(flag string, allowed []string) func(string) error {
	return func(v string) error {
		if v != "" && !slices.Contains(allowed, v) {
			return fmt.Errorf("invalid --%s %q (want %s)", flag, v, strings.Join(allowed, ", "))
		}

		return nil
	}
}

func oneOfMap[V any](flag string, allowed map[string]V) func(string) error {
	return func(v string) error {
		if _, ok := allowed[v]; !ok {
			return fmt.Errorf("invalid --%s %q", flag, v)
		}

		return nil
	}
}

func defaultStateDir() string { return home.Dir() }

// sandboxModes are the --sandbox values; no sandbox is --yolo.
func sandboxModes() []string {
	out := make([]string, 0, len(sandbox.Modes))
	for _, m := range sandbox.Modes {
		if m != sandbox.FullAccess {
			out = append(out, string(m))
		}
	}

	return out
}
