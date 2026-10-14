package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/viktordanov/uah/internal/app"
	"github.com/viktordanov/uah/internal/contextprep"
)

// contextCommand is `uah context`: the modules of context preparation, and
// whether each applies to a new session in the workspace.
func contextCommand() *cli.Command {
	return &cli.Command{
		Name:  "context",
		Usage: "list context preparation's modules and whether each applies to a new session here",
		Description: "Lists every module of the prepared context a new session starts with: the built-ins\n" +
			"(replaced by a file of the same path under <config dir>/prompts/context), the library,\n" +
			"the user's <config dir>/prompts/context.d, and the project's .uah/context.d. For each it\n" +
			"says whether it is on, trusted, and applies to a new main session in the workspace, and\n" +
			"why. Module checks run in a read-only sandbox with no network. --show prints the context\n" +
			"a new main session and a read-only subagent would get.",
		Flags: append(sessionFlags(),
			&cli.BoolFlag{Name: flagJSON, Usage: "print the modules (and with --show, the context) as JSON"},
			&cli.BoolFlag{Name: subShow, Usage: "print the prepared context of a new main session and of a read-only subagent"},
		),
		OnUsageError: onUsageError,
		Action:       contextAction,
		Commands: []*cli.Command{{
			Name:         "trust",
			Usage:        "allow this workspace's project modules (.uah/context.d) as they are now",
			Flags:        sessionFlags(),
			OnUsageError: onUsageError,
			Action:       trustContext,
		}},
	}
}

func contextAction(ctx context.Context, cmd *cli.Command) error {
	p, err := app.PreviewContext(ctx, inputs(cmd), os.Getenv)
	if err != nil {
		return err
	}
	if !cmd.Bool(subShow) {
		p.Main, p.Subagent = "", ""
	}
	if cmd.Bool(flagJSON) {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(p); err != nil {
			return fmt.Errorf("failed to write the modules: %w", err)
		}

		return nil
	}
	if err := printModules(os.Stdout, p.Modules); err != nil {
		return err
	}
	if cmd.Bool(subShow) {
		fmt.Printf("\n── a new main session ──\n%s\n\n── a read-only subagent ──\n%s\n", orNone(p.Main), orNone(p.Subagent))
	}

	return nil
}

// printModules prints one line per module: one that applies plain, with
// its state in green, one that does not dim.
func printModules(w io.Writer, modules []contextprep.Status) error {
	st := cliStyles
	var t table
	t.header("BLOCK", "MODULE", "SOURCE", "STATE", "APPLIES", "WHY")
	for _, m := range modules {
		row, applies := &st.dim, "no"
		if m.Applies {
			row, applies = nil, st.ok.Render("yes")
		}
		state := stateLabel(m)
		switch {
		case state == "on" && m.Applies:
			state = st.ok.Render(state)
		case state == untrusted:
			state = st.warn.Render(state)
		case state == "error":
			state = st.bad.Render(state)
		}
		t.row(row, m.Block, m.Path, sourceLabel(m), state, applies, oneLine(m.Reason, 100))
	}

	return t.write(w)
}

// sourceLabel names where a module comes from, and its file when it is not
// built in.
func sourceLabel(m contextprep.Status) string {
	label := string(m.Source)
	switch {
	case m.Pinned:
		label += " (replaces the built-in with an identical copy; `uah prompts prune` deletes it)"
	case m.Overrides:
		label += " (replaces the built-in)"
	}
	if m.File != "" {
		label += " " + homePath(m.File)
	}

	return label
}

// untrusted is the state of a project module or a hook not yet trusted.
const untrusted = "untrusted"

// stateLabel is a module's STATE column.
func stateLabel(m contextprep.Status) string {
	switch {
	case strings.HasPrefix(m.Reason, "error: "):
		return "error"
	case !m.Trusted:
		return untrusted
	case !m.Enabled:
		return "off"
	}

	return "on"
}

func orNone(s string) string {
	if s == "" {
		return "(none: context preparation is off, or no block has anything to say)"
	}

	return s
}

func trustContext(_ context.Context, cmd *cli.Command) error {
	paths, err := app.TrustContextModules(inputs(cmd))
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		fmt.Fprintln(os.Stderr, "no untrusted project modules")

		return nil
	}
	for _, p := range paths {
		fmt.Printf("trusted %s\n", p)
	}

	return nil
}
