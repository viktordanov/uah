package config_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/config"
	"github.com/viktordanov/uah/internal/hooks"
)

// layerHome isolates uah's home and UAH_EXTRA_CONFIG, and returns the
// user file and a workspace.
func layerHome(t *testing.T) (user, ws string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("UAH_HOME", filepath.Join(root, "home"))
	t.Setenv("UAH_EXTRA_CONFIG", "")

	return filepath.Join(root, "home", "config.toml"), filepath.Join(root, "ws")
}

func TestConfigLayers(t *testing.T) {
	t.Run("config.d in lexical order, then UAH_EXTRA_CONFIG, then the project file", func(t *testing.T) {
		user, ws := layerHome(t)
		write(t, user, "model = \"user\"\neffort = \"low\"\n[approvals]\nallow = [\"u\"]\n[[hooks.Stop]]\ncommand = \"from-user\"\n")
		b := filepath.Join(config.LayerDir(), "20-b.toml")
		a := filepath.Join(config.LayerDir(), "10-a.toml")
		write(t, b, "model = \"b\"\n[approvals]\nallow = [\"b\"]\n[[hooks.Stop]]\ncommand = \"from-b\"\n")
		write(t, a, "model = \"a\"\neffort = \"medium\"\n[approvals]\nallow = [\"a\"]\n[[hooks.Stop]]\ncommand = \"from-a\"\n")
		write(t, filepath.Join(config.LayerDir(), "notes.txt"), "not a layer")
		extra := filepath.Join(t.TempDir(), "extra.toml")
		write(t, extra, "effort = \"high\"\n[approvals]\nallow = [\"x\"]\n[[hooks.Stop]]\ncommand = \"from-extra\"\n[projects.\""+ws+"\"]\ntrusted = true\n")
		write(t, config.ProjectFile(ws), "[approvals]\nallow = [\"p\"]\n[[hooks.Stop]]\ncommand = \"from-project\"\n")
		t.Setenv("UAH_EXTRA_CONFIG", extra)

		l, err := config.LoadLayers(user, ws)
		require.NoError(t, err)
		cfg := l.Merged()

		assert.Equal(t, "b", cfg.Model, "a later layer overrides an earlier one")
		assert.Equal(t, "high", cfg.Effort, "UAH_EXTRA_CONFIG merges after config.d")
		assert.Equal(t, []string{"u", "a", "b", "x", "p"}, cfg.Approvals.Allow, "lists add up in merge order")
		assert.True(t, l.Trusted, "a layer may trust a workspace")
		assert.Equal(t, []string{user, a, b, extra, config.ProjectFile(ws)}, l.Files())
		require.Len(t, l.Extra, 3)
		assert.Equal(t, []string{"config.d/10-a.toml", "config.d/20-b.toml", "UAH_EXTRA_CONFIG"}, []string{l.Extra[0].Name, l.Extra[1].Name, l.Extra[2].Name})

		list, err := cfg.HookList()
		require.NoError(t, err)
		var got [][2]string
		for _, h := range list {
			got = append(got, [2]string{h.Command, string(h.Source)})
		}
		assert.Equal(t, [][2]string{
			{"from-user", "user"}, {"from-a", "config.d/10-a.toml"}, {"from-b", "config.d/20-b.toml"},
			{"from-extra", "UAH_EXTRA_CONFIG"}, {"from-project", "project"},
		}, got)
	})

	t.Run("layer hooks run as written; project hooks need trust", func(t *testing.T) {
		user, ws := layerHome(t)
		write(t, filepath.Join(config.LayerDir(), "host.toml"), "[[hooks.Stop]]\ncommand = \"from-layer\"\n[projects.\""+ws+"\"]\ntrusted = true\n")
		write(t, config.ProjectFile(ws), "[[hooks.Stop]]\ncommand = \"from-project\"\n")
		cfg, _, err := config.Load(user, ws)
		require.NoError(t, err)
		list, err := cfg.HookList()
		require.NoError(t, err)
		trust, err := hooks.LoadTrust(filepath.Join(t.TempDir(), "trust.json"))
		require.NoError(t, err)
		runner, err := hooks.New(list, trust, ws)
		require.NoError(t, err)

		require.Len(t, list, 2)
		assert.True(t, runner.Trusted(list[0]), list[0].Command)
		assert.False(t, runner.Trusted(list[1]), list[1].Command)
	})

	t.Run("a later layer's [projects] entry replaces an earlier one", func(t *testing.T) {
		user, ws := layerHome(t)
		write(t, user, "[projects.\""+ws+"\"]\ntrusted = true\n")
		write(t, filepath.Join(config.LayerDir(), "a.toml"), "[projects.\""+ws+"\"]\ntrusted = false\n")
		write(t, config.ProjectFile(ws), "effort = \"max\"\n")

		cfg, files, err := config.Load(user, ws)

		require.NoError(t, err)
		assert.Empty(t, cfg.Effort, "the project file is not read")
		assert.Len(t, files, 2)
	})

	t.Run("relative paths in a layer are relative to it", func(t *testing.T) {
		user, ws := layerHome(t)
		write(t, filepath.Join(config.LayerDir(), "a.toml"), "model_instructions_file = \"system.md\"\n")

		cfg, _, err := config.Load(user, ws)

		require.NoError(t, err)
		assert.Equal(t, filepath.Join(config.LayerDir(), "system.md"), cfg.ModelInstructionsFile)
	})

	t.Run("errors", func(t *testing.T) {
		user, ws := layerHome(t)
		t.Setenv("UAH_EXTRA_CONFIG", filepath.Join(t.TempDir(), "missing.toml"))
		_, _, err := config.Load(user, ws)
		require.ErrorContains(t, err, "UAH_EXTRA_CONFIG names")

		t.Setenv("UAH_EXTRA_CONFIG", "")
		write(t, filepath.Join(config.LayerDir(), "a.toml"), "efort = \"low\"\n")
		_, _, err = config.Load(user, ws)
		require.ErrorContains(t, err, `unknown key "efort"`)
	})
}

// TestConfigLayers_ToolPolicy: every file's [tools] allow narrows the
// others', whatever its place in the merge order, and the deny lists add
// up; a file without the key keeps the list, and an empty list allows none.
func TestConfigLayers_ToolPolicy(t *testing.T) {
	user, ws := layerHome(t)
	write(t, user, "[tools]\nallow = [\"Bash\", \"mcp__docs__*\", \"apply_patch\"]\ndeny = [\"mcp__docs__write\"]\n[projects.\""+ws+"\"]\ntrusted = true\n")
	write(t, filepath.Join(config.LayerDir(), "10-a.toml"), "[tools]\nallow = [\"Bash\", \"mcp__docs__read\", \"apply_patch\", \"ViewImage\"]\n")
	write(t, filepath.Join(config.LayerDir(), "20-b.toml"), "model = \"b\"\n")
	extra := filepath.Join(t.TempDir(), "extra.toml")
	write(t, extra, "[tools]\nallow = [\"Bash\", \"mcp__docs__read\", \"web_search\"]\ndeny = [\"Bash\"]\n")
	t.Setenv("UAH_EXTRA_CONFIG", extra)
	write(t, config.ProjectFile(ws), "[tools]\nallow = [\"Bash\", \"mcp__docs__*\", \"spawn_agent\"]\n")

	cfg, _, err := config.Load(user, ws)
	require.NoError(t, err)
	p := cfg.ToolPolicy()
	assert.Equal(t, []string{"Bash", "mcp__docs__read"}, p.Allow, "no file added a tool another left out")
	assert.Equal(t, []string{"mcp__docs__write", "Bash"}, p.Deny)

	write(t, config.ProjectFile(ws), "[tools]\nallow = []\n")
	cfg, _, err = config.Load(user, ws)
	require.NoError(t, err)
	assert.Equal(t, []string{}, cfg.ToolPolicy().Allow, "an empty list allows none")

	write(t, config.ProjectFile(ws), "[tools]\ndeny = [\"Edit\"]\n")
	_, _, err = config.Load(user, ws)
	require.Error(t, err)
	assert.Contains(t, err.Error(), config.ProjectFile(ws)+`: [tools]: unknown tool "Edit" (uah calls it apply_patch)`)

	t.Setenv("UAH_EXTRA_CONFIG", "")
	write(t, config.ProjectFile(ws), "")
	write(t, filepath.Join(config.LayerDir(), "10-a.toml"), "")
	write(t, user, "model = \"x\"\n")
	cfg, _, err = config.Load(user, ws)
	require.NoError(t, err)
	assert.False(t, cfg.ToolPolicy().Restricted(), "unset everywhere: every tool")
}
