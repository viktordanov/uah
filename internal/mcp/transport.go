package mcp

import (
	"fmt"
	"maps"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"slices"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// defaultEnv is what a stdio server gets from uah's environment before
// env_vars and env, as in Codex (codex-rs/rmcp-client/src/utils.rs).
var defaultEnv = []string{"HOME", "LOGNAME", "PATH", "SHELL", "USER", "__CF_USER_TEXT_ENCODING", "LANG", "LC_ALL", "TERM", "TMPDIR", "TZ"}

// transport builds the SDK transport for a server.
func (m *Manager) transport(s *server) (sdk.Transport, error) {
	c := s.cfg
	if c.URL != "" {
		headers, err := m.headers(c)
		if err != nil {
			return nil, err
		}
		client, err := serverClient(c.URL, http.DefaultTransport, headers)
		if err != nil {
			return nil, err
		}
		t := &sdk.StreamableClientTransport{Endpoint: c.URL, HTTPClient: client}
		m.mu.Lock()
		auth := s.auth // a new login replaces it
		m.mu.Unlock()
		if auth != nil {
			t.OAuthHandler = auth
		}

		return t, nil
	}
	cmd := exec.Command(c.Command, c.Args...) //nolint:noctx // the user configured it; the transport stops it
	cmd.Dir = c.Cwd
	if cmd.Dir == "" {
		cmd.Dir = m.opts.Workspace
	}
	cmd.Env = m.env(c)
	// Codex logs a server's standard error line by line; the screen never
	// shows it.
	cmd.Stderr = &stderrLog{logger: m.opts.Logger, server: s.name}

	return &sdk.CommandTransport{Command: cmd}, nil
}

// env is the stdio server's environment: the default variables, then
// env_vars by name, then env.
func (m *Manager) env(c ServerConfig) []string {
	vars := map[string]string{}
	for _, name := range append(slices.Clone(defaultEnv), c.EnvVars...) {
		if v := m.opts.Getenv(name); v != "" {
			vars[name] = v
		}
	}
	maps.Copy(vars, c.Env)
	out := make([]string, 0, len(vars))
	for _, k := range slices.Sorted(maps.Keys(vars)) {
		out = append(out, k+"="+vars[k])
	}

	return out
}

// headers are http_headers, env_http_headers, and the bearer token.
func (m *Manager) headers(c ServerConfig) (http.Header, error) { return httpHeaders(c, m.opts.Getenv) }

func httpHeaders(c ServerConfig, getenv func(string) string) (http.Header, error) {
	h := http.Header{}
	for k, v := range c.HTTPHeaders {
		h.Set(k, v)
	}
	for k, name := range c.EnvHTTPHeaders {
		if v := getenv(name); v != "" {
			h.Set(k, v)
		}
	}
	if name := c.BearerTokenEnvVar; name != "" {
		token := strings.TrimSpace(getenv(name))
		if token == "" {
			return nil, fmt.Errorf("the environment variable %s for the bearer token is not set", name)
		}
		h.Set("Authorization", "Bearer "+token)
	}

	return h, nil
}

// maxRedirects is Go's and Codex's limit on redirects in one request.
const maxRedirects = 10

// serverClient is the HTTP client for a server at endpoint. It adds the
// configured headers only to requests to the endpoint's origin, and it
// follows redirects only within that origin, so neither the configured
// headers nor what the SDK sets (the OAuth token, the session ID) reach
// another origin, and https never becomes http. Codex's MCP client
// refuses the same redirects (rmcp-client/src/http_client_redirect.rs).
func serverClient(endpoint string, base http.RoundTripper, headers http.Header) (*http.Client, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("invalid MCP server URL: %w", err)
	}

	return &http.Client{Transport: headerTransport{base: base, headers: headers, origin: origin(u)}, CheckRedirect: sameOriginRedirect}, nil
}

// oauthClient is client for OAuth requests (discovery, registration, the
// token endpoint): it follows redirects only within a request's origin, so
// a code, refresh token, or client secret in a body a 307 or 308 replays
// stays there, as Codex confines OAuth redirects. A nil client is
// http.DefaultClient.
func oauthClient(client *http.Client) *http.Client {
	if client == nil {
		client = http.DefaultClient
	}
	c := *client
	c.CheckRedirect = sameOriginRedirect

	return &c
}

// sameOriginRedirect is an http.Client's CheckRedirect that refuses a
// redirect away from the origin of the first request.
func sameOriginRedirect(r *http.Request, via []*http.Request) error {
	if from, to := origin(via[0].URL), origin(r.URL); from != to {
		return fmt.Errorf("refusing a redirect from %s to another origin, %s", from, to)
	}
	if len(via) >= maxRedirects {
		return fmt.Errorf("stopped after %d redirects", maxRedirects)
	}

	return nil
}

// origin is the URL's scheme, host, and port, in lower case and with the
// scheme's default port, so that equal origins compare equal.
func origin(u *url.URL) string {
	scheme, port := strings.ToLower(u.Scheme), u.Port()
	switch {
	case port != "":
	case scheme == "https":
		port = "443"
	default: // http; the client sends no other scheme
		port = "80"
	}

	return scheme + "://" + net.JoinHostPort(strings.ToLower(u.Hostname()), port)
}

// headerTransport adds fixed headers to every request to the server's
// origin.
type headerTransport struct {
	base    http.RoundTripper
	headers http.Header
	origin  string
}

func (t headerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if len(t.headers) > 0 && origin(r.URL) == t.origin {
		r = r.Clone(r.Context())
		maps.Copy(r.Header, t.headers)
	}

	return t.base.RoundTrip(r) // a transport passes errors through
}
