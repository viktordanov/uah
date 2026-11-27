package config

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewSettingsCoverNewKeys: each key the v1.17.2 baseline lacks belongs
// to a new setting, so the notice and `uah config update` describe it, and
// each new setting covers a key there is.
func TestNewSettingsCoverNewKeys(t *testing.T) {
	baseline := strings.Fields(baselineKeys)
	require.Greater(t, len(baseline), 100)
	keys := Keys()
	for _, k := range keys {
		if !slices.Contains(baseline, k) {
			assert.True(t, slices.ContainsFunc(NewSettings, func(n NewSetting) bool { return n.Covers(k) }),
				"%s is new since v1.17.2: describe it in NewSettings (ledger rule 8)", k)
		}
	}
	for _, n := range NewSettings {
		assert.True(t, n.Covers(n.Key), "%s announces itself", n.Key)
		for _, k := range n.Introduced {
			assert.Contains(t, keys, k, "%s announces a key there is", n.Key)
			assert.NotContains(t, baseline, k, "%s: %s was in v1.17.2", n.Key, k)
		}
	}
}

// TestNewSettingsComment: the block is comments only, starts with its
// marker, and each example loads as a user file; the file with the block
// appended loads as it did.
func TestNewSettingsComment(t *testing.T) {
	t.Setenv("UAH_HOME", t.TempDir())
	t.Setenv("UAH_EXTRA_CONFIG", "")
	for _, n := range NewSettings {
		block := n.Comment("main")
		assert.True(t, strings.HasPrefix(block, n.Marker()+"\n"), n.Key)
		for line := range strings.SplitSeq(strings.TrimSuffix(block, "\n"), "\n") {
			assert.True(t, strings.HasPrefix(line, "#"), "%s: %q", n.Key, line)
			assert.LessOrEqual(t, len(line), 200, "%s: %q", n.Key, line)
		}
		for _, e := range n.Examples {
			load(t, e.TOML)
			var doc map[string]any
			_, err := toml.Decode(e.TOML, &doc)
			require.NoError(t, err)
			assert.True(t, hasKey(doc, n.Key), "%s: %s sets it", n.Key, e.Title)
		}
		before := `model = "gpt-6.2-sol"` + "\n"
		assert.Equal(t, load(t, before), load(t, before+"\n"+block), "%s: comments only", n.Key)
	}
}

// TestNewSettingsClaudeMatchesTheReference: the claude -p example has the
// arguments of the one docs/configuration.md documents.
func TestNewSettingsClaudeMatchesTheReference(t *testing.T) {
	t.Setenv("UAH_HOME", t.TempDir())
	t.Setenv("UAH_EXTRA_CONFIG", "")
	doc, err := os.ReadFile("../../docs/configuration.md")
	require.NoError(t, err)
	block := regexp.MustCompile("(?s)```toml\n(\\[advisor\\]\ninput.*?\"claude\".*?)```").FindSubmatch(doc)
	require.Len(t, block, 2)
	want, got := load(t, string(block[1])).Advisor, load(t, claudeAdvisor).Advisor
	assert.Equal(t, want.Command, got.Command)
	assert.Equal(t, want.Input, got.Input)
}

func load(t *testing.T, text string) Config {
	t.Helper()
	file := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(file, []byte(text), 0o600))
	cfg, _, err := Load(file, t.TempDir())
	require.NoError(t, err)

	return cfg
}

// TestUnseen: a new home has seen every key; a home used before the record
// has seen v1.17.2's; record writes every key, keeping those another
// session wrote, so nothing is new twice, and until it runs the keys stay
// unseen; a key the record lacks is new; and a record that cannot be
// locked or read names nothing.
func TestUnseen(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "settings-seen")
	unseen, record := Unseen(file, false)
	assert.Empty(t, unseen, "a new home")
	assert.NoFileExists(t, file, "not before record")
	require.NoError(t, record())
	data, err := os.ReadFile(file)
	require.NoError(t, err)
	assert.Equal(t, string(seenText(Keys())), string(data))
	assert.True(t, strings.HasPrefix(string(data), seenHeader+"\n# "), "a header, then a comment line per key")
	unseen, _ = Unseen(file, true)
	assert.Empty(t, unseen, "recorded")

	require.NoError(t, os.Remove(file))
	unseen, record = Unseen(file, true)
	require.NotEmpty(t, unseen, "used with v1.17.2")
	assert.Contains(t, unseen, "advisor")
	assert.Contains(t, unseen, "advisor.command")
	assert.NotContains(t, unseen, "model")
	again, _ := Unseen(file, true)
	assert.Equal(t, unseen, again, "unseen until recorded: a session that failed to open names them again")
	require.NoError(t, os.WriteFile(file, seenText([]string{"from.another.session"}), 0o600))
	require.NoError(t, record())
	data, err = os.ReadFile(file)
	require.NoError(t, err)
	assert.Contains(t, string(data), "# from.another.session\n", "another session's keys stay")
	unseen, _ = Unseen(file, true)
	assert.Empty(t, unseen, "named once")

	require.NoError(t, os.WriteFile(file, seenText([]string{"model", "advisor"}), 0o600))
	unseen, _ = Unseen(file, false)
	assert.Contains(t, unseen, "advisor.command", "a key the record lacks")

	unseen, _ = Unseen(dir, true)
	assert.Empty(t, unseen, "a record that cannot be read")
	foreign := filepath.Join(t.TempDir(), "settings-seen")
	require.NoError(t, os.WriteFile(foreign, []byte("my notes\n"), 0o600))
	unseen, _ = Unseen(foreign, true)
	assert.Empty(t, unseen, "a file that is not uah's record")
	assert.Equal(t, "my notes\n", readFile(t, foreign), "is left as it is")
	withSetting := filepath.Join(t.TempDir(), "settings-seen")
	saved := string(seenText([]string{"model"})) + "effort = \"high\"\n"
	require.NoError(t, os.WriteFile(withSetting, []byte(saved), 0o600))
	unseen, _ = Unseen(withSetting, true)
	assert.Empty(t, unseen, "a record with a setting saved in it")
	assert.Equal(t, saved, readFile(t, withSetting), "is left as it is")
	linked := filepath.Join(t.TempDir(), "settings-seen")
	require.NoError(t, os.Symlink(filepath.Join(t.TempDir(), "elsewhere"), linked))
	unseen, record = Unseen(linked, true)
	assert.Empty(t, unseen, "a symlink, even to a file not made yet")
	require.NoError(t, record())
	info, err := os.Lstat(linked)
	require.NoError(t, err)
	assert.Equal(t, os.ModeSymlink, info.Mode().Type(), "stays a link")

	lockDir := t.TempDir()
	target := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.Symlink(target, filepath.Join(lockDir, "settings-seen.lock")))
	unseen, _ = Unseen(filepath.Join(lockDir, "settings-seen"), true)
	assert.Empty(t, unseen, "a symlinked lock names nothing")
	assert.NoFileExists(t, target, "and makes nothing where it leads")
	unseen, record = Unseen(filepath.Join(dir, "home", "settings-seen"), false)
	assert.Empty(t, unseen, "a home not made yet")
	require.NoError(t, record())
	assert.FileExists(t, filepath.Join(dir, "home", "settings-seen"), "is made for the record")
	fifo := filepath.Join(t.TempDir(), "settings-seen")
	require.NoError(t, syscall.Mkfifo(fifo, 0o600))
	unseen, _ = Unseen(fifo, true)
	assert.Empty(t, unseen, "a FIFO is refused, not waited on")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "file"), nil, 0o600))
	unseen, _ = Unseen(filepath.Join(dir, "file", "settings-seen"), true)
	assert.Empty(t, unseen, "nor locked")

	require.NoError(t, os.Remove(file))
	unlock, err := lockFile(file + ".lock")
	require.NoError(t, err)
	unseen, _ = Unseen(file, true)
	assert.Empty(t, unseen, "another session holds the record")
	unlock()
	unseen, _ = Unseen(file, true)
	assert.NotEmpty(t, unseen, "then this one names them")
}

// TestNewSettingsNotice names each new setting with what to do, and the
// keys no setting covers.
func TestNewSettingsNotice(t *testing.T) {
	notes := NewSettingsNotice([]string{"advisor", "advisor.model", "x.y"}, "/home/me/.uah/config.toml", "uah config update")
	require.Len(t, notes, 2)
	assert.Contains(t, notes[0], "New setting [advisor]: The agent can ask")
	assert.Contains(t, notes[0], "`uah config update` adds it to /home/me/.uah/config.toml, commented out, if it is not there yet")
	assert.Contains(t, notes[0], `"How do I set up the advisor?"`)
	assert.Equal(t, "New settings: `x.y`; docs/configuration.md describes them", notes[1])
	assert.Empty(t, NewSettingsNotice(nil, "f", "uah config update"))
	for _, args := range [][2]string{{"", ""}, {`/tmp/a\b.toml`, ""}} {
		notes = NewSettingsNotice([]string{"advisor"}, args[0], args[1])
		require.Len(t, notes, 1)
		assert.NotContains(t, notes[0], "uah config update", "no file, or no command that copies safely: nothing to run")
	}
}

// TestUsedBefore looks for the user file, and for sessions or the index in
// the state directory, wherever they are.
func TestUsedBefore(t *testing.T) {
	t.Setenv("UAH_HOME", t.TempDir())
	user, state := filepath.Join(t.TempDir(), "config.toml"), t.TempDir()
	assert.False(t, UsedBefore(user, state))
	assert.False(t, UsedBefore(os.DevNull, state), "a device as the user file is no sign of use")
	require.NoError(t, os.Mkdir(filepath.Join(state, "sessions"), 0o700))
	assert.True(t, UsedBefore(user, state))
	assert.False(t, UsedBefore(user, t.TempDir()))
	require.NoError(t, os.WriteFile(user, nil, 0o600))
	assert.True(t, UsedBefore(user, t.TempDir()))
	require.NoError(t, os.Mkdir(filepath.Join(Dir(), "sessions"), 0o700))
	assert.True(t, UsedBefore(filepath.Join(t.TempDir(), "none.toml"), t.TempDir()), "the home's sessions, with the state elsewhere now")
}

// TestSeenRecordIsEmptyTOML: the record loads as a configuration with
// nothing set, so a path that names it by mistake sets nothing.
func TestSeenRecordIsEmptyTOML(t *testing.T) {
	t.Setenv("UAH_HOME", t.TempDir())
	t.Setenv("UAH_EXTRA_CONFIG", "")
	assert.Equal(t, load(t, ""), load(t, string(seenText(Keys()))))
}

func readFile(t *testing.T, file string) string {
	t.Helper()
	data, err := os.ReadFile(file)
	require.NoError(t, err)

	return string(data)
}

// TestAddSeenLeavesAForeignFile: a file at the record's path that is not
// uah's record is kept, and so is a symlink there.
func TestAddSeenLeavesAForeignFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "settings-seen")
	require.NoError(t, os.WriteFile(file, []byte("synced\n"), 0o600))
	require.Error(t, addSeen(file, Keys()))
	assert.Equal(t, "synced\n", readFile(t, file))

	link := filepath.Join(t.TempDir(), "settings-seen")
	require.NoError(t, os.Symlink(file, link))
	require.Error(t, addSeen(link, Keys()))
	assert.Equal(t, "synced\n", readFile(t, file))
}

// TestAddSeenAppends: keys go at the end of the record it read, so an edit
// made since stays; a record cut short is still uah's.
func TestAddSeenAppends(t *testing.T) {
	file := filepath.Join(t.TempDir(), "settings-seen")
	require.NoError(t, addSeen(file, []string{"model"}))
	require.NoError(t, addSeen(file, []string{"model", "advisor"}))
	assert.Equal(t, seenHeader+"\n# model\n# advisor\n", readFile(t, file))

	for _, cut := range []string{seenHeader[:20], seenHeader + "\n# model\n# advis"} {
		file := filepath.Join(t.TempDir(), "settings-seen")
		require.NoError(t, os.WriteFile(file, []byte(cut), 0o600))
		unseen, record := Unseen(file, false)
		assert.Contains(t, unseen, "advisor", "a key the cut record lost is named again: %q", cut)
		require.NoError(t, record())
		unseen, _ = Unseen(file, false)
		assert.Empty(t, unseen, "and the record, mended, holds every key: %q", readFile(t, file))
		assert.True(t, strings.HasPrefix(readFile(t, file), seenHeader+"\n"))
	}

	for _, theirs := range []string{"", "#"} {
		file := filepath.Join(t.TempDir(), "settings-seen")
		require.NoError(t, os.WriteFile(file, []byte(theirs), 0o600))
		unseen, record := Unseen(file, true)
		assert.Empty(t, unseen, "%q is not uah's", theirs)
		require.NoError(t, record())
		assert.Equal(t, theirs, readFile(t, file), "and is left as it is")
	}
}

// seenText is a record of keys, as addSeen writes one.
func seenText(keys []string) []byte {
	var b strings.Builder
	b.WriteString(seenHeader + "\n")
	for _, k := range keys {
		b.WriteString("# " + k + "\n")
	}

	return []byte(b.String())
}

// TestNewSettingsAreTables: each entry is a top-level table whose examples
// open it, since its block goes at the end of the file, where only a table
// of its own keeps its keys in scope once uncommented.
func TestNewSettingsAreTables(t *testing.T) {
	for _, n := range NewSettings {
		assert.NotContains(t, n.Key, ".", "%s is a top-level table", n.Key)
		for _, e := range n.Examples {
			assert.True(t, strings.HasPrefix(e.TOML, "["+n.Key+"]\n"), "%s: %s opens its table", n.Key, e.Title)
		}
	}
}
