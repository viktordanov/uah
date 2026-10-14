package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"charm.land/lipgloss/v2"
	"github.com/urfave/cli/v3"

	"github.com/viktordanov/uah/internal/app"
)

// doctorCommand is `uah doctor`: check what a session in the workspace needs.
func doctorCommand() *cli.Command {
	return &cli.Command{
		Name:  "doctor",
		Usage: "check the setup: credentials, models, sandbox, configuration, hooks, MCP servers, and state",
		Description: "Checks what a session in the workspace would use, with the same flags, and prints one\n" +
			"line per check: ✓ fine, ! works but needs a look, ✗ broken, each with a fix. It calls no\n" +
			"model; it lists the provider's models, runs `true` in the sandbox, and starts the MCP servers.\n" +
			"Exits 1 when a check fails.",
		Flags:        append(sessionFlags(), &cli.BoolFlag{Name: flagJSON, Usage: "print the checks as JSON"}),
		OnUsageError: onUsageError,
		Action:       doctorAction,
	}
}

func doctorAction(ctx context.Context, cmd *cli.Command) error {
	checks := app.Doctor(ctx, inputs(cmd), app.DoctorOptions{})
	if cmd.Bool(flagJSON) {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(struct {
			OK     bool        `json:"ok"`
			Checks []app.Check `json:"checks"`
		}{app.Healthy(checks), checks}); err != nil {
			return fmt.Errorf("failed to write the checks: %w", err)
		}
	} else {
		printChecks(os.Stdout, checks)
	}
	if !app.Healthy(checks) {
		return cli.Exit("", exitFailed)
	}

	return nil
}

var checkMarks = map[app.CheckStatus]string{app.CheckOK: "✓", app.CheckWarn: "!", app.CheckFail: "✗"}

// checkStyles color a check's mark and name: green, yellow, red.
var checkStyles = map[app.CheckStatus]lipgloss.Style{app.CheckOK: cliStyles.ok, app.CheckWarn: cliStyles.warn, app.CheckFail: cliStyles.bad}

func printChecks(w io.Writer, checks []app.Check) {
	w = colorOut(w)
	for _, c := range checks {
		st := checkStyles[c.Status]
		fmt.Fprintf(w, "%s %s: %s\n", st.Render(checkMarks[c.Status]), st.Render(c.Name), c.Detail)
		if c.Fix != "" {
			fmt.Fprintf(w, "    %s %s\n", cliStyles.bold.Render("fix:"), c.Fix)
		}
	}
}
