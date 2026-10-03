package mcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/mcp"
	"github.com/viktordanov/uah/testing/oauthserver"
)

// countingServer is an HTTP MCP server that counts the connections opened
// to it: initialize requests.
func countingServer(t *testing.T) (string, *atomic.Int64) {
	t.Helper()
	server := sdk.NewServer(&sdk.Implementation{Name: "http", Version: "1"}, nil)
	server.AddTool(&sdk.Tool{Name: "ping", InputSchema: map[string]any{"type": "object"}},
		func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "pong"}}}, nil
		})
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil)
	var opened atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if bytes.Contains(body, []byte(`"method":"initialize"`)) {
			opened.Add(1)
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	return srv.URL, &opened
}

// After Close nothing starts a server again: a session's delayed startup
// report, a run's Tools, /mcp's Status, and a call all find the manager
// closed.
func TestCloseIsTerminal(t *testing.T) {
	t.Parallel()
	url, opened := countingServer(t)
	m := newManager(t, map[string]mcp.ServerConfig{"remote": {URL: url}})
	_, err := m.Tools(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(1), opened.Load())
	require.NoError(t, m.Close())

	assert.Nil(t, m.Started(context.Background()), "a late startup report finds the manager closed")
	_, err = m.Tools(context.Background())
	require.ErrorIs(t, err, mcp.ErrClosed)
	assert.Empty(t, m.Status())
	_, err = m.Call(context.Background(), "remote", "ping", nil)
	require.ErrorContains(t, err, "not running")
	m.Start()
	require.NoError(t, m.Close(), "closing twice is fine")
	assert.Equal(t, int64(1), opened.Load(), "no server started after Close")

	never := newManager(t, map[string]mcp.ServerConfig{"remote": {URL: url}})
	require.NoError(t, never.Close())
	assert.Nil(t, never.Started(context.Background()), "a manager closed before it started never starts")
	assert.Equal(t, int64(1), opened.Load())
}

// A startup report racing with Close either reports the servers or nothing,
// and once both return no connection stays open.
func TestStartedRacesClose(t *testing.T) {
	t.Parallel()
	url, _ := countingServer(t)
	for range 20 {
		m := newManager(t, map[string]mcp.ServerConfig{"remote": {URL: url}})
		var wg sync.WaitGroup
		wg.Go(func() {
			if got := m.Started(context.Background()); got != nil {
				assert.Len(t, got, 1)
			}
		})
		wg.Go(func() { _ = m.Status() })
		wg.Go(func() { _ = m.Close() })
		wg.Wait()
		require.NoError(t, m.Close())
		assert.Nil(t, m.Started(context.Background()))
		assert.Empty(t, m.Status())
	}
}

// A login revoked while the server runs makes it needs_login, as a 401 at
// startup does, and later calls say to log in again.
func TestRuntimeLoginExpiry(t *testing.T) {
	t.Parallel()
	srv := oauthserver.New(t)
	store := &mcp.FileStore{Path: filepath.Join(t.TempDir(), "mcp-credentials.json")}
	cfg := mcp.ServerConfig{URL: srv.MCPURL()}
	login(t, cfg, store)
	m := oauthManager(t, cfg, store)
	require.Equal(t, mcp.StateReady, m.Status()[0].State, m.Status()[0].Error)
	_, err := m.Call(context.Background(), "remote", "whoami", json.RawMessage(`{}`))
	require.NoError(t, err)

	srv.Revoke()
	_, err = m.Call(context.Background(), "remote", "whoami", json.RawMessage(`{}`))
	require.ErrorIs(t, err, mcp.ErrNeedsLogin)
	st := m.Status()[0]
	assert.Equal(t, mcp.StateNeedsLogin, st.State)
	assert.Equal(t, "The remote MCP server requires OAuth reauthentication. Run `uah mcp login remote`.", st.Error)
	assert.Equal(t, mcp.AuthNotLoggedIn, st.Auth)
	_, err = m.Call(context.Background(), "remote", "whoami", json.RawMessage(`{}`))
	require.ErrorContains(t, err, "Run `uah mcp login remote`")
}

// An auth mode uah does not support fails only that server, with the
// reason; the others start.
func TestUnsupportedAuthFailsOneServer(t *testing.T) {
	t.Parallel()
	url, opened := countingServer(t)
	m := newManager(t, map[string]mcp.ServerConfig{
		"ok":      {URL: url},
		"chatgpt": {URL: url, Auth: "chatgpt"},
		"off":     {URL: url, Auth: "chatgpt", Enabled: ptr(false)},
	})
	tools, err := m.Tools(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"mcp__ok__ping"}, names(tools))
	byName := map[string]mcp.ServerStatus{}
	for _, s := range m.Started(context.Background()) {
		byName[s.Name] = s
	}
	assert.Equal(t, mcp.StateReady, byName["ok"].State)
	assert.Equal(t, mcp.StateFailed, byName["chatgpt"].State)
	assert.Equal(t, `auth "chatgpt" is not supported (want oauth)`, byName["chatgpt"].Error)
	assert.Equal(t, mcp.StateDisabled, byName["off"].State)
	assert.Equal(t, int64(1), opened.Load(), "the unsupported server is never contacted")
	_, err = m.Call(context.Background(), "chatgpt", "ping", nil)
	require.ErrorContains(t, err, `auth "chatgpt" is not supported`)

	required := newManager(t, map[string]mcp.ServerConfig{"chatgpt": {URL: url, Auth: "chatgpt", Required: true}})
	_, err = required.Tools(context.Background())
	require.ErrorContains(t, err, `the required MCP server chatgpt failed to start: auth "chatgpt" is not supported`)

	_, err = mcp.NewManager(map[string]mcp.ServerConfig{"bad": {URL: url, Auth: "chatgpt", ToolTimeoutSec: ptr(0.0)}}, mcp.Options{})
	require.ErrorContains(t, err, "positive", "other configuration errors still fail the manager")
}

// A `uah mcp login` in another terminal reconnects a needs_login server at
// the next run (Tools) or /mcp (Status), without /new, as Codex checks the
// stored login of a server that failed to authorize before each step.
func TestLoginElsewhereReconnects(t *testing.T) {
	t.Parallel()
	srv := oauthserver.New(t)
	store := &mcp.FileStore{Path: filepath.Join(t.TempDir(), "mcp-credentials.json")}
	cfg := mcp.ServerConfig{URL: srv.MCPURL()}
	m := oauthManager(t, cfg, store)
	require.Equal(t, mcp.StateNeedsLogin, m.Status()[0].State)
	tools, err := m.Tools(context.Background())
	require.NoError(t, err)
	assert.Empty(t, tools, "nothing changed: no reconnect")

	login(t, cfg, store)
	tools, err = m.Tools(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"mcp__remote__whoami"}, names(tools), "the next run reconnects first")
	st := m.Status()[0]
	assert.Equal(t, mcp.StateReady, st.State, st.Error)
	assert.Equal(t, mcp.AuthOAuth, st.Auth)
	r, err := m.Call(context.Background(), "remote", "whoami", json.RawMessage(`{}`))
	require.NoError(t, err)
	assert.Equal(t, "authorized", r.Text)

	// A revoked login needs a new one; /mcp after it reconnects too.
	srv.Revoke()
	_, err = m.Call(context.Background(), "remote", "whoami", json.RawMessage(`{}`))
	require.ErrorIs(t, err, mcp.ErrNeedsLogin)
	login(t, cfg, store)
	require.Eventually(t, func() bool { return m.Status()[0].State == mcp.StateReady }, 10*time.Second, 20*time.Millisecond)
}

// A 401 on the SDK's background SSE stream (a login revoked while no call
// runs) makes the server needs_login, not failed: the reconnect it causes
// is refused with a 401.
func TestRevokedLoginOnTheSSEStream(t *testing.T) {
	t.Parallel()
	srv := oauthserver.New(t)
	store := &mcp.FileStore{Path: filepath.Join(t.TempDir(), "mcp-credentials.json")}
	cfg := mcp.ServerConfig{URL: srv.MCPURL()}
	login(t, cfg, store)
	m, err := mcp.NewManager(map[string]mcp.ServerConfig{"remote": cfg}, mcp.Options{
		Credentials: store, Getenv: func(string) string { return "" }, RestartDelay: 10 * time.Millisecond,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = m.Close() })
	_, err = m.Tools(context.Background())
	require.NoError(t, err)
	require.Equal(t, mcp.StateReady, m.Status()[0].State)

	srv.Revoke()
	srv.DropConnections()
	require.Eventually(t, func() bool { return m.Status()[0].State == mcp.StateNeedsLogin }, 20*time.Second, 20*time.Millisecond, "%+v", m.Status()[0])
	assert.Contains(t, m.Status()[0].Error, "requires OAuth reauthentication. Run `uah mcp login remote`.")
	assert.Equal(t, 1, m.Status()[0].Restarts, "the stream failed the connection, and the reconnect got the 401")
}
