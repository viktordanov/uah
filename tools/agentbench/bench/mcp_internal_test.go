package bench

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestUAHConfigWithMCP: a task's MCP servers follow the shared file's
// lines as tables, with {{TASK_DIR}} the task's directory.
func TestUAHConfigWithMCP(t *testing.T) {
	dir, art := t.TempDir(), t.TempDir()
	base := filepath.Join(dir, "uah.toml")
	require.NoError(t, os.WriteFile(base, []byte("permission_mode = \"auto\"\n"), 0o644))
	task := Task{Dir: filepath.Join(dir, "task"), MCPServers: map[string]MCPServer{"incidents": {Command: "python3", Args: []string{"-I", "{{TASK_DIR}}/../../mcp/incidents.py"}}}}

	path, err := task.uahConfigWithMCP(base, art)
	require.NoError(t, err)
	var conf struct {
		PermissionMode string `toml:"permission_mode"`
		MCP            map[string]struct {
			Command string   `toml:"command"`
			Args    []string `toml:"args"`
		} `toml:"mcp_servers"`
	}
	_, err = toml.DecodeFile(path, &conf)
	require.NoError(t, err)
	assert.Equal(t, "auto", conf.PermissionMode, "the shared lines come first")
	assert.Equal(t, "python3", conf.MCP["incidents"].Command)
	assert.Equal(t, []string{"-I", filepath.Join(dir, "task") + "/../../mcp/incidents.py"}, conf.MCP["incidents"].Args)
}

// TestTaskMCPNeedsUAHOnly: a task with MCP servers must be uah-only, and
// a server needs a plain name and a command.
func TestTaskMCPNeedsUAHOnly(t *testing.T) {
	dir := t.TempDir()
	for _, d := range []string{repoDir, solutionDir} {
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "t", d), 0o755))
	}
	write := func(body string) error {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "t", taskFile), []byte(body), 0o644))
		_, err := LoadTasks(dir, nil)

		return err
	}
	err := write(`{"prompt":"p","check":"true","exercises":"e","tags":["mcp"],"mcp_servers":{"x":{"command":"python3"}}}`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "MCP servers need the tag uah-only")
	err = write(`{"prompt":"p","check":"true","exercises":"e","tags":["uah-only"],"mcp_servers":{"bad name":{"command":""}}}`)
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "MCP server bad name"))
	require.NoError(t, write(`{"prompt":"p","check":"true","exercises":"e","tags":["uah-only"],"mcp_servers":{"x":{"command":"python3"}}}`))
}
