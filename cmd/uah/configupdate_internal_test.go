package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestUpdateCommand always names the file by its absolute path, and offers
// no command for a path that needs quoting, which each shell does its way.
func TestUpdateCommand(t *testing.T) {
	assert.Equal(t, "uah config update --config /etc/uah.toml", updateCommand("/etc/uah.toml"))
	cwd, err := os.Getwd()
	require.NoError(t, err)
	if shellQuote(cwd) == cwd {
		assert.Equal(t, "uah config update --config "+cwd+"/rel.toml", updateCommand("rel.toml"), "an absolute path")
	}
	for _, path := range []string{"/tmp/my dir/c.toml", "/tmp/it's.toml", "/tmp/a\tb.toml", `/tmp/a\\b.toml`, "/tmp/~/c.toml", os.DevNull, "/tmp/missing/", "/tmp/missing/..", "/tmp/no-such-dir-uah/../user.toml"} {
		assert.Empty(t, updateCommand(path), "%q needs quoting", path)
	}
}

// TestDocsRef links the docs of the version that wrote the block.
func TestDocsRef(t *testing.T) {
	for version, want := range map[string]string{
		"v1.18.0":                                     "v1.18.0",
		"v1.17.3-0.20261127114037-1eaffbffad63":       "1eaffbffad63",
		"v1.17.3-0.20261127114037-1eaffbffad63+dirty": "1eaffbffad63",
		"v1.18.0-rc.1.0.20261127114037-1eaffbffad63":  "1eaffbffad63",
		"v1.18.0-rc.1":                                "v1.18.0-rc.1",
		"v0.0.0-20261127114037-1eaffbffad63":          "1eaffbffad63",
		"dev":                                         "main",
		"":                                            "main",
	} {
		assert.Equal(t, want, docsRef(version), version)
	}
}

// TestSameFileLoop: a symlink loop ends the comparison, not the program.
func TestSameFileLoop(t *testing.T) {
	dir := t.TempDir()
	loop := filepath.Join(dir, "loop")
	require.NoError(t, os.Symlink("loop/child", loop))
	assert.False(t, sameFile(loop+"/a.toml", loop+"/./a.toml"))

	newDir := filepath.Join(dir, "newdir")
	assert.True(t, sameFile(newDir+"/./user.toml", newDir+"/user.toml"), "a . names nothing")
	assert.True(t, sameFile(newDir+"//user.toml", newDir+"/user.toml"), "nor does a doubled separator")
}
