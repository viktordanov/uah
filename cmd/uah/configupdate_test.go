package main_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestConfigUpdate adds the new settings to the user file, commented out,
// once; --dry-run prints them and writes nothing.
func TestConfigUpdate(t *testing.T) {
	t.Parallel()
	env := offlineEnv(t)
	file := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(file, []byte("model = \"gpt-6.2-sol\"\n"), 0o600))

	res := uahWith(t, env, "", "config", "update", "--config", file, "--dry-run")
	require.Equal(t, 0, res.code, res.stderr)
	assert.Contains(t, res.stdout, "would add [advisor] to "+file+", commented out:")
	assert.Contains(t, res.stdout, "# uah config update: [advisor]")
	data, err := os.ReadFile(file)
	require.NoError(t, err)
	assert.Equal(t, "model = \"gpt-6.2-sol\"\n", string(data))

	res = uahWith(t, env, "", "config", "update", "--config", file)
	require.Equal(t, 0, res.code, res.stderr)
	assert.Contains(t, res.stdout, "added [advisor] to "+file+", commented out: uncomment one example to use it\n")
	data, err = os.ReadFile(file)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(data), "model = \"gpt-6.2-sol\"\n\n# uah config update: [advisor]"))

	res = uahWith(t, env, "", "config", "update", "--config", file)
	require.Equal(t, 0, res.code, res.stderr)
	assert.Contains(t, res.stdout, "has every setting this uah describes")

	res = uahWith(t, env, "", "config", "update", "extra")
	assert.Equal(t, 2, res.code)
	for _, args := range [][]string{{"config", "update", "--json"}, {"config", "--json", "update"}} {
		res = uahWith(t, env, "", args...)
		assert.Equal(t, 2, res.code, "config's --json is its own: %q", args)
	}
}

// TestConfigUpdateNamesTheFile: update's own --config names the file, over
// UAH_CONFIG; a different --config before update is refused, not ignored;
// the same one, or UAH_CONFIG's, is fine.
func TestConfigUpdateNamesTheFile(t *testing.T) {
	t.Parallel()
	env := offlineEnv(t)
	envFile := filepath.Join(t.TempDir(), "env.toml")
	env = append(env, "UAH_CONFIG="+envFile)
	file := filepath.Join(t.TempDir(), "custom.toml")
	for _, args := range [][]string{
		{"config", "update", "--dry-run", "--config", file},
		{"--config", file, "config", "update", "--dry-run", "--config", file},
		{"--config", envFile, "config", "update", "--dry-run", "--config", file},
	} {
		res := uahWith(t, env, "", args...)
		require.Equal(t, 0, res.code, res.stderr)
		assert.Contains(t, res.stdout, "would add [advisor] to "+file+",", args)
	}
	for _, args := range [][]string{
		{"config", "--config", file, "update", "--dry-run"},
		{"--config", file, "config", "update", "--dry-run"},
	} {
		res := uahWith(t, env, "", args...)
		assert.Equal(t, 2, res.code, args)
		assert.Contains(t, res.stderr, "put --config after update", args)
	}
	res := uahWith(t, env, "", "--config", filepath.Dir(file)+"/cfg/", "config", "update", "--dry-run", "--config", filepath.Dir(file)+"/cfg")
	assert.Equal(t, 2, res.code, "a folder before update is refused")
	assert.Contains(t, res.stderr, "names a folder")
	for _, args := range [][]string{{"--config=", "config", "update", "--dry-run"}, {"config", "--config=", "update", "--dry-run"}} {
		res := uahWith(t, env, "", args...)
		assert.Equal(t, 2, res.code, args)
		assert.Contains(t, res.stderr, "names no file", args)
	}
	res = uahWith(t, env, "", "config", "update", "--dry-run")
	require.Equal(t, 0, res.code, res.stderr)
	assert.Contains(t, res.stdout, "would add [advisor] to "+envFile+",", "UAH_CONFIG")

	notYet := filepath.Join(t.TempDir(), "new.toml")
	res = uahWith(t, env, "", "--config", filepath.Dir(notYet)+"/./new.toml", "config", "update", "--dry-run", "--config", notYet)
	require.Equal(t, 0, res.code, "a file not made yet, spelled two ways: %s", res.stderr)
	link := filepath.Join(t.TempDir(), "link.toml")
	require.NoError(t, os.Symlink(notYet, link))
	res = uahWith(t, env, "", "--config", link, "config", "update", "--dry-run", "--config", notYet)
	require.Equal(t, 0, res.code, "a link to a file not made yet, and that file: %s", res.stderr)

	require.NoError(t, os.WriteFile(file, nil, 0o600))
	alias := filepath.Dir(file) + "/./" + filepath.Base(file) // not joined: Join would clean it
	res = uahWith(t, env, "", "--config", alias, "config", "update", "--dry-run", "--config", file)
	require.Equal(t, 0, res.code, "one file spelled two ways: %s", res.stderr)
}

// TestConfigUpdateEmptyUAHConfig: an empty UAH_CONFIG is unset, so the
// command takes the default file, as a session does.
func TestConfigUpdateEmptyUAHConfig(t *testing.T) {
	t.Parallel()
	env := offlineEnv(t)
	homeDir := t.TempDir()
	env = append(env, "UAH_CONFIG=", "UAH_HOME="+homeDir)
	res := uahWith(t, env, "", "config", "update", "--dry-run")
	require.Equal(t, 0, res.code, res.stderr)
	assert.Contains(t, res.stdout, "would add [advisor] to "+filepath.Join(homeDir, "config.toml")+",")
}

// offlineEnv is a home and a state directory of a test's own, for a command
// that needs no model.
func offlineEnv(t *testing.T) []string {
	t.Helper()

	return []string{"UAH_HOME=" + t.TempDir(), "UAH_STATE_DIR=" + t.TempDir(), "UAH_CONFIG=", "UAH_EXTRA_CONFIG="}
}

// TestConfigUpdateOverUAHConfigFolder: update's own --config wins over a
// UAH_CONFIG that names a folder, unused here.
func TestConfigUpdateOverUAHConfigFolder(t *testing.T) {
	t.Parallel()
	env := append(offlineEnv(t), "UAH_CONFIG="+t.TempDir()+"/")
	file := filepath.Join(t.TempDir(), "user.toml")
	res := uahWith(t, env, "", "config", "update", "--config", file, "--dry-run")
	require.Equal(t, 0, res.code, res.stderr)
	assert.Contains(t, res.stdout, "would add [advisor] to "+file+",")
}

// TestConfigUpdateLinkToFolder: a --config before update that is a link to
// a folder not made yet is refused, not taken for the file after update.
func TestConfigUpdateLinkToFolder(t *testing.T) {
	t.Parallel()
	env := offlineEnv(t)
	dir := t.TempDir()
	future := filepath.Join(dir, "future-dir")
	link := filepath.Join(dir, "link")
	require.NoError(t, os.Symlink(future+"/", link))
	res := uahWith(t, env, "", "--config", link, "config", "update", "--config", future)
	assert.Equal(t, 2, res.code, res.stderr)
	assert.Contains(t, res.stderr, "names a folder")
	assert.NoFileExists(t, future)
}
