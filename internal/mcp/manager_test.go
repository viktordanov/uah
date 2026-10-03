package mcp_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/mcp"
	"github.com/viktordanov/uah/testing/harnesstest"
)

func ptr[T any](v T) *T { return &v }

func newManager(t *testing.T, servers map[string]mcp.ServerConfig) *mcp.Manager {
	t.Helper()
	m, err := mcp.NewManager(servers, mcp.Options{Workspace: t.TempDir(), Getenv: func(string) string { return "" }})
	require.NoError(t, err)
	t.Cleanup(func() { _ = m.Close() })

	return m
}

func stdio(t *testing.T) mcp.ServerConfig {
	t.Helper()

	return mcp.ServerConfig{Command: harnesstest.MCPServer(t), Env: map[string]string{"MCPSERVER_GREETING": "hi"}}
}

func names(tools []mcp.Tool) []string {
	out := make([]string, 0, len(tools))
	for _, tl := range tools {
		out = append(out, tl.Name)
	}

	return out
}

func TestToolsAndCalls(t *testing.T) {
	t.Parallel()
	cfg := stdio(t)
	cfg.EnabledTools = []string{"echo", "fail", "image", "sleep", "structured", "crash"}
	cfg.DisabledTools = []string{"crash"}
	m := newManager(t, map[string]mcp.ServerConfig{"test-srv": cfg})
	tools, err := m.Tools(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{
		"mcp__test_srv__echo", "mcp__test_srv__fail", "mcp__test_srv__image", "mcp__test_srv__sleep", "mcp__test_srv__structured",
	}, names(tools))
	assert.Equal(t, "echo", tools[0].Tool)
	assert.Equal(t, "object", tools[0].InputSchema["type"])

	ctx := context.Background()
	r, err := m.Call(ctx, "test-srv", "echo", json.RawMessage(`{"text":"hello"}`))
	require.NoError(t, err)
	assert.Equal(t, "echo: hello env=hi", r.Text, "env reaches the server")

	r, err = m.Call(ctx, "test-srv", "image", nil)
	require.NoError(t, err)
	assert.Equal(t, "a pixel", r.Text)
	require.Len(t, r.Images, 1)
	assert.True(t, strings.HasPrefix(r.Images[0], "data:image/png;base64,iVBORw0KGgo"))

	r, err = m.Call(ctx, "test-srv", "structured", nil)
	require.NoError(t, err)
	assert.JSONEq(t, `{"n":42}`, r.Text, "structured content replaces the content")

	r, err = m.Call(ctx, "test-srv", "fail", nil)
	require.NoError(t, err)
	assert.True(t, r.IsError)
	assert.Equal(t, "it failed", r.Text)
}

func TestEnabledTools(t *testing.T) {
	t.Parallel()
	cfg := stdio(t)
	cfg.EnabledTools = []string{"echo", "sleep"}
	cfg.DisabledTools = []string{"sleep"}
	m := newManager(t, map[string]mcp.ServerConfig{"s": cfg})
	tools, err := m.Tools(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"mcp__s__echo"}, names(tools))
}

func TestToolTimeout(t *testing.T) {
	t.Parallel()
	cfg := stdio(t)
	cfg.ToolTimeoutSec = ptr(0.2)
	m := newManager(t, map[string]mcp.ServerConfig{"s": cfg})
	_, err := m.Call(context.Background(), "s", "sleep", nil)
	require.Error(t, err, "not started yet")
	_, err = m.Tools(context.Background())
	require.NoError(t, err)
	start := time.Now()
	_, err = m.Call(context.Background(), "s", "sleep", json.RawMessage(`{"ms":1000}`))
	require.ErrorContains(t, err, "did not finish within 200ms")
	assert.Less(t, time.Since(start), 3*time.Second)
	r, err := m.Call(context.Background(), "s", "echo", json.RawMessage(`{"text":"after"}`))
	require.NoError(t, err, "the server still works")
	assert.Contains(t, r.Text, "after")
}

func TestStartupFailures(t *testing.T) {
	t.Parallel()
	slow := stdio(t)
	slow.Env["MCPSERVER_START_DELAY"] = "1s"
	slow.StartupTimeoutSec = ptr(0.3)
	m := newManager(t, map[string]mcp.ServerConfig{
		"slow":     slow,
		"missing":  {Command: "/nonexistent/mcp-server"},
		"ok":       stdio(t),
		"disabled": {Command: "/nonexistent/mcp-server", Enabled: ptr(false)},
	})
	tools, err := m.Tools(context.Background())
	require.NoError(t, err, "servers that fail do not stop the others")
	assert.Contains(t, names(tools), "mcp__ok__echo")
	byName := map[string]mcp.ServerStatus{}
	for _, s := range m.Status() {
		byName[s.Name] = s
	}
	assert.Equal(t, mcp.StateFailed, byName["slow"].State)
	assert.Contains(t, byName["slow"].Error, "did not start within 300ms")
	assert.Equal(t, mcp.StateFailed, byName["missing"].State)
	assert.Equal(t, mcp.StateDisabled, byName["disabled"].State)
	assert.Equal(t, mcp.StateReady, byName["ok"].State)
	assert.Contains(t, names(byName["ok"].Tools), "mcp__ok__echo")

	required := newManager(t, map[string]mcp.ServerConfig{"missing": {Command: "/nonexistent/mcp-server", Required: true}})
	_, err = required.Tools(context.Background())
	require.ErrorContains(t, err, "the required MCP server missing failed to start")
}

func TestValidate(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		cfg  mcp.ServerConfig
		want string
	}{
		"neither":       {cfg: mcp.ServerConfig{}, want: "set command"},
		"both":          {cfg: mcp.ServerConfig{Command: "x", URL: "http://x"}, want: "not both"},
		"http key":      {cfg: mcp.ServerConfig{Command: "x", BearerTokenEnvVar: "T"}, want: "bearer_token_env_var is not supported for stdio"},
		"stdio key":     {cfg: mcp.ServerConfig{URL: "http://x", Args: []string{"a"}}, want: "args is not supported for streamable_http"},
		"approval mode": {cfg: mcp.ServerConfig{Command: "x", Tools: map[string]mcp.ToolConfig{"t": {ApprovalMode: "deny"}}}, want: "unknown approval mode"},
		"timeout":       {cfg: mcp.ServerConfig{Command: "x", ToolTimeoutSec: ptr(0.0)}, want: "positive"},
		"stdio oauth":   {cfg: mcp.ServerConfig{Command: "x", Scopes: []string{"a"}}, want: "scopes is not supported for stdio"},
		"auth":          {cfg: mcp.ServerConfig{URL: "http://x", Auth: "chatgpt"}, want: `auth "chatgpt" is not supported`},
		"port":          {cfg: mcp.ServerConfig{URL: "http://x", OAuth: &mcp.OAuthConfig{CallbackPort: ptr(0)}}, want: "not a port"},
		"callback":      {cfg: mcp.ServerConfig{URL: "http://x", OAuth: &mcp.OAuthConfig{CallbackURL: "/cb"}}, want: "not an http(s) URL"},
	} {
		t.Run(name, func(t *testing.T) {
			require.ErrorContains(t, tc.cfg.Validate(), tc.want)
		})
	}
	cfg := mcp.ServerConfig{Command: "x", DefaultToolsApprovalMode: mcp.ApprovalWrites, Tools: map[string]mcp.ToolConfig{"t": {ApprovalMode: mcp.ApprovalApprove}}}
	require.NoError(t, cfg.Validate())
	assert.Equal(t, mcp.ApprovalApprove, cfg.ApprovalFor("t"))
	assert.Equal(t, mcp.ApprovalWrites, cfg.ApprovalFor("u"))
	assert.Equal(t, mcp.ApprovalAuto, mcp.ServerConfig{}.ApprovalFor("u"))
	assert.Equal(t, 30*time.Second, mcp.ServerConfig{}.StartupTimeout())
	assert.Equal(t, 1500*time.Millisecond, mcp.ServerConfig{StartupTimeoutMs: ptr(int64(1500))}.StartupTimeout())
	assert.Equal(t, 300*time.Second, mcp.ServerConfig{}.ToolTimeout())
}
