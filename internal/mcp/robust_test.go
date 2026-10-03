package mcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/mcp"
)

func call(t *testing.T, m *mcp.Manager, server, tool, args string) (mcp.Result, error) {
	t.Helper()

	return m.Call(context.Background(), server, tool, json.RawMessage(args))
}

// A server that takes one call at a time runs them in turn, within a tool
// timeout that covers the wait for its turn.
func TestSerialCallsWaitTheirTurn(t *testing.T) {
	t.Parallel()
	serial := stdio(t)
	serial.ToolTimeoutSec = ptr(2.0)
	parallel := stdio(t)
	parallel.SupportsParallelToolCalls = true
	m := newManager(t, map[string]mcp.ServerConfig{"serial": serial, "parallel": parallel})
	_, err := m.Tools(context.Background())
	require.NoError(t, err)

	for server, want := range map[string]string{"serial": "1", "parallel": "2"} {
		var wg sync.WaitGroup
		results := make([]string, 2)
		for i := range 2 {
			wg.Go(func() {
				r, err := call(t, m, server, "overlap", `{"ms":700}`)
				assert.NoError(t, err, server)
				results[i] = r.Text
			})
		}
		wg.Wait()
		assert.Contains(t, results, want, "%s: the most calls running at once", server)
	}

	// A call canceled while it waits for its turn never reaches the server.
	mark := filepath.Join(t.TempDir(), "running")
	go func() {
		_, _ = call(t, m, "serial", "sleep", `{"ms":600,"mark":"`+mark+`"}`)
	}()
	require.Eventually(t, func() bool { _, err := os.Stat(mark); return err == nil }, 5*time.Second, 5*time.Millisecond, "the first call holds the server")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err = m.Call(ctx, "serial", "echo", nil)
	require.ErrorContains(t, err, "canceled while waiting for the server")
}

// Output is not cut here: the runner bounds a remote job's result (the
// embedded engine's test checks that), and a large result does not break
// the connection.
func TestLargeResult(t *testing.T) {
	t.Parallel()
	m := newManager(t, map[string]mcp.ServerConfig{"s": stdio(t)})
	_, err := m.Tools(context.Background())
	require.NoError(t, err)
	r, err := call(t, m, "s", "big", `{"n":4000000}`)
	require.NoError(t, err)
	assert.Len(t, r.Text, 4_000_000)
	r, err = call(t, m, "s", "echo", `{"text":"still"}`)
	require.NoError(t, err)
	assert.Contains(t, r.Text, "still")
}

// Standard error goes to the log, a line per record, with the server's name.
func TestStderrIsLogged(t *testing.T) {
	t.Parallel()
	var buf safeBuffer
	m, err := mcp.NewManager(map[string]mcp.ServerConfig{"loud": stdio(t)}, mcp.Options{
		Workspace: t.TempDir(), Getenv: func(string) string { return "" }, Logger: slog.New(slog.NewTextHandler(&buf, nil)),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = m.Close() })
	_, err = m.Tools(context.Background())
	require.NoError(t, err)
	_, err = call(t, m, "loud", "stderr", `{"text":"first line\nsecond line"}`)
	require.NoError(t, err)
	require.Eventually(t, func() bool { return strings.Contains(buf.String(), "second line") }, 5*time.Second, 10*time.Millisecond)
	assert.Contains(t, buf.String(), `msg="MCP server stderr" server=loud line="first line"`)
}

// A stdio server gets Codex's basic variables, then env_vars, then env,
// and nothing else from uah's environment.
func TestServerEnvironment(t *testing.T) {
	t.Parallel()
	parent := map[string]string{"HOME": "/home/u", "SECRET": "s3", "PASSED": "yes", "PATH": "/usr/bin:/bin"}
	cfg := stdio(t)
	cfg.EnvVars = []string{"PASSED"}
	m, err := mcp.NewManager(map[string]mcp.ServerConfig{"s": cfg}, mcp.Options{Workspace: t.TempDir(), Getenv: func(k string) string { return parent[k] }})
	require.NoError(t, err)
	t.Cleanup(func() { _ = m.Close() })
	_, err = m.Tools(context.Background())
	require.NoError(t, err)
	for name, want := range map[string]string{"HOME": "/home/u", "SECRET": "unset", "PASSED": "yes", "MCPSERVER_GREETING": "hi"} {
		r, err := call(t, m, "s", "env", `{"text":"`+name+`"}`)
		require.NoError(t, err)
		assert.Equal(t, want, r.Text, name)
	}
}

// Every page of a long tool list is read.
func TestManyTools(t *testing.T) {
	t.Parallel()
	cfg := stdio(t)
	cfg.Env["MCPSERVER_EXTRA_TOOLS"] = "1500"
	m := newManager(t, map[string]mcp.ServerConfig{"s": cfg})
	tools, err := m.Tools(context.Background())
	require.NoError(t, err)
	assert.Len(t, tools, 1500+13)
	r, err := call(t, m, "s", "tool_1499", `{}`)
	require.NoError(t, err)
	assert.Equal(t, "extra", r.Text)
}

// Servers whose names sanitize alike get distinct tool names.
func TestNameCollisions(t *testing.T) {
	t.Parallel()
	a, b := stdio(t), stdio(t)
	a.EnabledTools, b.EnabledTools = []string{"echo"}, []string{"echo"}
	m := newManager(t, map[string]mcp.ServerConfig{"my-srv": a, "my.srv": b})
	tools, err := m.Tools(context.Background())
	require.NoError(t, err)
	require.Len(t, tools, 2)
	assert.Equal(t, "mcp__my_srv__echo", tools[0].Name)
	assert.Regexp(t, `^mcp__my_srv__echo_[0-9a-f]{12}$`, tools[1].Name)
	r, err := call(t, m, "my.srv", "echo", `{"text":"b"}`)
	require.NoError(t, err)
	assert.Contains(t, r.Text, "echo: b")
}

// swapHandler serves an MCP server that can be replaced, so a test can make
// the server forget its sessions, as a restarted server does.
type swapHandler struct{ h atomic.Pointer[http.Handler] }

func (s *swapHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	(*s.h.Load()).ServeHTTP(w, r)
}

func (s *swapHandler) fresh() {
	server := sdk.NewServer(&sdk.Implementation{Name: "http", Version: "1"}, nil)
	server.AddTool(&sdk.Tool{Name: "ping", InputSchema: map[string]any{"type": "object"}},
		func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "pong"}}}, nil
		})
	var h http.Handler = sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil)
	s.h.Store(&h)
}

// An HTTP server that forgot the session (404) gets a new one and the call
// runs, as Codex re-initializes; a server that is gone fails the call
// within the timeout; one that answers 500 at startup fails to start.
func TestHTTPFailures(t *testing.T) {
	t.Parallel()
	sw := &swapHandler{}
	sw.fresh()
	srv := httptest.NewServer(sw)
	t.Cleanup(srv.Close)
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "boom", http.StatusInternalServerError) }))
	t.Cleanup(broken.Close)
	m := newManager(t, map[string]mcp.ServerConfig{
		"remote": {URL: srv.URL, ToolTimeoutSec: ptr(2.0)},
		"broken": {URL: broken.URL},
	})
	_, err := m.Tools(context.Background())
	require.NoError(t, err)
	status := m.Status()
	assert.Equal(t, mcp.StateFailed, status[0].State, "broken")
	assert.Contains(t, status[0].Error, "Internal Server Error")

	r, err := call(t, m, "remote", "ping", `{}`)
	require.NoError(t, err)
	assert.Equal(t, "pong", r.Text)
	sw.fresh()
	r, err = call(t, m, "remote", "ping", `{}`)
	require.NoError(t, err, "a new session after the old one expired")
	assert.Equal(t, "pong", r.Text)

	srv.CloseClientConnections()
	srv.Close()
	start := time.Now()
	_, err = call(t, m, "remote", "ping", `{}`)
	require.Error(t, err)
	assert.Less(t, time.Since(start), 5*time.Second)
}

type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}
