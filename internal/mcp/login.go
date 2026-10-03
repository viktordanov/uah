package mcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"
)

// DefaultLoginTimeout is how long a login waits for the browser, as
// Codex's 300 s.
const DefaultLoginTimeout = 300 * time.Second

// OAuthSettings are the top-level OAuth keys: where credentials are kept
// and the callback a login listens on.
type OAuthSettings struct {
	// CallbackPort is mcp_oauth_callback_port (0: a port the OS picks).
	CallbackPort int
	// CallbackURL is mcp_oauth_callback_url: the redirect URI sent to the
	// server instead of http://127.0.0.1:<port>/callback.
	CallbackURL string
}

// LoginOptions configure Login.
type LoginOptions struct {
	Store    CredentialStore
	Settings OAuthSettings
	// Scopes replace the configured and discovered scopes.
	Scopes []string
	// OpenBrowser opens the authorization URL; nil or a failure leaves the
	// printed URL for the user to open.
	OpenBrowser func(url string) error
	// Out receives the URL and progress.
	Out io.Writer
	// Getenv reads env_http_headers (default os.Getenv).
	Getenv func(string) string
	// HTTPClient makes the discovery, registration, and token requests
	// (default http.DefaultClient), following redirects only within an
	// origin; its transport also carries the requests to the server.
	HTTPClient *http.Client
	// Timeout bounds the wait for the browser (default 300 s).
	Timeout time.Duration
}

// Login runs the OAuth authorization code flow with PKCE for an HTTP
// server, through the SDK's AuthorizationCodeHandler: it connects, and the
// server's 401 starts discovery, client registration (the configured
// client_id, else dynamic registration), the browser step with a loopback
// callback, and the token exchange. The tokens are saved to the store.
func Login(ctx context.Context, name string, c ServerConfig, opts LoginOptions) error {
	if c.URL == "" {
		return errors.New("OAuth login is only supported for streamable HTTP servers")
	}
	if c.usesBearer() {
		return fmt.Errorf("the %s MCP server authorizes with a bearer token or an Authorization header; OAuth login is not used", name)
	}
	if opts.Store == nil {
		return errors.New("no credential store")
	}
	if u := opts.Settings.CallbackURL; u != "" {
		if err := checkCallbackURL(u); err != nil {
			return fmt.Errorf("mcp_oauth_callback_url: %w", err)
		}
	}
	if opts.Timeout <= 0 {
		opts.Timeout = DefaultLoginTimeout
	}
	if opts.Out == nil {
		opts.Out = io.Discard
	}
	client := oauthClient(opts.HTTPClient)
	cb, err := listenCallback(ctx, c.OAuth, opts.Settings)
	if err != nil {
		return err
	}
	defer cb.close() //nolint:contextcheck // shuts down after ctx may be done
	saved := &loginResult{}
	handler, err := auth.NewAuthorizationCodeHandler(loginConfig(name, c, opts, cb, saved, withResource(client, c.OAuthResource)))
	if err != nil {
		return fmt.Errorf("failed to set up the OAuth login: %w", err)
	}
	getenv := opts.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	headers, err := httpHeaders(c, getenv)
	if err != nil {
		return err
	}
	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	server, err := serverClient(c.URL, base, headers)
	if err != nil {
		return err
	}
	t := &sdk.StreamableClientTransport{
		Endpoint: c.URL, HTTPClient: server,
		OAuthHandler: handler, DisableStandaloneSSE: true, MaxRetries: -1,
	}
	session, err := sdk.NewClient(implementation, nil).Connect(ctx, t, nil)
	if err != nil {
		return fmt.Errorf("failed to log in to %s: %w", name, err)
	}
	_ = session.Close()
	if !saved.done() {
		return fmt.Errorf("the %s MCP server did not ask for authorization; it needs no login", name)
	}

	return nil
}

// loginResult records that the flow finished and its tokens were saved.
type loginResult struct {
	mu    sync.Mutex
	saved bool
}

func (r *loginResult) done() bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.saved
}

// loginConfig is the SDK handler's configuration: the client registration,
// the callback, the scopes, and saving the tokens.
func loginConfig(name string, c ServerConfig, opts LoginOptions, cb *callback, saved *loginResult, client *http.Client) *auth.AuthorizationCodeHandlerConfig {
	cfg := &auth.AuthorizationCodeHandlerConfig{
		RedirectURL: cb.redirect,
		DynamicClientRegistrationConfig: &auth.DynamicClientRegistrationConfig{Metadata: &oauthex.ClientRegistrationMetadata{
			RedirectURIs: []string{cb.redirect}, ClientName: "uah", TokenEndpointAuthMethod: "none",
			GrantTypes: []string{"authorization_code", "refresh_token"}, ResponseTypes: []string{"code"},
		}},
		AuthorizationCodeFetcher: func(ctx context.Context, args *auth.AuthorizationArgs) (*auth.AuthorizationResult, error) {
			return cb.fetch(ctx, name, resourceURL(args.URL, c.OAuthResource), opts)
		},
		RequestRefreshToken:   true,
		AcceptUnadvertisedIss: true,
		Client:                client,
		NewTokenSource: func(ctx context.Context, oc *oauth2.Config, tok *oauth2.Token) (oauth2.TokenSource, error) {
			creds := Credentials{
				ServerName: name, ServerURL: c.URL, ClientID: oc.ClientID, ClientSecret: oc.ClientSecret,
				TokenURL: oc.Endpoint.TokenURL, AuthStyle: int(oc.Endpoint.AuthStyle), Scopes: oc.Scopes,
			}.withToken(tok)
			if err := opts.Store.Save(creds); err != nil {
				return nil, err // the store's own error
			}
			saved.mu.Lock()
			saved.saved = true
			saved.mu.Unlock()

			// The SDK reads the token again at once; a refresh then must be
			// saved too, or the stored refresh token goes stale.
			return &saving{base: oc.TokenSource(ctx, tok), store: opts.Store, logger: discard, creds: creds, last: tok.AccessToken}, nil
		},
	}
	if c.OAuth != nil && c.OAuth.ClientID != "" {
		cfg.PreregisteredClient = &oauthex.ClientCredentials{ClientID: c.OAuth.ClientID}
	}
	scopes := slices.Clone(c.Scopes)
	if len(opts.Scopes) > 0 {
		scopes = slices.Clone(opts.Scopes)
	}
	if len(scopes) > 0 {
		cfg.ScopeFilter = func([]string) []string { return scopes }
	}

	return cfg
}

// Logout deletes the server's stored login. ok is false when there was none.
func Logout(name string, c ServerConfig, store CredentialStore) (bool, error) {
	if c.URL == "" {
		return false, errors.New("OAuth logout is only supported for streamable_http transports")
	}

	return store.Delete(name, c.URL) // the store's own error
}
