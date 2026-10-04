// Package oauthserver is an MCP server behind OAuth 2.1 for tests, in one
// httptest server: the protected resource (/mcp, a streamable HTTP MCP
// server built with the official SDK, with a whoami tool), its protected
// resource metadata, and a small authorization server with dynamic client
// registration, PKCE (S256), and refresh tokens. Browser follows the
// authorization URL the way a user's browser would.
package oauthserver

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

// Server is the resource and authorization server.
type Server struct {
	srv *httptest.Server

	mu       sync.Mutex
	ttl      time.Duration
	clients  map[string][]string // client ID to redirect URIs
	codes    map[string]grant
	access   map[string]time.Time // access token to expiry
	refresh  map[string]bool
	stats    Stats
	sequence int
}

// Stats count what clients did.
type Stats struct {
	Registrations int
	Refreshes     int
	// Resource and Scope are the last authorization request's.
	Resource string
	Scope    string
}

type grant struct {
	challenge, clientID, redirect string
}

// New starts the server; access tokens live for an hour.
func New(tb testing.TB) *Server {
	tb.Helper()
	s := &Server{ttl: time.Hour, clients: map[string][]string{}, codes: map[string]grant{}, access: map[string]time.Time{}, refresh: map[string]bool{}}
	mux := http.NewServeMux()
	s.srv = httptest.NewServer(mux)
	tb.Cleanup(s.srv.Close)
	mcp := sdk.NewServer(&sdk.Implementation{Name: "oauth-mcp", Version: "1"}, nil)
	mcp.AddTool(&sdk.Tool{Name: "whoami", Description: "Say who is calling.", InputSchema: map[string]any{"type": "object"}},
		func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "authorized"}}}, nil
		})
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return mcp }, nil)
	mux.Handle("/mcp", auth.RequireBearerToken(s.verify, &auth.RequireBearerTokenOptions{
		ResourceMetadataURL: s.URL() + "/.well-known/oauth-protected-resource/mcp",
	})(handler))
	mux.Handle("/.well-known/oauth-protected-resource/mcp", auth.ProtectedResourceMetadataHandler(&oauthex.ProtectedResourceMetadata{
		Resource: s.MCPURL(), AuthorizationServers: []string{s.URL()}, ScopesSupported: []string{"mcp:read", "mcp:write"},
	}))
	mux.HandleFunc("/.well-known/oauth-authorization-server", s.metadata)
	mux.HandleFunc("/register", s.register)
	mux.HandleFunc("/authorize", s.authorize)
	mux.HandleFunc("/token", s.token)

	return s
}

// URL is the issuer; MCPURL is the MCP endpoint.
func (s *Server) URL() string    { return s.srv.URL }
func (s *Server) MCPURL() string { return s.srv.URL + "/mcp" }

// SetTTL sets how long new access tokens live.
func (s *Server) SetTTL(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ttl = d
}

// Revoke forgets every access and refresh token.
func (s *Server) Revoke() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.access, s.refresh = map[string]time.Time{}, map[string]bool{}
}

// DropConnections closes every client connection, as a proxy that restarts
// does, so a client's standalone SSE stream has to reconnect.
func (s *Server) DropConnections() { s.srv.CloseClientConnections() }

// Stats returns the counts so far.
func (s *Server) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.stats
}

// Browser opens an authorization URL as a browser would: it lets the user
// "approve" and follows the redirect to the client's callback.
func Browser(authURL string) error {
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := noFollow.Get(authURL) //nolint:noctx // a test browser
	if err != nil {
		return fmt.Errorf("failed to open %s: %w", authURL, err)
	}
	_ = resp.Body.Close()
	callback, err := resp.Location()
	if err != nil {
		return fmt.Errorf("no redirect (%s): %w", resp.Status, err)
	}
	resp, err = http.Get(callback.String()) //nolint:noctx // the client's own loopback callback
	if err != nil {
		return fmt.Errorf("failed to call back: %w", err)
	}

	return resp.Body.Close()
}

func (s *Server) verify(_ context.Context, token string, _ *http.Request) (*auth.TokenInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	expiry, ok := s.access[token]
	if !ok || time.Now().After(expiry) {
		return nil, auth.ErrInvalidToken
	}

	return &auth.TokenInfo{Expiration: expiry}, nil
}

func (s *Server) metadata(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, &oauthex.AuthServerMeta{
		Issuer: s.URL(), AuthorizationEndpoint: s.URL() + "/authorize", TokenEndpoint: s.URL() + "/token",
		RegistrationEndpoint: s.URL() + "/register", ScopesSupported: []string{"mcp:read", "mcp:write", "offline_access"},
		ResponseTypesSupported: []string{"code"}, CodeChallengeMethodsSupported: []string{"S256"},
		TokenEndpointAuthMethodsSupported: []string{"none"},
	})
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	var meta oauthex.ClientRegistrationMetadata
	if err := json.NewDecoder(r.Body).Decode(&meta); err != nil || len(meta.RedirectURIs) == 0 {
		http.Error(w, "bad registration", http.StatusBadRequest)

		return
	}
	s.mu.Lock()
	id := s.nextLocked("client")
	s.clients[id] = meta.RedirectURIs
	s.stats.Registrations++
	s.mu.Unlock()
	meta.TokenEndpointAuthMethod = "none"
	writeJSON(w, http.StatusCreated, &oauthex.ClientRegistrationResponse{ClientID: id, ClientRegistrationMetadata: meta})
}

func (s *Server) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	s.mu.Lock()
	defer s.mu.Unlock()
	redirects, ok := s.clients[q.Get("client_id")]
	switch {
	case !ok:
		http.Error(w, "unknown client", http.StatusBadRequest)

		return
	case !slices.Contains(redirects, q.Get("redirect_uri")):
		http.Error(w, "unknown redirect_uri", http.StatusBadRequest)

		return
	case q.Get("code_challenge") == "" || q.Get("code_challenge_method") != "S256":
		http.Error(w, "PKCE required", http.StatusBadRequest)

		return
	}
	code := s.nextLocked("code")
	s.codes[code] = grant{challenge: q.Get("code_challenge"), clientID: q.Get("client_id"), redirect: q.Get("redirect_uri")}
	s.stats.Resource, s.stats.Scope = q.Get("resource"), q.Get("scope")
	http.Redirect(w, r, q.Get("redirect_uri")+"?code="+code+"&state="+q.Get("state"), http.StatusFound)
}

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)

		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var err error
	switch r.Form.Get("grant_type") {
	case "authorization_code":
		err = s.exchangeLocked(r)
	case "refresh_token":
		if !s.refresh[r.Form.Get("refresh_token")] {
			err = errors.New("invalid_grant")
		} else {
			delete(s.refresh, r.Form.Get("refresh_token"))
			s.stats.Refreshes++
		}
	default:
		err = errors.New("unsupported_grant_type")
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})

		return
	}
	access, refresh := s.nextLocked("access"), s.nextLocked("refresh")
	s.access[access], s.refresh[refresh] = time.Now().Add(s.ttl), true
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": access, "token_type": "Bearer", "expires_in": int(s.ttl.Seconds()), "refresh_token": refresh,
	})
}

func (s *Server) exchangeLocked(r *http.Request) error {
	g, ok := s.codes[r.Form.Get("code")]
	delete(s.codes, r.Form.Get("code"))
	sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
	switch {
	case !ok || g.clientID != r.Form.Get("client_id") || g.redirect != r.Form.Get("redirect_uri"):
		return errors.New("invalid_grant")
	case base64.RawURLEncoding.EncodeToString(sum[:]) != g.challenge:
		return errors.New("invalid_grant: PKCE verification failed")
	}

	return nil
}

func (s *Server) nextLocked(kind string) string {
	s.sequence++

	return fmt.Sprintf("%s-%d-%s", kind, s.sequence, strings.ToLower(rand.Text()[:8]))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v) //nolint:errchkjson // test responses always encode
}
