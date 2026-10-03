package mcp_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/mcp"
)

// fastRestarts is a manager whose servers restart after 10 ms, then 20 ms,
// and so on, logging to buf.
func fastRestarts(t *testing.T, servers map[string]mcp.ServerConfig, buf *safeBuffer) *mcp.Manager {
	t.Helper()
	m, err := mcp.NewManager(servers, mcp.Options{
		Workspace: t.TempDir(), Getenv: func(string) string { return "" }, RestartDelay: 10 * time.Millisecond,
		Logger: slog.New(slog.NewTextHandler(buf, nil)),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = m.Close() })

	return m
}

func connections(t *testing.T, log string) int {
	t.Helper()
	data, err := os.ReadFile(log)
	if err != nil {
		return 0
	}

	return strings.Count(string(data), " closed\n")
}

// A stdio server that exits restarts with backoff and keeps its tools, so
// the tools offered (and the prompt cache) do not change; a call made
// while it restarts waits for it and runs.
func TestCrashingServerRestarts(t *testing.T) {
	t.Parallel()
	var buf safeBuffer
	cfg := stdio(t)
	log := filepath.Join(t.TempDir(), "log")
	cfg.Env["MCPSERVER_LOG"] = log
	m := fastRestarts(t, map[string]mcp.ServerConfig{"s": cfg}, &buf)
	before, err := m.Tools(context.Background())
	require.NoError(t, err)

	_, err = m.Call(context.Background(), "s", "crash", nil)
	require.Error(t, err, "the call in flight fails; it is not repeated")
	r, err := m.Call(context.Background(), "s", "echo", json.RawMessage(`{"text":"again"}`))
	require.NoError(t, err, "the next call waits for the restart")
	assert.Contains(t, r.Text, "echo: again")
	st := m.Status()[0]
	assert.Equal(t, mcp.StateReady, st.State)
	assert.Equal(t, 1, st.Restarts)
	after, err := m.Tools(context.Background())
	require.NoError(t, err)
	assert.Equal(t, before, after, "the same tools, so the prompt cache holds")
	assert.Contains(t, buf.String(), `msg="MCP server restarted" server=s restart=1`)
	require.NoError(t, m.Close(), "the crash is no news on close")
	require.Eventually(t, func() bool { return connections(t, log) == 1 }, 5*time.Second, 10*time.Millisecond, "the first process exited")
}

// A server that cannot start again is tried MaxRestarts times, then fails
// with the last reason; calls fail with it, and its tools stay offered.
func TestRestartGivesUp(t *testing.T) {
	t.Parallel()
	var buf safeBuffer
	cfg := stdio(t)
	fail := filepath.Join(t.TempDir(), "fail")
	cfg.Env["MCPSERVER_FAIL_FILE"] = fail
	m := fastRestarts(t, map[string]mcp.ServerConfig{"s": cfg}, &buf)
	tools, err := m.Tools(context.Background())
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(fail, nil, 0o600))

	_, err = m.Call(context.Background(), "s", "crash", nil)
	require.Error(t, err)
	_, err = m.Call(context.Background(), "s", "echo", nil)
	require.ErrorContains(t, err, "the MCP server s failed: the server stopped and did not restart after 5 attempts")
	st := m.Status()[0]
	assert.Equal(t, mcp.StateFailed, st.State)
	assert.Equal(t, mcp.MaxRestarts, st.Restarts)
	again, err := m.Tools(context.Background())
	require.NoError(t, err)
	assert.Equal(t, names(tools), names(again), "the tools stay, so the prompt cache holds")
}

// A server that keeps stopping is restarted MaxRestarts times in a row,
// then left stopped.
func TestRestartLimit(t *testing.T) {
	t.Parallel()
	var buf safeBuffer
	m := fastRestarts(t, map[string]mcp.ServerConfig{"s": stdio(t)}, &buf)
	_, err := m.Tools(context.Background())
	require.NoError(t, err)
	for range mcp.MaxRestarts {
		_, err = m.Call(context.Background(), "s", "crash", nil)
		require.Error(t, err)
		_, err = m.Call(context.Background(), "s", "echo", nil)
		require.NoError(t, err, "restarted")
	}
	_, err = m.Call(context.Background(), "s", "crash", nil)
	require.Error(t, err)
	require.Eventually(t, func() bool { return m.Status()[0].State == mcp.StateFailed }, 5*time.Second, 10*time.Millisecond)
	_, err = m.Call(context.Background(), "s", "echo", nil)
	require.ErrorContains(t, err, "it stopped after 5 restarts in a row, so it stays stopped until /new")
}

// A call that waits for a restart longer than its tool timeout fails and
// says why; it was never sent.
func TestCallWaitingForARestartTimesOut(t *testing.T) {
	t.Parallel()
	var buf safeBuffer
	cfg := stdio(t)
	cfg.ToolTimeoutSec = ptr(0.2)
	m, err := mcp.NewManager(map[string]mcp.ServerConfig{"s": cfg}, mcp.Options{
		Workspace: t.TempDir(), Getenv: func(string) string { return "" }, RestartDelay: 5 * time.Second,
		Logger: slog.New(slog.NewTextHandler(&buf, nil)),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = m.Close() })
	_, err = m.Tools(context.Background())
	require.NoError(t, err)
	_, err = m.Call(context.Background(), "s", "crash", nil)
	require.Error(t, err)
	require.Eventually(t, func() bool { return m.Status()[0].State == mcp.StateRestarting }, 5*time.Second, 10*time.Millisecond)
	_, err = m.Call(context.Background(), "s", "echo", nil)
	require.ErrorContains(t, err, "the MCP server s is restarting after the server stopped")
	assert.ErrorContains(t, err, "the call was not sent")
	start := time.Now()
	require.NoError(t, m.Close(), "closing does not wait for the restart")
	assert.Less(t, time.Since(start), 3*time.Second)
}

// notifications/tools/list_changed lists the tools again; the new list is
// what the next run gets, and a list a run already has never changes.
func TestToolListChangeAppliesNextRun(t *testing.T) {
	t.Parallel()
	var buf safeBuffer
	m := fastRestarts(t, map[string]mcp.ServerConfig{"s": stdio(t)}, &buf)
	run1, err := m.Tools(context.Background())
	require.NoError(t, err)
	_, err = m.Call(context.Background(), "s", "add_tool", json.RawMessage(`{"text":"late"}`))
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		run2, err := m.Tools(context.Background())
		return err == nil && slices.Contains(names(run2), "mcp__s__late")
	}, 5*time.Second, 10*time.Millisecond)
	assert.NotContains(t, names(run1), "mcp__s__late", "the first run's list stays")
	assert.Contains(t, buf.String(), "the new list applies from the next run")
	r, err := m.Call(context.Background(), "s", "late", nil)
	require.NoError(t, err)
	assert.Equal(t, "new", r.Text)
}

// Resources are listed and read as Codex's resource tools return them, per
// server or for every server; listing every server leaves out one that
// does not offer resources.
func TestResources(t *testing.T) {
	t.Parallel()
	url, _ := countingServer(t)
	m := newManager(t, map[string]mcp.ServerConfig{"s": stdio(t), "plain": {URL: url}})
	_, err := m.Tools(context.Background())
	require.NoError(t, err)
	ctx := context.Background()

	list, err := m.ListResources(ctx, "", "")
	require.NoError(t, err)
	assert.JSONEq(t, `{"resources":[
		{"server":"s","uri":"test://greeting","name":"greeting","title":"Greeting","description":"A friendly greeting.","mimeType":"text/plain"},
		{"server":"s","uri":"test://pixel","name":"pixel","mimeType":"image/png"}]}`, mustJSON(t, list))
	one, err := m.ListResources(ctx, "s", "")
	require.NoError(t, err)
	assert.Equal(t, "s", one.Server)
	assert.Len(t, one.Resources, 2)
	_, err = m.ListResources(ctx, "", "abc")
	require.ErrorIs(t, err, mcp.ErrCursorWithoutServer)
	_, err = m.ListResources(ctx, "nope", "")
	require.ErrorContains(t, err, "resources/list failed: the MCP server nope is not running")

	templates, err := m.ListResourceTemplates(ctx, "", "")
	require.NoError(t, err)
	assert.JSONEq(t, `{"resourceTemplates":[{"server":"s","uriTemplate":"test://items/{id}","name":"item","description":"An item by ID."}]}`, mustJSON(t, templates))

	read, _, err := m.ReadResource(ctx, "s", "test://items/7")
	require.NoError(t, err)
	assert.JSONEq(t, `{"server":"s","uri":"test://items/7","contents":[{"uri":"test://items/7","mimeType":"text/plain","text":"item 7"}]}`, mustJSON(t, read))
	read, _, err = m.ReadResource(ctx, "s", "test://pixel")
	require.NoError(t, err)
	assert.Contains(t, mustJSON(t, read), `"blob":"iVBORw0KGgo`)
	_, _, err = m.ReadResource(ctx, "nope", "test://x")
	require.ErrorContains(t, err, "the MCP server nope is not running")

	refs := m.Resources(ctx)
	require.Len(t, refs, 2)
	assert.Equal(t, mcp.ResourceRef{Server: "s", URI: "test://greeting", Name: "Greeting", Description: "A friendly greeting."}, refs[0])

	_, err = m.Call(ctx, "s", "add_resource", json.RawMessage(`{"text":"fresh"}`))
	require.NoError(t, err)
	assert.Len(t, m.Resources(ctx), 3, "resources are listed live")
}

// Prompts are named as tools are and filled from positional arguments,
// the last taking the rest; a missing required one shows the usage. A
// changed prompt list is listed again.
func TestPrompts(t *testing.T) {
	t.Parallel()
	m := newManager(t, map[string]mcp.ServerConfig{"my-srv": stdio(t)})
	_, err := m.Tools(context.Background())
	require.NoError(t, err)
	ctx := context.Background()
	prompts := m.Prompts()
	require.Len(t, prompts, 1)
	p := prompts[0]
	assert.Equal(t, "mcp__my_srv__review", p.Command)
	assert.Equal(t, "<file> [focus]", p.Usage())
	assert.Equal(t, "Review a file.", p.Description)
	assert.Equal(t, prompts, m.Status()[0].Prompts)

	r, err := m.GetPrompt(ctx, p, []string{"main.go", "error", "handling"})
	require.NoError(t, err)
	require.Len(t, r.Messages, 3)
	assert.Equal(t, "Review main.go for error handling.", mustText(t, r.Messages[0]))
	_, err = m.GetPrompt(ctx, p, nil)
	require.ErrorContains(t, err, "/mcp__my_srv__review needs file: /mcp__my_srv__review <file> [focus]")

	_, err = m.Call(ctx, "my-srv", "add_prompt", json.RawMessage(`{"text":"later"}`))
	require.NoError(t, err)
	require.Eventually(t, func() bool { return len(m.Prompts()) == 2 }, 5*time.Second, 10*time.Millisecond)
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)

	return string(b)
}

func mustText(t *testing.T, msg *sdk.PromptMessage) string {
	t.Helper()
	c, ok := msg.Content.(*sdk.TextContent)
	require.True(t, ok, "%T", msg.Content)

	return c.Text
}
