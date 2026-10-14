package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/viktordanov/uah/internal/app"
	"github.com/viktordanov/uah/internal/models"
)

// modelsCommand is `uah models`: the models the provider offers this login,
// as Codex's `codex debug models` prints its catalog.
func modelsCommand() *cli.Command {
	return &cli.Command{
		Name:  "models",
		Usage: "list the models the provider offers to your login",
		Description: "Lists the provider's models for the session flags' provider and credentials: from the cache\n" +
			"while it is fresh (5 minutes), else from the provider, else the list bundled with uah.\n" +
			"The first line says which. --refresh always asks the provider. Hidden models work but\n" +
			"are not offered in menus; --all lists them too. The model a session with the same flags\n" +
			"would use is marked default.",
		Flags: append(sessionFlags(),
			&cli.BoolFlag{Name: flagJSON, Usage: "print the catalog as JSON"},
			&cli.BoolFlag{Name: "refresh", Usage: "ask the provider even when the cache is fresh"},
			&cli.BoolFlag{Name: "all", Usage: "include hidden models"},
		),
		OnUsageError: onUsageError,
		Action:       modelsAction,
	}
}

func modelsAction(ctx context.Context, cmd *cli.Command) error {
	c, model, err := app.ListModels(ctx, inputs(cmd), cmd.Bool("refresh"), os.Getenv)
	if err != nil {
		return exitError(err)
	}
	if !cmd.Bool("all") {
		c.Models = c.Visible()
	}
	if cmd.Bool(flagJSON) {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(catalogJSON{Catalog: c, Default: model, Error: errText(c.Err)}); err != nil {
			return fmt.Errorf("failed to write the models: %w", err)
		}

		return nil
	}
	printCatalog(os.Stdout, c, model)

	return nil
}

// catalogJSON is the catalog with the model a session would use and the
// refresh error as text.
type catalogJSON struct {
	models.Catalog

	Default string `json:"default,omitempty"`
	Error   string `json:"error,omitempty"`
}

func errText(err error) string {
	if err == nil {
		return ""
	}

	return err.Error()
}

// printCatalog marks the model a session with the same flags would use
// (on openai-codex without -m, gpt-6.1-sol when listed, else gpt-6-sol).
func printCatalog(w io.Writer, c models.Catalog, model string) {
	w = colorOut(w)
	fmt.Fprintf(w, "%d models from %s (%s list)\n", len(c.Models), c.Provider, c.Origin)
	if c.Err != nil {
		fmt.Fprintf(w, "the provider did not answer: %v\n", c.Err)
	}
	for _, m := range c.Models {
		line := app.ModelLine(m)
		if m.ID == model {
			line = cliStyles.bold.Render(line) + cliStyles.ok.Render(" · default")
		}
		fmt.Fprintln(w, line)
	}
}
