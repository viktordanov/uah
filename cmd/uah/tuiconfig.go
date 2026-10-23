package main

import (
	"context"

	"github.com/urfave/cli/v3"

	"github.com/viktordanov/uah/internal/app"
	"github.com/viktordanov/uah/internal/tui/state"
)

// tuiConfig loads what /config shows: the values the TUI's session uses
// (the flags, the session, then the files of its workspace), with their
// sources, as `uah config --session` explains them. With no session, as
// in the picker, they are what a session opened now would use.
func tuiConfig(in app.Inputs) func(ctx context.Context, sessionID string) state.ConfigLoaded {
	return func(ctx context.Context, sessionID string) state.ConfigLoaded {
		in := in // loads may overlap
		in.SessionRef = sessionID
		rep, err := app.Inspect(ctx, in)
		if err != nil {
			return state.ConfigLoaded{Err: err}
		}
		values := make(map[string]state.ConfigValue, len(rep.Settings))
		for _, s := range rep.Settings {
			values[s.Key] = state.ConfigValue{Value: s.Text(), Source: s.SourceText()}
		}

		return state.ConfigLoaded{Path: rep.UserFile.Path, Values: values}
	}
}

// tuiSaveConfig writes one key to the user file with the editor `uah mcp
// add` uses, keeping its comments.
func tuiSaveConfig(ctx context.Context, cmd *cli.Command) func(key string, value any) error {
	return func(key string, value any) error {
		return app.SaveSetting(ctx, inputs(cmd), key, value) // SaveSetting says what failed
	}
}
