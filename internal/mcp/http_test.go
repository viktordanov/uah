package mcp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/mcp"
)

func TestStreamableHTTP(t *testing.T) {
	t.Parallel()
	server := sdk.NewServer(&sdk.Implementation{Name: "http", Version: "1"}, nil)
	server.AddTool(&sdk.Tool{Name: "whoami", InputSchema: map[string]any{"type": "object"}},
		func(_ context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: req.Extra.Header.Get("X-Team")}}}, nil
		})
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)

			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	env := map[string]string{"TOKEN": "secret", "TEAM": "core"}
	m, err := mcp.NewManager(map[string]mcp.ServerConfig{
		"remote": {URL: srv.URL, BearerTokenEnvVar: "TOKEN", EnvHTTPHeaders: map[string]string{"X-Team": "TEAM"}},
	}, mcp.Options{Getenv: func(k string) string { return env[k] }})
	require.NoError(t, err)
	t.Cleanup(func() { _ = m.Close() })
	tools, err := m.Tools(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"mcp__remote__whoami"}, names(tools))
	r, err := m.Call(context.Background(), "remote", "whoami", json.RawMessage(`{}`))
	require.NoError(t, err)
	assert.Equal(t, "core", r.Text)
}

// TestStreamableHTTPCrossOriginRedirect: a server that redirects to
// another origin gets the request refused there, so neither the bearer
// token, the configured headers, the stored OAuth token, nor the session
// ID leave the configured origin.
func TestStreamableHTTPCrossOriginRedirect(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var stolen []http.Header
	other := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		mu.Lock()
		stolen = append(stolen, r.Header.Clone())
		mu.Unlock()
	}))
	t.Cleanup(other.Close)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/mcp", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(srv.Close)

	store := &mcp.FileStore{Path: filepath.Join(t.TempDir(), "mcp-credentials.json")}
	require.NoError(t, store.Save(mcp.Credentials{
		ServerName: "oauth", ServerURL: srv.URL + "/mcp", ClientID: "id", TokenURL: srv.URL + "/token",
		AccessToken: "oauth-token", ExpiresAt: time.Now().Add(time.Hour).UnixMilli(),
	}))
	env := map[string]string{"TOKEN": "secret"}
	m, err := mcp.NewManager(map[string]mcp.ServerConfig{
		"bearer": {URL: srv.URL + "/mcp", BearerTokenEnvVar: "TOKEN", HTTPHeaders: map[string]string{"X-Api-Key": "key"}},
		"oauth":  {URL: srv.URL + "/mcp"},
	}, mcp.Options{Credentials: store, Getenv: func(k string) string { return env[k] }})
	require.NoError(t, err)
	t.Cleanup(func() { _ = m.Close() })
	_, _ = m.Tools(context.Background())
	for _, st := range m.Status() {
		assert.Equal(t, mcp.StateFailed, st.State, st.Name)
		assert.Contains(t, st.Error, "refusing a redirect from http://127.0.0.1:", st.Name)
	}
	mu.Lock()
	defer mu.Unlock()
	assert.Empty(t, stolen, "nothing reaches the other origin")
}
