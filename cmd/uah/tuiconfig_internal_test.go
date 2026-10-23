package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/app"
	"github.com/viktordanov/uah/internal/tui/state"
)

// TestTUIConfig_SessionWorkspace: /config reads the project file of the
// open session's workspace, not of the directory uah started in, and the
// values the session itself keeps.
func TestTUIConfig_SessionWorkspace(t *testing.T) {
	root := t.TempDir()
	t.Setenv("UAH_HOME", filepath.Join(root, "home"))
	ws, user, stateDir := filepath.Join(root, "ws"), filepath.Join(root, "home", "config.toml"), filepath.Join(root, "state")
	const id = "11111111-2222-4333-8444-555555555555"
	write := func(path, content string) {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	}
	write(user, "effort = \"low\"\n[projects.\""+ws+"\"]\ntrusted = true\n")
	write(filepath.Join(ws, ".uah", "config.toml"), "auto_compact_percent = 80\n")
	write(filepath.Join(stateDir, "sessions", id+".uah.json"),
		`{"source":"tui","created":"2026-01-01T00:00:00Z","workspace":"`+ws+`","settings":{"provider":"openai","model":"gpt-6-luna","effort":"high"}}`)
	load := tuiConfig(app.Inputs{ConfigPath: user, StateDir: stateDir, MaxDisk: "5G"})

	got := load(context.Background(), id)
	require.NoError(t, got.Err)
	assert.Equal(t, state.ConfigValue{Value: "80", Source: "project file"}, got.Values["auto_compact_percent"], "the session's workspace")
	assert.Equal(t, state.ConfigValue{Value: "high", Source: "session"}, got.Values["effort"], "the session's own effort")

	got = load(context.Background(), "")
	require.NoError(t, got.Err)
	assert.Equal(t, "default", got.Values["auto_compact_percent"].Source, "no session: the current directory, which has no project file")
	assert.Equal(t, state.ConfigValue{Value: "low", Source: "user file"}, got.Values["effort"])
}
