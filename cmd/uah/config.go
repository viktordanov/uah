package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/urfave/cli/v3"

	"github.com/viktordanov/uah/internal/app"
)

// flagConfig names the user configuration file flag, and its command.
const flagConfig = "config"

// configCommand is `uah config`: the effective configuration and where each
// value came from.
func configCommand() *cli.Command {
	return &cli.Command{
		Name:  flagConfig,
		Usage: "show the effective configuration for a workspace and each value's source",
		Description: "Takes the same flags as a session and shows what a session started with them would use:\n" +
			"each key's value and its source (flag, env, session, project file, UAH_EXTRA_CONFIG,\n" +
			"config.d/<file>, user file, or default).\n" +
			"Keys whose files add up list every file that set them. The reference is docs/configuration.md.",
		Flags:        append(sessionFlags(), &cli.BoolFlag{Name: flagJSON, Usage: "print JSON"}),
		OnUsageError: onUsageError,
		Action:       configAction,
	}
}

func configAction(ctx context.Context, cmd *cli.Command) error {
	rep, err := app.Inspect(ctx, inputs(cmd))
	if err != nil {
		return exitError(err)
	}
	if cmd.Bool(flagJSON) {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")

		return enc.Encode(rep)
	}

	return printReport(os.Stdout, rep)
}

// printReport writes the home, the workspace, and the files (the layers
// after the user file), then one line per key: key, value (in one line,
// its line breaks and tabs escaped; a long one runs past its column), and
// source, dim when it is the default.
func printReport(w io.Writer, rep app.Report) error {
	fmt.Fprintf(w, "home:         %s\n", rep.Home)
	fmt.Fprintf(w, "workspace:    %s (%s)\n", rep.Workspace, rep.WorkspaceSource)
	fmt.Fprintf(w, "user file:    %s (%s)\n", rep.UserFile.Path, rep.UserFile.State)
	for _, f := range rep.Layers {
		fmt.Fprintf(w, "layer:        %s (%s)\n", f.Path, f.State)
	}
	fmt.Fprintf(w, "project file: %s (%s)\n\n", rep.ProjectFile.Path, rep.ProjectFile.State)
	var t table
	t.capColumn(1, 60)
	t.header("KEY", "VALUE", "SOURCE")
	for _, s := range rep.Settings {
		var row *lipgloss.Style
		if s.SourceText() == string(app.FromDefault) {
			row = &cliStyles.dim
		}
		t.row(row, s.Key, controlEscaper.Replace(s.Text()), s.SourceText())
	}

	return t.write(w)
}

// controlEscaper writes a value's line breaks and tabs as \n, \r, and \t,
// so it keeps one line and every other character as it is.
var controlEscaper = strings.NewReplacer("\n", `\n`, "\r", `\r`, "\t", `\t`)
