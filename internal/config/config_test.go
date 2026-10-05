package config_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/config"
	"github.com/viktordanov/uah/internal/hooks"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}

func TestLoad(t *testing.T) {
	t.Run("missing files are an empty config", func(t *testing.T) {
		cfg, loaded, err := config.Load(filepath.Join(t.TempDir(), "none.toml"), t.TempDir())

		require.NoError(t, err)
		assert.Empty(t, loaded)
		assert.True(t, cfg.InstructionsEnabled())
	})

	t.Run("a project file applies only when the user file trusts the workspace", func(t *testing.T) {
		root := t.TempDir()
		ws := filepath.Join(root, "ws")
		user := filepath.Join(root, "config.toml")
		write(t, config.ProjectFile(ws), "effort = \"max\"\n[instructions]\nenabled = false\n")
		write(t, user, "provider = \"openrouter\"\neffort = \"low\"\n")

		untrusted, loaded, err := config.Load(user, ws)
		require.NoError(t, err)
		assert.Equal(t, "low", untrusted.Effort)
		assert.Equal(t, []string{user}, loaded)

		write(t, user, "provider = \"openrouter\"\neffort = \"low\"\n[projects.\""+ws+"\"]\ntrusted = true\n")
		trusted, loaded, err := config.Load(user, ws)
		require.NoError(t, err)
		assert.Equal(t, "max", trusted.Effort, "the project file overrides the user file")
		assert.Equal(t, "openrouter", trusted.Provider, "unset project values keep the user's")
		assert.False(t, trusted.InstructionsEnabled())
		assert.Equal(t, []string{user, config.ProjectFile(ws)}, loaded)
	})

	t.Run("compaction keys, and the project file overrides them", func(t *testing.T) {
		root := t.TempDir()
		ws := filepath.Join(root, "ws")
		user := filepath.Join(root, "config.toml")
		write(t, user, "auto_compact_percent = 80\nmodel_context_window = 128000\n[projects.\""+ws+"\"]\ntrusted = true\n")
		write(t, config.ProjectFile(ws), "auto_compact_percent = 0\n")

		cfg, _, err := config.Load(user, ws)

		require.NoError(t, err)
		require.NotNil(t, cfg.AutoCompactPercent)
		assert.Equal(t, 0, *cfg.AutoCompactPercent, "0 in the project file turns it off")
		assert.Equal(t, int64(128000), cfg.ModelContextWindow)
	})

	t.Run("reviewer keys, and the project file overrides them", func(t *testing.T) {
		root := t.TempDir()
		ws := filepath.Join(root, "ws")
		user := filepath.Join(root, "config.toml")
		write(t, user, "approvals_reviewer = \"user\"\n[review]\nmodel = \"m\"\neffort = \"medium\"\ntimeout = \"30s\"\n[projects.\""+ws+"\"]\ntrusted = true\n")
		write(t, config.ProjectFile(ws), "[review]\neffort = \"low\"\n")

		cfg, _, err := config.Load(user, ws)

		require.NoError(t, err)
		assert.Equal(t, "user", cfg.ApprovalsReviewer)
		assert.Equal(t, config.Review{Model: "m", Effort: "low", Timeout: "30s"}, cfg.Review)
	})

	t.Run("unknown keys are errors", func(t *testing.T) {
		user := filepath.Join(t.TempDir(), "config.toml")
		write(t, user, "efort = \"low\"\n")

		_, _, err := config.Load(user, t.TempDir())

		require.ErrorContains(t, err, `unknown key "efort"`)
	})

	t.Run("timeout is an unknown key: a turn has no wall-clock limit", func(t *testing.T) {
		user := filepath.Join(t.TempDir(), "config.toml")
		write(t, user, "timeout = \"30m\"\n")

		_, _, err := config.Load(user, t.TempDir())

		require.ErrorContains(t, err, `unknown key "timeout"`)
	})

	t.Run("effort_updates is an unknown key: only UAH_EFFORT_UPDATES turns them off", func(t *testing.T) {
		user := filepath.Join(t.TempDir(), "config.toml")
		write(t, user, "effort_updates = false\n")

		_, _, err := config.Load(user, t.TempDir())

		require.ErrorContains(t, err, `unknown key "effort_updates"`)
	})

	t.Run("a project file cannot trust itself", func(t *testing.T) {
		root := t.TempDir()
		ws := filepath.Join(root, "ws")
		user := filepath.Join(root, "config.toml")
		write(t, user, "[projects.\""+ws+"\"]\ntrusted = true\n")
		write(t, config.ProjectFile(ws), "[projects.\"/elsewhere\"]\ntrusted = true\n")

		_, _, err := config.Load(user, ws)

		require.ErrorContains(t, err, "belongs in the user file")
	})
}

func TestHooks(t *testing.T) {
	root := t.TempDir()
	ws := filepath.Join(root, "ws")
	user := filepath.Join(root, "config.toml")
	write(t, user, `
[projects."`+ws+`"]
trusted = true

[[hooks.PreToolUse]]
matcher = "Bash"
command = "check.sh"
timeout = "5s"
`)
	write(t, config.ProjectFile(ws), "[[hooks.Stop]]\ncommand = \"notify.sh\"\n")

	cfg, _, err := config.Load(user, ws)
	require.NoError(t, err)
	list, err := cfg.HookList()
	require.NoError(t, err)
	assert.Equal(t, []hooks.Hook{
		{Event: hooks.PreToolUse, Matcher: "Bash", Command: "check.sh", Timeout: 5 * time.Second, Source: hooks.SourceUser},
		{Event: hooks.Stop, Command: "notify.sh", Source: hooks.SourceProject},
	}, list)

	write(t, user, "[[hooks.Nope]]\ncommand = \"x\"\n")
	cfg, _, err = config.Load(user, ws)
	require.NoError(t, err)
	_, err = cfg.HookList()
	require.ErrorContains(t, err, `unknown hook event "Nope"`)
}

func TestApprovals(t *testing.T) {
	root := t.TempDir()
	ws := filepath.Join(root, "ws")
	user := filepath.Join(root, "config.toml")
	write(t, user, `
approval_policy = "on-request"

[approvals]
allow = ["git status"]
forbid = ["git push --force"]

[sandbox_workspace_write]
writable_roots = ["~/.cache/go-build"]

[projects."`+ws+`"]
trusted = true
`)
	write(t, config.ProjectFile(ws), `
approval_policy = "never"

[approvals]
allow = ["go test"]

[sandbox_workspace_write]
writable_roots = ["build"]
`)

	cfg, _, err := config.Load(user, ws)
	require.NoError(t, err)

	assert.Equal(t, "never", cfg.ApprovalPolicy, "the project file overrides the policy")
	assert.Equal(t, []string{"git status", "go test"}, cfg.Approvals.Allow, "the project file adds approvals")
	assert.Equal(t, []string{"git push --force"}, cfg.Approvals.Forbid)
	assert.Equal(t, []string{"~/.cache/go-build", "build"}, cfg.SandboxWorkspaceWrite.WritableRoots, "the project file adds writable roots")
	assert.Equal(t, filepath.Join(ws, ".uah", "rules"), config.ProjectRulesDir(ws))
}

func TestLoadLayers(t *testing.T) {
	root := t.TempDir()
	ws := filepath.Join(root, "ws")
	user := filepath.Join(root, "config.toml")
	write(t, user, "[[hooks.Stop]]\ncommand = \"user\"\n[projects.\""+ws+"\"]\ntrusted = true\n")
	write(t, config.ProjectFile(ws), "[[hooks.Stop]]\ncommand = \"project\"\n")

	l, err := config.LoadLayers(user, ws)

	require.NoError(t, err)
	assert.True(t, l.Trusted)
	assert.Equal(t, []string{user, config.ProjectFile(ws)}, l.Files())
	assert.Len(t, l.Merged().Hooks["Stop"], 2)
	assert.Len(t, l.Merged().Hooks["Stop"], 2, "merging twice adds the project hooks once")
	assert.Len(t, l.User.Hooks["Stop"], 1, "merging leaves the user file's hooks alone")
}

func TestAgents(t *testing.T) {
	root := t.TempDir()
	ws := filepath.Join(root, "ws")
	user := filepath.Join(root, "config.toml")
	write(t, user, `
[agents]
max_concurrent_threads_per_session = 6
default_subagent_model = "gpt-small"

[projects."`+ws+`"]
trusted = true
`)
	write(t, config.ProjectFile(ws), `
[agents]
enabled = false
max_depth = 2
default_subagent_reasoning_effort = "low"
`)

	cfg, _, err := config.Load(user, ws)
	require.NoError(t, err)

	a := cfg.Agents
	require.NotNil(t, a.Enabled)
	assert.False(t, *a.Enabled, "the project file turns agents off")
	assert.Equal(t, 6, *a.MaxConcurrentThreadsPerSession)
	assert.Equal(t, 2, *a.MaxDepth)
	assert.Equal(t, "gpt-small", a.DefaultSubagentModel)
	assert.Equal(t, "low", a.DefaultSubagentReasoningEffort)
	assert.Equal(t, filepath.Join(ws, ".uah", "agents"), config.ProjectAgentsDir(ws))

	write(t, user, "[agents]\nmax_threads = 2\n")
	cfg, _, err = config.Load(user, ws)
	require.NoError(t, err)
	assert.Equal(t, 2, *cfg.Agents.MaxThreadsValue(), "Codex's alias")
	write(t, user, "[agents]\nmax_thread = 2\n")
	_, _, err = config.Load(user, ws)
	require.ErrorContains(t, err, `unknown key "agents.max_thread"`)
}

// TestMouse: the TUI reports the mouse unless a file sets mouse = false,
// and a trusted project file can turn it off or on again.
func TestMouse(t *testing.T) {
	root := t.TempDir()
	ws := filepath.Join(root, "ws")
	user := filepath.Join(root, "config.toml")
	cfg, _, err := config.Load(user, ws)
	require.NoError(t, err)
	assert.True(t, cfg.TUI.MouseOn(), "on by default")

	write(t, user, "[tui]\nmouse = false\n")
	cfg, _, err = config.Load(user, ws)
	require.NoError(t, err)
	assert.False(t, cfg.TUI.MouseOn())

	write(t, user, "[tui]\nmouse = false\n[projects.\""+ws+"\"]\ntrusted = true\n")
	write(t, config.ProjectFile(ws), "[tui]\nmouse = true\n")
	cfg, _, err = config.Load(user, ws)
	require.NoError(t, err)
	assert.True(t, cfg.TUI.MouseOn(), "the project file wins")
}

// TestFileLinks: a click on a path peeks unless file_links says otherwise,
// a trusted project file overrides it, and an unknown value is refused.
func TestFileLinks(t *testing.T) {
	root := t.TempDir()
	ws := filepath.Join(root, "ws")
	user := filepath.Join(root, "config.toml")
	cfg, _, err := config.Load(user, ws)
	require.NoError(t, err)
	assert.Equal(t, config.FileLinksPeek, cfg.TUI.FileLinksMode(), "peek by default")

	write(t, user, "[tui]\nfile_links = \"editor\"\n[projects.\""+ws+"\"]\ntrusted = true\n")
	cfg, _, err = config.Load(user, ws)
	require.NoError(t, err)
	assert.Equal(t, config.FileLinksEditor, cfg.TUI.FileLinksMode())

	write(t, config.ProjectFile(ws), "[tui]\nfile_links = \"off\"\n")
	cfg, _, err = config.Load(user, ws)
	require.NoError(t, err)
	assert.Equal(t, config.FileLinksOff, cfg.TUI.FileLinksMode(), "the project file wins")

	write(t, user, "[tui]\nfile_links = \"browser\"\n")
	_, _, err = config.Load(user, ws)
	require.ErrorContains(t, err, `file_links is "browser"; want one of peek, editor, open, off`)
}

// TestAdaptiveEffort: adaptive_effort is unset by default, the user file sets it, and a
// trusted project file overrides it.
func TestAdaptiveEffort(t *testing.T) {
	root := t.TempDir()
	ws := filepath.Join(root, "ws")
	user := filepath.Join(root, "config.toml")
	cfg, _, err := config.Load(user, ws)
	require.NoError(t, err)
	assert.Empty(t, cfg.AdaptiveEffort, "off by default")

	write(t, user, "adaptive_effort = \"1-step\"\n")
	cfg, _, err = config.Load(user, ws)
	require.NoError(t, err)
	assert.Equal(t, "1-step", cfg.AdaptiveEffort)

	write(t, user, "adaptive_effort = \"2-steps\"\n[projects.\""+ws+"\"]\ntrusted = true\n")
	write(t, config.ProjectFile(ws), "adaptive_effort = \"off\"\n")
	cfg, _, err = config.Load(user, ws)
	require.NoError(t, err)
	assert.Equal(t, "off", cfg.AdaptiveEffort, "the project file wins")
}

// TestTitle: the TUI sets the terminal's title unless a file sets title =
// false, and a trusted project file can turn it on again.
func TestTitle(t *testing.T) {
	root := t.TempDir()
	ws := filepath.Join(root, "ws")
	user := filepath.Join(root, "config.toml")
	cfg, _, err := config.Load(user, ws)
	require.NoError(t, err)
	assert.True(t, cfg.TUI.TitleOn(), "on by default")

	write(t, user, "[tui]\ntitle = false\n[projects.\""+ws+"\"]\ntrusted = true\n")
	cfg, _, err = config.Load(user, ws)
	require.NoError(t, err)
	assert.False(t, cfg.TUI.TitleOn())

	write(t, config.ProjectFile(ws), "[tui]\ntitle = true\n")
	cfg, _, err = config.Load(user, ws)
	require.NoError(t, err)
	assert.True(t, cfg.TUI.TitleOn(), "the project file wins")
}

// TestHistory: [history] takes Codex's keys, and a later file overrides
// each one it sets.
func TestHistory(t *testing.T) {
	root := t.TempDir()
	ws := filepath.Join(root, "ws")
	user := filepath.Join(root, "config.toml")
	cfg, _, err := config.Load(user, ws)
	require.NoError(t, err)
	assert.Empty(t, cfg.History.Persistence)
	assert.Nil(t, cfg.History.MaxBytes)

	write(t, user, "[history]\npersistence = \"none\"\nmax_bytes = 4096\n[projects.\""+ws+"\"]\ntrusted = true\n")
	write(t, config.ProjectFile(ws), "[history]\nmax_bytes = 0\n")
	cfg, _, err = config.Load(user, ws)
	require.NoError(t, err)
	assert.Equal(t, "none", cfg.History.Persistence)
	require.NotNil(t, cfg.History.MaxBytes)
	assert.Equal(t, int64(0), *cfg.History.MaxBytes, "the project file wins")
}
