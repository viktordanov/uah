package mcp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"
)

// AuthStatus is how uah authorizes to a server: Codex's McpAuthStatus.
type AuthStatus string

const (
	// AuthUnsupported: a stdio server, or an HTTP server that does not
	// advertise OAuth.
	AuthUnsupported AuthStatus = "unsupported"
	// AuthNotLoggedIn: the server wants OAuth and no usable login is stored.
	AuthNotLoggedIn AuthStatus = "not_logged_in"
	// AuthBearerToken: bearer_token_env_var or an Authorization header.
	AuthBearerToken AuthStatus = "bearer_token"
	// AuthOAuth: a stored OAuth login.
	AuthOAuth AuthStatus = "oauth"
)

// Text is Codex's display string.
func (a AuthStatus) Text() string {
	switch a {
	case AuthUnsupported:
		return "Unsupported"
	case AuthNotLoggedIn:
		return "Not logged in"
	case AuthBearerToken:
		return "Bearer token"
	case AuthOAuth:
		return "OAuth"
	}

	return "Unknown"
}

// ErrNeedsLogin marks a server that asked for OAuth without a usable login.
var ErrNeedsLogin = errors.New("not logged in")

// discoveryTimeout bounds OAuth discovery, as Codex's 5 s.
const discoveryTimeout = 5 * time.Second

// usesBearer reports whether the configuration authorizes with a token of
// its own, which OAuth never replaces.
func (c ServerConfig) usesBearer() bool {
	if c.BearerTokenEnvVar != "" {
		return true
	}
	for k := range c.HTTPHeaders {
		if strings.EqualFold(k, "Authorization") {
			return true
		}
	}
	for k := range c.EnvHTTPHeaders {
		if strings.EqualFold(k, "Authorization") {
			return true
		}
	}

	return false
}

// AuthStatusOf is the server's auth status without starting it, as `uah mcp
// list` shows it: the configuration, then a stored login, then OAuth
// discovery (bounded by 5 s).
func AuthStatusOf(ctx context.Context, name string, c ServerConfig, store CredentialStore, client *http.Client) AuthStatus {
	switch {
	case c.URL == "":
		return AuthUnsupported
	case c.usesBearer():
		return AuthBearerToken
	}
	if store != nil {
		if creds, err := store.Load(name, c.URL); err == nil && usable(creds) {
			return AuthOAuth
		}
	}
	if DiscoverOAuth(ctx, c.URL, client) {
		return AuthNotLoggedIn
	}

	return AuthUnsupported
}

// usable reports whether stored credentials can authorize: a token that
// has not expired, or a refresh token.
func usable(c Credentials) bool {
	return c.RefreshToken != "" || c.Token().Valid()
}

// DiscoverOAuth reports whether the server advertises OAuth: protected
// resource metadata (RFC 9728) at its well-known places, or authorization
// server metadata at its origin (the 2025-03-26 fallback). It gives up
// after 5 s.
func DiscoverOAuth(ctx context.Context, serverURL string, client *http.Client) bool {
	ctx, cancel := context.WithTimeout(ctx, discoveryTimeout)
	defer cancel()
	u, err := url.Parse(serverURL)
	if err != nil {
		return false
	}
	origin := &url.URL{Scheme: u.Scheme, Host: u.Host}
	pathMeta := *origin
	pathMeta.Path = "/.well-known/oauth-protected-resource/" + strings.TrimLeft(u.Path, "/")
	rootMeta := *origin
	rootMeta.Path = "/.well-known/oauth-protected-resource"
	for _, try := range []struct{ meta, resource string }{{pathMeta.String(), serverURL}, {rootMeta.String(), origin.String()}} {
		prm, err := oauthex.GetProtectedResourceMetadata(ctx, try.meta, try.resource, client)
		if err == nil && prm != nil && len(prm.AuthorizationServers) > 0 {
			return true
		}
	}
	asm, err := auth.GetAuthServerMetadata(ctx, origin.String(), client)

	return err == nil && asm != nil
}

// storedAuth is the SDK's OAuthHandler for a running server: it sends the
// stored token, refreshing it and saving the refreshed one, and turns a 401
// into ErrNeedsLogin. It never opens a browser: `uah mcp login` does that.
type storedAuth struct {
	name, url string
	store     CredentialStore
	client    *http.Client
	logger    *slog.Logger

	mu       sync.Mutex
	loaded   bool
	hasLogin bool
	// tokens are the stored tokens the handler loaded, "" without a login;
	// a login elsewhere changes what the store holds.
	tokens string
	source oauth2.TokenSource
}

var _ auth.OAuthHandler = (*storedAuth)(nil)

func newStoredAuth(name, url string, opts Options) *storedAuth {
	return &storedAuth{name: name, url: url, store: opts.Credentials, client: opts.HTTPClient, logger: opts.Logger}
}

func (a *storedAuth) TokenSource(context.Context) (oauth2.TokenSource, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.loaded {
		a.loaded = true
		if creds, err := a.store.Load(a.name, a.url); err == nil && usable(creds) {
			a.hasLogin, a.tokens = true, creds.AccessToken+"\x00"+creds.RefreshToken
			a.source = savingSource(a.client, creds, a.store, a.logger) //nolint:contextcheck // the source outlives any request
		}
	}

	return a.source, nil // nil without a login: the SDK then sends no token
}

// loginChanged reports whether the store now holds a usable login other
// than the one the handler loaded: `uah mcp login` ran since, as Codex's
// oauth_credentials_changed checks a server that failed to authorize.
func (a *storedAuth) loginChanged() bool {
	creds, err := a.store.Load(a.name, a.url)
	if err != nil || !usable(creds) {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()

	return creds.AccessToken+"\x00"+creds.RefreshToken != a.tokens
}

// Authorize is called on a 401, and on a 403 that asks for more scopes.
func (a *storedAuth) Authorize(ctx context.Context, _ *http.Request, resp *http.Response) error {
	_ = resp.Body.Close()
	challenges, _ := oauthex.ParseWWWAuthenticate(resp.Header.Values("WWW-Authenticate"))
	if resp.StatusCode == http.StatusForbidden && challengeParam(challenges, "error") != "insufficient_scope" {
		return nil // retried once, then fails as the server answered
	}
	a.mu.Lock()
	relogin := a.hasLogin
	a.mu.Unlock()
	if challengeParam(challenges, "") == "" && !DiscoverOAuth(ctx, a.url, a.client) {
		return fmt.Errorf("the server answered %s and does not advertise OAuth", resp.Status)
	}

	return &LoginError{Server: a.name, Relogin: relogin}
}

// LoginError is a server that needs `uah mcp login`, with Codex's message.
// It matches ErrNeedsLogin.
type LoginError struct {
	Server string
	// Relogin is set when a stored login was rejected.
	Relogin bool
}

func (e *LoginError) Error() string {
	if e.Relogin {
		return fmt.Sprintf("The %s MCP server requires OAuth reauthentication. Run `uah mcp login %s`.", e.Server, e.Server)
	}

	return fmt.Sprintf("The %s MCP server is not logged in. Run `uah mcp login %s`.", e.Server, e.Server)
}

func (*LoginError) Is(target error) bool { return target == ErrNeedsLogin }

// challengeParam returns a Bearer challenge's parameter; with key "", it
// returns "bearer" when there is a Bearer challenge at all.
func challengeParam(cs []oauthex.Challenge, key string) string {
	for _, c := range cs {
		if c.Scheme != "bearer" {
			continue
		}
		if key == "" {
			return c.Scheme
		}
		if v := c.Params[key]; v != "" {
			return v
		}
	}

	return ""
}

// savingSource refreshes the stored token through the oauth2 package and
// saves each new token back to the store, as Codex persists refreshed
// tokens.
func savingSource(client *http.Client, creds Credentials, store CredentialStore, logger *slog.Logger) oauth2.TokenSource {
	cfg := &oauth2.Config{
		ClientID: creds.ClientID, ClientSecret: creds.ClientSecret, Scopes: creds.Scopes,
		Endpoint: oauth2.Endpoint{TokenURL: creds.TokenURL, AuthStyle: oauth2.AuthStyle(creds.AuthStyle)},
	}
	// The source outlives any request, so it refreshes on a context of its own.
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, client)

	return &saving{base: cfg.TokenSource(ctx, creds.Token()), creds: creds, store: store, logger: logger, last: creds.AccessToken}
}

type saving struct {
	base   oauth2.TokenSource
	store  CredentialStore
	logger *slog.Logger

	mu    sync.Mutex
	creds Credentials
	last  string
}

func (s *saving) Token() (*oauth2.Token, error) {
	t, err := s.base.Token()
	if err != nil {
		return nil, err // the SDK checks for *oauth2.RetrieveError
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if t.AccessToken != s.last {
		s.last = t.AccessToken
		s.creds = s.creds.withToken(t)
		if err := s.store.Save(s.creds); err != nil { // the new token still works for this process
			s.logger.LogAttrs(context.Background(), slog.LevelWarn, "failed to save a refreshed MCP OAuth token",
				slog.String("server", s.creds.ServerName),
				slog.Any("err", err))
		}
	}

	return t, nil
}
