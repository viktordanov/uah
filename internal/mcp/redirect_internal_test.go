package mcp

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recorder is a server that records the requests it gets.
type recorder struct {
	*httptest.Server

	mu   sync.Mutex
	h    http.HandlerFunc
	reqs []recorded
}

type recorded struct {
	path, body string
	header     http.Header
}

func newRecorder(t *testing.T, tls bool, h http.HandlerFunc) *recorder {
	t.Helper()
	r := &recorder{h: h}
	handler := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.reqs = append(r.reqs, recorded{path: req.URL.Path, body: string(body), header: req.Header.Clone()})
		h := r.h
		r.mu.Unlock()
		if h != nil {
			h(w, req)
		}
	})
	if tls {
		r.Server = httptest.NewTLSServer(handler)
	} else {
		r.Server = httptest.NewServer(handler)
	}
	t.Cleanup(r.Close)

	return r
}

func (r *recorder) handle(h http.HandlerFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.h = h
}

func (r *recorder) requests() []recorded {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]recorded(nil), r.reqs...)
}

func redirectTo(target string, code int) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/mcp" {
			w.Header().Set("Location", target)
			w.WriteHeader(code)

			return
		}
		_, _ = io.WriteString(w, "ok")
	}
}

var secretHeaders = http.Header{"Authorization": {"Bearer secret"}, "X-Api-Key": {"key"}}

func post(t *testing.T, client *http.Client, u string) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, u, strings.NewReader(`{"jsonrpc":"2.0"}`))
	require.NoError(t, err)
	req.Header.Set("Mcp-Session-Id", "session")
	resp, err := client.Do(req)
	if resp != nil {
		_ = resp.Body.Close()
	}

	return resp, err //nolint:wrapcheck // the test reads it
}

func TestServerClientSameOriginRedirectKeepsHeaders(t *testing.T) {
	t.Parallel()
	for _, location := range []string{"/next", "next", "upper-case host"} {
		srv := newRecorder(t, false, nil)
		target := location
		if target == "upper-case host" {
			// The same origin, written another way.
			u, _ := url.Parse(srv.URL)
			target = "HTTP://" + strings.ToUpper(u.Hostname()) + ":" + u.Port() + "/next"
		}
		srv.handle(func(w http.ResponseWriter, req *http.Request) {
			if req.URL.Path == "/mcp" {
				http.Redirect(w, req, target, http.StatusTemporaryRedirect)
			}
		})
		client, err := serverClient(srv.URL+"/mcp", srv.Client().Transport, secretHeaders)
		require.NoError(t, err)
		resp, err := post(t, client, srv.URL+"/mcp")
		require.NoError(t, err, location)
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		reqs := srv.requests()
		require.Len(t, reqs, 2, location)
		assert.Equal(t, "/next", reqs[1].path)
		assert.Equal(t, "Bearer secret", reqs[1].header.Get("Authorization"), "the redirect keeps the configured headers")
		assert.Equal(t, "key", reqs[1].header.Get("X-Api-Key"))
		assert.JSONEq(t, `{"jsonrpc":"2.0"}`, reqs[1].body, "a 307 replays the body")
	}
}

func TestServerClientRefusesCrossOriginRedirects(t *testing.T) {
	t.Parallel()
	other := newRecorder(t, false, nil)
	otherURL, _ := url.Parse(other.URL)
	for name, target := range map[string]string{
		"another port": other.URL + "/steal",
		"localhost":    "http://localhost:" + otherURL.Port() + "/steal",
	} {
		for _, code := range []int{http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
			srv := newRecorder(t, false, redirectTo(target, code))
			client, err := serverClient(srv.URL+"/mcp", srv.Client().Transport, secretHeaders)
			require.NoError(t, err)
			_, err = post(t, client, srv.URL+"/mcp")
			require.Error(t, err, "%s %d", name, code)
			assert.Contains(t, err.Error(), "refusing a redirect from http://127.0.0.1:")
		}
	}
	assert.Empty(t, other.requests(), "nothing reaches the other origin")
}

// Through a redirect chain: a same-origin hop first, then another origin.
func TestServerClientRefusesCrossOriginLaterInAChain(t *testing.T) {
	t.Parallel()
	other := newRecorder(t, false, nil)
	srv := newRecorder(t, false, func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/mcp":
			http.Redirect(w, req, "/hop", http.StatusPermanentRedirect)
		case "/hop":
			http.Redirect(w, req, other.URL+"/steal", http.StatusPermanentRedirect)
		}
	})
	client, err := serverClient(srv.URL+"/mcp", srv.Client().Transport, secretHeaders)
	require.NoError(t, err)
	_, err = post(t, client, srv.URL+"/mcp")
	require.Error(t, err)
	assert.Len(t, srv.requests(), 2)
	assert.Empty(t, other.requests())
}

func TestServerClientRefusesHTTPSDowngrade(t *testing.T) {
	t.Parallel()
	plain := newRecorder(t, false, nil)
	plainURL, _ := url.Parse(plain.URL)
	srv := newRecorder(t, true, nil)
	srvURL, _ := url.Parse(srv.URL)
	// The same host and port over http: another origin.
	srv.handle(redirectTo("http://"+srvURL.Host+"/mcp", http.StatusTemporaryRedirect))
	client, err := serverClient(srv.URL+"/mcp", srv.Client().Transport, secretHeaders)
	require.NoError(t, err)
	_, err = post(t, client, srv.URL+"/mcp")
	require.ErrorContains(t, err, "refusing a redirect from https://")

	srv.handle(redirectTo(plain.URL+"/mcp", http.StatusFound))
	_, err = post(t, client, srv.URL+"/mcp")
	require.ErrorContains(t, err, "to another origin, http://"+plainURL.Host)
	assert.Empty(t, plain.requests())
}

// The headers go only to the server's origin, even when a request goes
// elsewhere without a redirect.
func TestHeaderTransportOnlyAddsHeadersToTheOrigin(t *testing.T) {
	t.Parallel()
	srv := newRecorder(t, false, nil)
	other := newRecorder(t, false, nil)
	client, err := serverClient(srv.URL+"/mcp", srv.Client().Transport, secretHeaders)
	require.NoError(t, err)
	_, err = post(t, client, other.URL+"/mcp")
	require.NoError(t, err)
	_, err = post(t, client, srv.URL+"/mcp")
	require.NoError(t, err)
	assert.Empty(t, other.requests()[0].header.Get("Authorization"))
	assert.Empty(t, other.requests()[0].header.Get("X-Api-Key"))
	assert.Equal(t, "Bearer secret", srv.requests()[0].header.Get("Authorization"))
}

func TestOrigin(t *testing.T) {
	t.Parallel()
	same := [][2]string{
		{"https://Example.COM/mcp", "https://example.com:443/other?q=1"},
		{"HTTP://example.com", "http://example.com:80/"},
		{"http://[::1]:8080/a", "http://[::1]:8080/b"},
		{"http://[FE80::1]/", "http://[fe80::1]:80/"},
		{"https://user:pass@example.com/", "https://example.com/"},
	}
	for _, p := range same {
		a, _ := url.Parse(p[0])
		b, _ := url.Parse(p[1])
		assert.Equal(t, origin(a), origin(b), "%s and %s", p[0], p[1])
	}
	different := [][2]string{
		{"https://example.com/", "http://example.com/"},
		{"https://example.com/", "https://example.com:8443/"},
		{"https://example.com/", "https://sub.example.com/"},
		{"https://example.com/", "https://example.com./"},
		{"http://[::1]/", "http://127.0.0.1/"},
		{"http://localhost/", "http://127.0.0.1/"},
	}
	for _, p := range different {
		a, _ := url.Parse(p[0])
		b, _ := url.Parse(p[1])
		assert.NotEqual(t, origin(a), origin(b), "%s and %s", p[0], p[1])
	}
}

// A token endpoint that redirects a refresh elsewhere with a 307 would
// replay the refresh token and client secret there; the OAuth client
// refuses.
func TestTokenRefreshRefusesCrossOriginRedirect(t *testing.T) {
	t.Parallel()
	other := newRecorder(t, false, nil)
	as := newRecorder(t, false, func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, other.URL+"/token", http.StatusTemporaryRedirect)
	})
	creds := Credentials{
		ServerName: "remote", ServerURL: "http://mcp.invalid/", ClientID: "id", ClientSecret: "client-secret",
		TokenURL: as.URL + "/token", AccessToken: "old", RefreshToken: "refresh-secret",
		ExpiresAt: time.Now().Add(-time.Hour).UnixMilli(),
	}
	src := savingSource(oauthClient(as.Client()), creds, nil, discard)
	_, err := src.Token()
	require.Error(t, err)
	reqs := as.requests()
	require.NotEmpty(t, reqs) // the oauth2 package tries both auth styles
	assert.Contains(t, reqs[0].body, "refresh-secret", "the token endpoint itself gets the refresh")
	assert.Empty(t, other.requests(), "the refresh token and client secret stay at the token endpoint's origin")

	// The same origin still works.
	as.handle(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/token" {
			http.Redirect(w, req, "/token2", http.StatusTemporaryRedirect)

			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"new","token_type":"Bearer","expires_in":3600}`)
	})
	store := &FileStore{Path: t.TempDir() + "/creds.json"}
	tok, err := savingSource(oauthClient(as.Client()), creds, store, discard).Token()
	require.NoError(t, err)
	assert.Equal(t, "new", tok.AccessToken)
}
