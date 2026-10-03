package app_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/app"
	"github.com/viktordanov/uah/internal/config"
	"github.com/viktordanov/uah/internal/instructions"
	"github.com/viktordanov/uah/internal/sandbox"
	"github.com/viktordanov/uah/testing/harnesstest"
)

// setupEnv isolates the configuration directory and CODEX_HOME, and returns
// inputs for a new session in a fresh workspace. The base URL refuses
// connections, so settling the default model never reaches the network.
func setupEnv(t *testing.T) (*harnesstest.Env, app.Inputs) {
	t.Helper()
	e := harnesstest.NewEnv(t)
	configDir := filepath.Join(e.StateDir, "..", "config")
	t.Setenv("UAH_HOME", configDir)
	t.Setenv("CODEX_HOME", e.CodexHome)

	return e, app.Inputs{
		ConfigPath: filepath.Join(configDir, "config.toml"),
		StateDir:   e.StateDir,
		Workspace:  e.Workspace,
		LogLevel:   "warn",
		MaxDisk:    "5G",
		BaseURL:    closedURL,
	}
}

// closedURL is a loopback address nothing listens on.
const closedURL = "http://127.0.0.1:1"

func TestSetup(t *testing.T) {
	e, in := setupEnv(t)
	require.NoError(t, os.WriteFile(filepath.Join(e.Workspace, "AGENTS.md"), []byte("Use tabs in Go files."), 0o600))

	res, err := app.Setup(context.Background(), in, io.Discard)

	require.NoError(t, err)
	assert.Equal(t, e.StateDir, res.StateDir)
	assert.Equal(t, "embedded", res.Engine.Name())
	assert.Equal(t, filepath.Join(e.StateDir, "sessions"), res.Options.SessionsDir)
	assert.False(t, res.Options.Resumed)
	assert.Equal(t, app.CodexProvider, res.Options.Settings.Provider)
	require.NotNil(t, res.Options.Instructions)
	assert.Equal(t, []string{filepath.Join(e.Workspace, "AGENTS.md")}, res.Options.Instructions.Files)
	assertHostPrompt(t, res.Options.Settings.SystemPrompt, e.Workspace, "Use tabs in Go files.")
	assert.Nil(t, res.Options.Hooks)

	t.Run("without instructions", func(t *testing.T) {
		in := in
		in.NoInstructions = true

		res, err := app.Setup(context.Background(), in, io.Discard)

		require.NoError(t, err)
		assert.Nil(t, res.Options.Instructions)
		assertHostPrompt(t, res.Options.Settings.SystemPrompt, e.Workspace, "")
	})
}

// assertHostPrompt checks a session's system prompt without
// model_instructions_file: uah's default base instructions, the
// instruction files when there are any, and then Codex's
// <environment_context> with the workspace, the user's shell, today's
// date, and the time zone.
func assertHostPrompt(t *testing.T, prompt, workspace, instructionsText string) {
	t.Helper()
	require.True(t, strings.HasPrefix(prompt, instructions.DefaultPrompt), "the default base instructions first")
	rest := strings.TrimPrefix(prompt, instructions.DefaultPrompt)
	files, env, ok := strings.Cut(rest, "\n"+instructions.EnvironmentOpen+"\n")
	require.True(t, ok, "an environment block: %q", rest)
	if instructionsText == "" {
		assert.Empty(t, files)
	} else {
		assert.Contains(t, files, instructionsText, "the instructions come before the environment")
	}
	local := instructions.LocalEnvironment(workspace, app.RealShell(), time.Now(), os.Getenv)
	assert.Equal(t, local.String()+"\n", instructions.EnvironmentOpen+"\n"+env)
	assert.NotEmpty(t, local.CurrentDate)
	assert.NotEmpty(t, local.Timezone)
	assert.Equal(t, filepath.Base(app.RealShell()), local.Shell)
}

func TestSetupUsageErrors(t *testing.T) {
	tests := []struct {
		name   string
		in     func(*app.Inputs)
		config string
		want   string
	}{
		{name: "an unknown session", in: func(in *app.Inputs) { in.SessionRef = "zzzz" }, want: `no session matches "zzzz"`},
		{name: "a config typo", config: "efort = \"low\"\n", want: `unknown key "efort"`},
		{
			name: "fast on a provider without priority processing",
			in:   func(in *app.Inputs) { in.Provider, in.Fast, in.FastSet = "ollama", true, true },
			want: "--fast needs the openai or openai-codex provider",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, in := setupEnv(t)
			if tt.in != nil {
				tt.in(&in)
			}
			if tt.config != "" {
				require.NoError(t, os.MkdirAll(filepath.Dir(in.ConfigPath), 0o700))
				require.NoError(t, os.WriteFile(in.ConfigPath, []byte(tt.config), 0o600))
			}

			_, err := app.Setup(context.Background(), in, io.Discard)

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
			var usage *app.UsageError
			assert.True(t, errors.As(err, &usage), "a usage error")
		})
	}
}

// TestSetup_Rules loads the user's rules files and a trusted project's; a
// broken rules file is a usage error, and an untrusted project's is not read.
func TestSetup_Rules(t *testing.T) {
	e, in := setupEnv(t)
	userRules := filepath.Join(filepath.Dir(in.ConfigPath), "rules")
	require.NoError(t, os.MkdirAll(userRules, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(userRules, "default.rules"), []byte(`prefix_rule(pattern=["git", "pull"])`), 0o600))
	projectRules := filepath.Join(e.Workspace, ".uah", "rules")
	require.NoError(t, os.MkdirAll(projectRules, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(projectRules, "broken.rules"), []byte(`prefix_rule(`), 0o600))

	_, err := app.Setup(context.Background(), in, io.Discard)
	require.NoError(t, err, "an untrusted project's rules are not read")

	require.NoError(t, os.WriteFile(in.ConfigPath, []byte("[projects.\""+e.Workspace+"\"]\ntrusted = true\n"), 0o600))
	_, err = app.Setup(context.Background(), in, io.Discard)
	require.ErrorContains(t, err, "broken.rules")
	var usage *app.UsageError
	assert.ErrorAs(t, err, &usage)

	require.NoError(t, os.WriteFile(filepath.Join(userRules, "default.rules"), []byte(`prefix_rule(pattern=[])`), 0o600))
	_, err = app.Setup(context.Background(), in, io.Discard)
	require.ErrorContains(t, err, "default.rules")
}

func TestSetupChecksTheCompactPromptFile(t *testing.T) {
	_, in := setupEnv(t)
	prompt := filepath.Join(t.TempDir(), "compact.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(in.ConfigPath), 0o700))
	require.NoError(t, os.WriteFile(in.ConfigPath, []byte(`experimental_compact_prompt_file = "`+prompt+`"`+"\n"), 0o600))

	_, err := app.Setup(context.Background(), in, io.Discard)
	require.ErrorContains(t, err, "failed to read experimental_compact_prompt_file", "a missing file stops the session, as in Codex")

	require.NoError(t, os.WriteFile(prompt, []byte(" \n"), 0o600))
	_, err = app.Setup(context.Background(), in, io.Discard)
	require.ErrorContains(t, err, "is empty")

	require.NoError(t, os.WriteFile(prompt, []byte("Summarize for a handoff.\n"), 0o600))
	_, err = app.Setup(context.Background(), in, io.Discard)
	require.NoError(t, err)
}

// TestSetup_OldProjectDirectory: a workspace that still has .uagent gets a
// notice with the command that moves it; uah moves nothing.
func TestSetup_OldProjectDirectory(t *testing.T) {
	e, in := setupEnv(t)
	writeConfig(t, &in, "[projects.\""+e.Workspace+"\"]\ntrusted = true\n")
	writeFile(t, filepath.Join(e.Workspace, ".uagent", "config.toml"), "effort = \"low\"\n")

	res, err := app.Setup(context.Background(), in, io.Discard)

	require.NoError(t, err)
	assert.Contains(t, res.Options.Notices, config.ProjectMoveNotice(e.Workspace))
	assert.NotEqual(t, "low", res.Options.Settings.Effort, "a trusted workspace's .uagent is not read")
	assert.DirExists(t, filepath.Join(e.Workspace, ".uagent"))
	assert.NoDirExists(t, filepath.Join(e.Workspace, ".uah"))
}

// TestSetup_SandboxScriptsStayInTheStateDir checks that an ephemeral run
// (a RunStateDir under $TMPDIR, which sandboxed commands write) keeps the
// sandboxing scripts in the state directory, and that the session's
// policy keeps their directory read-only.
func TestSetup_SandboxScriptsStayInTheStateDir(t *testing.T) {
	e, in := setupEnv(t)
	in.RunStateDir = t.TempDir()
	res, err := app.Setup(context.Background(), in, io.Discard)
	require.NoError(t, err)

	scripts := filepath.Join(e.StateDir, "sandbox")
	assert.Equal(t, filepath.Join(in.RunStateDir, "sessions"), res.Options.SessionsDir)
	require.NotNil(t, res.Options.Shell)
	assert.Equal(t, scripts, res.Options.Shell.Dir)
	assert.Contains(t, res.Sandbox.ReadOnly, scripts)
	ws := res.Sandbox
	ws.Mode = sandbox.WorkspaceWrite
	assert.False(t, ws.CanWrite(filepath.Join(scripts, "sh-0123")))
}
