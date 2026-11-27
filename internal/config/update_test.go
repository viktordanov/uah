package config_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/config"
)

// TestAddNewSettings appends each new setting the file lacks, as comments,
// after what the file had; adds it once; leaves a file that sets it, or
// that is not TOML, as it is; and with dry writes nothing.
func TestAddNewSettings(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.toml")
	before := "# mine\nmodel = \"gpt-6.2-sol\""
	require.NoError(t, os.WriteFile(file, []byte(before), 0o600))
	require.NoError(t, os.Chmod(file, 0o640)) // whatever the umask

	added, err := config.AddNewSettings(file, "main", true)
	require.NoError(t, err)
	require.NotEmpty(t, added)
	assert.Equal(t, before, read(t, file), "dry writes nothing")

	added, err = config.AddNewSettings(file, "main", false)
	require.NoError(t, err)
	require.Len(t, added, len(config.NewSettings))
	text := read(t, file)
	assert.True(t, strings.HasPrefix(text, before+"\n\n"+config.NewSettings[0].Marker()), text)
	info, err := os.Stat(file)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o640), info.Mode().Perm(), "the file keeps its permissions")

	added, err = config.AddNewSettings(file, "main", false)
	require.NoError(t, err)
	assert.Empty(t, added, "once")
	assert.Equal(t, text, read(t, file))

	for _, sets := range []string{"[advisor]\nmodel = \"m\"\n", "[Advisor]\nmodel = \"m\"\n"} {
		set := filepath.Join(t.TempDir(), "config.toml")
		require.NoError(t, os.WriteFile(set, []byte(sets), 0o600))
		added, err = config.AddNewSettings(set, "main", false)
		require.NoError(t, err)
		assert.False(t, slices.ContainsFunc(added, isAdvisor), "the file sets it, in any case, as the loader reads it: %q", sets)
	}

	quoted := filepath.Join(t.TempDir(), "config.toml")
	prompt := "compact_prompt = \"\"\"\nkeep this:\n" + config.NewSettings[0].EndMarker() + "\n\"\"\"\n"
	require.NoError(t, os.WriteFile(quoted, []byte(prompt), 0o600))
	added, err = config.AddNewSettings(quoted, "main", false)
	require.NoError(t, err)
	assert.True(t, slices.ContainsFunc(added, isAdvisor), "the end marker as text in a string is not the block")

	nan := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(nan, []byte("[tui]\nscroll = nan\n"), 0o600))
	_, err = config.AddNewSettings(nan, "main", false)
	require.NoError(t, err)
	added, err = config.AddNewSettings(nan, "main", false)
	require.NoError(t, err)
	assert.Empty(t, added, "the block is found in a file with a NaN")

	readOnly := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(readOnly, []byte("[advisor]\nmodel = \"m\"\n"), 0o400))
	added, err = config.AddNewSettings(readOnly, "main", false)
	require.NoError(t, err, "a file that needs nothing is only read")
	assert.False(t, slices.ContainsFunc(added, isAdvisor))

	toFolder := filepath.Join(t.TempDir(), "config.toml")
	dotfiles := filepath.Join(t.TempDir(), "dotfiles") + "/"
	require.NoError(t, os.Symlink(dotfiles, toFolder))
	for _, dry := range []bool{true, false} {
		_, err = config.AddNewSettings(toFolder, "main", dry)
		require.ErrorContains(t, err, "names a folder")
	}
	assert.NoDirExists(t, dotfiles, "a link to a folder makes none")

	aliasDir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(aliasDir, "actual"), 0o700))
	require.NoError(t, os.Symlink("missing/../actual", filepath.Join(aliasDir, "alias")))
	_, err = config.AddNewSettings(filepath.Join(aliasDir, "alias", "config.toml"), "main", true)
	require.ErrorContains(t, err, "does not exist", "a .. in a folder link's target")
	assert.NoDirExists(t, filepath.Join(aliasDir, "missing"))

	through := filepath.Join(t.TempDir(), "missing") + "/../config.toml"
	for _, dry := range []bool{true, false} {
		_, err = config.AddNewSettings(through, "main", dry)
		require.ErrorContains(t, err, "does not exist", "dry %v", dry)
	}

	for _, end := range []string{"/", "/.", "/.."} {
		folder := filepath.Join(t.TempDir(), "missing", "new") + end
		for _, dry := range []bool{true, false} {
			_, err = config.AddNewSettings(folder, "main", dry)
			require.ErrorContains(t, err, "names a folder", folder)
		}
		assert.NoDirExists(t, filepath.Join(filepath.Dir(strings.TrimSuffix(folder, end)), "new"), "no folder is made for %q", folder)
	}

	odd := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(odd, []byte("\xff\xfemodel = \"m\"\n"), 0o600))
	if _, err := config.AddNewSettings(odd, "main", false); err == nil {
		again, err := config.AddNewSettings(odd, "main", false)
		require.NoError(t, err)
		assert.Empty(t, again, "a file the loader takes and the scanner may not: never added twice")
	}

	big := filepath.Join(t.TempDir(), "config.toml")
	f, err := os.Create(big)
	require.NoError(t, err)
	require.NoError(t, f.Truncate(17<<20)) // sparse: no disk used
	require.NoError(t, f.Close())
	_, err = config.AddNewSettings(big, "main", true)
	require.ErrorContains(t, err, "too large for a configuration")

	bad := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(bad, []byte("model = \n"), 0o600))
	_, err = config.AddNewSettings(bad, "main", false)
	require.ErrorContains(t, err, "is not valid TOML")
	assert.Equal(t, "model = \n", read(t, bad))
}

// TestAddNewSettingsCreatesAndFollowsLinks creates a missing file, and
// appends to a symlinked one through the link, which stays a link, also
// when the file it names does not exist yet; a device is refused.
func TestAddNewSettingsCreatesAndFollowsLinks(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "uah", "config.toml")
	added, err := config.AddNewSettings(missing, "main", false)
	require.NoError(t, err)
	require.NotEmpty(t, added)
	assert.True(t, strings.HasPrefix(read(t, missing), config.NewSettings[0].Marker()))

	target := filepath.Join(t.TempDir(), "dotfiles", "config.toml")
	require.NoError(t, os.MkdirAll(filepath.Dir(target), 0o700))
	require.NoError(t, os.WriteFile(target, []byte("model = \"m\"\n"), 0o600))
	link := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.Symlink(target, link))
	_, err = config.AddNewSettings(link, "main", false)
	require.NoError(t, err)
	info, err := os.Lstat(link)
	require.NoError(t, err)
	assert.Equal(t, os.ModeSymlink, info.Mode().Type(), "still a link")
	assert.Contains(t, read(t, target), config.NewSettings[0].Marker())

	dangling, notYet := filepath.Join(t.TempDir(), "config.toml"), filepath.Join(t.TempDir(), "not-yet.toml")
	require.NoError(t, os.Symlink(notYet, dangling))
	_, err = config.AddNewSettings(dangling, "main", false)
	require.NoError(t, err)
	info, err = os.Lstat(dangling)
	require.NoError(t, err)
	assert.Equal(t, os.ModeSymlink, info.Mode().Type(), "a link to a missing file stays a link")
	assert.Contains(t, read(t, notYet), config.NewSettings[0].Marker(), "and the file it names is created")

	deep, deepTarget := filepath.Join(t.TempDir(), "config.toml"), filepath.Join(t.TempDir(), "dotfiles", "uah", "config.toml")
	require.NoError(t, os.Symlink(deepTarget, deep))
	_, err = config.AddNewSettings(deep, "main", false)
	require.NoError(t, err, "the folder the link leads to is made too")
	assert.Contains(t, read(t, deepTarget), config.NewSettings[0].Marker())

	real := filepath.Join(t.TempDir(), "real", "sub")
	require.NoError(t, os.MkdirAll(real, 0o700))
	linkedDir := filepath.Join(t.TempDir(), "linked-dir")
	require.NoError(t, os.Symlink(real, linkedDir))
	through := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.Symlink(linkedDir+"/../new/config.toml", through))
	_, err = config.AddNewSettings(through, "main", false)
	require.NoError(t, err, "a .. after a linked folder is resolved where the link leads")
	assert.FileExists(t, filepath.Join(filepath.Dir(real), "new", "config.toml"))

	home := filepath.Join(t.TempDir(), "uah-home")
	require.NoError(t, os.Symlink(filepath.Join(t.TempDir(), "dotfiles", "uah"), home))
	_, err = config.AddNewSettings(filepath.Join(home, "config.toml"), "main", false)
	require.NoError(t, err, "a folder link to a folder not made yet")
	assert.FileExists(t, filepath.Join(home, "config.toml"))

	t.Chdir(t.TempDir())
	require.NoError(t, os.Symlink("dotfiles/user.toml", "config.toml"))
	_, err = config.AddNewSettings("config.toml", "main", false)
	require.NoError(t, err, "a link with no folder in its name resolves from here")
	assert.FileExists(t, filepath.Join("dotfiles", "user.toml"))

	device := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.Symlink(os.DevNull, device))
	_, err = config.AddNewSettings(device, "main", false)
	require.ErrorContains(t, err, "is not a regular file")
}

// TestAddNewSettingsOnce: updates that overlap add a block once; a block
// an append left without its end is added again; a FIFO is refused at
// once, not waited on.
func TestAddNewSettingsOnce(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(file, []byte("model = \"m\"\n"), 0o600))
	var wg sync.WaitGroup
	for range 7 {
		wg.Go(func() {
			_, err := config.AddNewSettings(file, "main", false)
			assert.NoError(t, err)
		})
	}
	wg.Wait()
	assert.Equal(t, 1, strings.Count(read(t, file), config.NewSettings[0].EndMarker()))

	bom := filepath.Join(t.TempDir(), "config.toml")
	_, err := config.AddNewSettings(bom, "main", false)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(bom, append([]byte("\uFEFF"), []byte(read(t, bom))...), 0o600))
	again, err := config.AddNewSettings(bom, "main", false)
	require.NoError(t, err)
	assert.False(t, slices.ContainsFunc(again, isAdvisor), "a block after a BOM is found")

	footer := filepath.Join(t.TempDir(), "config.toml")
	end := config.NewSettings[0].EndMarker() + "\n"
	require.NoError(t, os.WriteFile(footer, []byte(end+end), 0o600))
	added, err := config.AddNewSettings(footer, "main", false)
	require.NoError(t, err)
	assert.True(t, slices.ContainsFunc(added, isAdvisor), "an end without its start is not the block")

	fresh := filepath.Join(t.TempDir(), "config.toml")
	var wg2 sync.WaitGroup
	for range 7 {
		wg2.Go(func() {
			_, err := config.AddNewSettings(fresh, "main", false)
			assert.NoError(t, err, "runs that all found no file")
		})
	}
	wg2.Wait()
	assert.Equal(t, 1, strings.Count(read(t, fresh), config.NewSettings[0].EndMarker()))

	partial := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(partial, []byte(config.NewSettings[0].Marker()+"\n# The agent"), 0o600))
	added, err = config.AddNewSettings(partial, "main", false)
	require.NoError(t, err)
	assert.NotEmpty(t, added, "a block without its end")
	assert.Contains(t, read(t, partial), config.NewSettings[0].EndMarker())

	through := filepath.Join(t.TempDir(), "dest", "child")
	require.NoError(t, os.MkdirAll(through, 0o700))
	link := filepath.Join(t.TempDir(), "link")
	require.NoError(t, os.Symlink(through, link))
	_, err = config.AddNewSettings(link+"/../new/config.toml", "main", false)
	require.NoError(t, err, "the folder is made where the link's .. leads")
	assert.FileExists(t, filepath.Join(filepath.Dir(through), "new", "config.toml"))

	fifo := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, syscall.Mkfifo(fifo, 0o600))
	for _, dry := range []bool{true, false} {
		_, err = config.AddNewSettings(fifo, "main", dry)
		require.ErrorContains(t, err, "is not a regular file")
	}
}

// TestAddNewSettingsThenConfigThenUncomment: a file uah config update made
// takes a /config save above the block, so uncommenting the block's first
// example leaves the saved key at the root.
func TestAddNewSettingsThenConfigThenUncomment(t *testing.T) {
	t.Setenv("UAH_HOME", t.TempDir())
	t.Setenv("UAH_EXTRA_CONFIG", "")
	file := filepath.Join(t.TempDir(), "config.toml")
	_, err := config.AddNewSettings(file, "main", false)
	require.NoError(t, err)
	require.NoError(t, config.SetValue(file, "effort", "high"))
	text := read(t, file)
	commented := "# [advisor]\n# model = \"gpt-6.2-astra\"\n# effort = \"xhigh\"\n"
	require.Contains(t, text, commented)
	text = strings.Replace(text, commented, "[advisor]\nmodel = \"gpt-6.2-astra\"\neffort = \"xhigh\"\n", 1)
	require.NoError(t, os.WriteFile(file, []byte(text), 0o600))
	cfg, _, err := config.Load(file, t.TempDir())
	require.NoError(t, err)
	assert.Equal(t, "high", cfg.Effort, "the saved key stays at the root")
	assert.Equal(t, "xhigh", cfg.Advisor.Effort)
}

func read(t *testing.T, file string) string {
	t.Helper()
	data, err := os.ReadFile(file)
	require.NoError(t, err)

	return string(data)
}

func isAdvisor(n config.NewSetting) bool { return n.Key == "advisor" }
