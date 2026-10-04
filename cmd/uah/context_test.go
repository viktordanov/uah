package main_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestContextCommand lists the modules for a workspace, with why each
// applies or not, as text and as JSON; --show prints the context a new
// main session and a read-only subagent get; trust allows a project
// module.
func TestContextCommand(t *testing.T) {
	t.Parallel()
	_, env := mcpEnv(t)
	ws := t.TempDir()
	writeFile(t, filepath.Join(ws, ".uah", "context.d", "repo.md"), "---\nid: repo\ndescription: The repo's notes\n---\nRepo note.\n")
	fish := filepath.Join(t.TempDir(), "fish") // $SHELL must be an executable file
	require.NoError(t, os.WriteFile(fish, []byte("#!/bin/sh\n"), 0o755))
	env = append(env, "SHELL="+fish)

	res := uahWith(t, env, "", "context", "-C", ws)
	require.Equal(t, 0, res.code, res.stderr)
	lines := strings.Split(res.stdout, "\n")
	assert.Regexp(t, `^BLOCK\s+MODULE\s+SOURCE\s+STATE\s+APPLIES\s+WHY$`, lines[0])
	assert.Regexp(t, `(?m)^environment\s+environment/fish\s+builtin\s+on\s+yes\s+applies$`, res.stdout)
	assert.Regexp(t, `(?m)^environment\s+environment/zsh\s+builtin\s+on\s+no\s+when\.shell: fish is not zsh$`, res.stdout)
	assert.Regexp(t, `(?m)^go\s+library/go\s+library\s+off\s+no\s+disabled`, res.stdout)
	assert.Regexp(t, `(?m)^repo\s+context\.d/repo\s+project \S+repo\.md\s+untrusted\s+no\s+untrusted project module`, res.stdout)

	res = uahWith(t, env, "", "context", "-C", ws, "--json", "--show")
	require.Equal(t, 0, res.code, res.stderr)
	var out struct {
		Workspace string `json:"workspace"`
		Modules   []struct {
			Path    string `json:"path"`
			Applies bool   `json:"applies"`
			Reason  string `json:"reason"`
		} `json:"modules"`
		Main     string `json:"main"`
		Subagent string `json:"subagent"`
	}
	require.NoError(t, json.Unmarshal([]byte(res.stdout), &out), res.stdout)
	assert.NotEmpty(t, out.Modules)
	assert.Contains(t, out.Main, "<context_preparation>\n")
	assert.Contains(t, out.Main, "Commands run in fish ("+fish+" -c)")
	assert.NotContains(t, out.Main, "$SHELL was unset", "$SHELL is used")
	assert.NotContains(t, out.Main, "Repo note.")
	assert.Contains(t, out.Subagent, "<context_preparation>\n")

	res = uahWith(t, env, "", "context", "trust", "-C", ws)
	require.Equal(t, 0, res.code, res.stderr)
	assert.Contains(t, res.stdout, "trusted "+filepath.Join(ws, ".uah", "context.d", "repo.md"))
	res = uahWith(t, env, "", "context", "-C", ws, "--show")
	require.Equal(t, 0, res.code, res.stderr)
	assert.Contains(t, res.stdout, "── a new main session ──\n<context_preparation>")
	assert.Contains(t, res.stdout, "\n## repo\nRepo note.\n")
	assert.Contains(t, res.stdout, "── a read-only subagent ──\n")
}

// TestContextStrippedEnvironment runs `uah context --show` as a service
// that starts uah without the user's environment would: no SHELL, no
// locale, and a minimal PATH. The context says so, and names the tool
// directory that exists but PATH lacks.
func TestContextStrippedEnvironment(t *testing.T) {
	t.Parallel()
	_, env := mcpEnv(t)
	home := t.TempDir()
	gobin := filepath.Join(home, "go", "bin")
	require.NoError(t, os.MkdirAll(gobin, 0o755))
	env = append(env, "SHELL=", "LANG=", "LC_ALL=", "LC_CTYPE=", "PATH=/usr/bin:/bin", "HOME="+home)

	res := uahWith(t, env, "", "context", "-C", t.TempDir(), "--json", "--show")
	require.Equal(t, 0, res.code, res.stderr)
	var out struct {
		Main string `json:"main"`
	}
	require.NoError(t, json.Unmarshal([]byte(res.stdout), &out), res.stdout)
	assert.Regexp(t, `\$SHELL was unset or not an executable file when uah started, (so uah picked your login shell|and the login shell could not be read)`, out.Main)
	assert.Contains(t, out.Main, "The locale is not UTF-8 (LC_ALL, LC_CTYPE, and LANG unset)")
	assert.Contains(t, out.Main, "PATH has none of the user's tool directories; these exist: ")
	assert.Contains(t, out.Main, gobin)
}
