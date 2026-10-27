package embedded_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/engine/codexauth"
	"github.com/viktordanov/uah/internal/engine/embedded"
	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/testing/fakellm"
)

// accessToken is a test token (not a real one) for acct-test expiring at
// exp; n makes tokens with the same expiry differ.
func accessToken(exp time.Time, n int) string {
	claims := `{"exp":` + strconv.FormatInt(exp.Unix(), 10) + `,"n":` + strconv.Itoa(n) +
		`,"https://api.openai.com/auth":{"chatgpt_account_id":"acct-test"}}`

	return "x." + base64.RawURLEncoding.EncodeToString([]byte(claims)) + ".y"
}

// codexLogin is an env whose Codex login has a refresh token, with a fake
// token endpoint that issues fresh.
type codexLogin struct {
	*env
	fresh    string
	tokenURL string
	calls    *atomic.Int32
}

func newCodexLogin(t *testing.T, access string, replies ...fakellm.Reply) *codexLogin {
	t.Helper()
	e := newEnv(t, replies...)
	auth := `{"auth_mode":"chatgpt","tokens":{"id_token":"id","access_token":"` + access +
		`","refresh_token":"rt-1","account_id":"acct-test"},"last_refresh":"2026-09-01T00:00:00Z"}`
	require.NoError(t, os.WriteFile(filepath.Join(e.CodexHome, "auth.json"), []byte(auth), 0o600))
	c := &codexLogin{env: e, fresh: accessToken(time.Now().Add(10*24*time.Hour), 2), calls: &atomic.Int32{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		c.calls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": c.fresh, "refresh_token": "rt-2"})
	}))
	t.Cleanup(srv.Close)
	c.tokenURL = srv.URL

	return c
}

func (c *codexLogin) getenv(key string) string {
	if key == codexauth.TokenURLEnv {
		return c.tokenURL
	}

	return c.env.getenv(key)
}

// run sends one message and returns the result and the runner's errors.
func (c *codexLogin) run(t *testing.T, eng engine.Engine) (core.Result, string) {
	t.Helper()
	settings := session.Settings{Provider: codexauth.Provider, Model: "gpt-test", Effort: "high", Workspace: c.Workspace, BaseURL: c.llm.URL}
	s, err := session.Open(context.Background(), eng, session.Options{Settings: settings})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	ev := &events{t: t, s: s}
	_, err = s.Submit("hi")
	require.NoError(t, err)
	result := ev.finished()
	ev.idle()
	var errs string
	for _, e := range ev.all {
		if re, ok := e.(core.RunnerError); ok {
			errs += re.Message + "\n"
		}
	}

	return result, errs
}

// TestEmbedded_CodexRenewsARejectedLogin: the backend rejects the token
// once, as after the login expired while the session was open (GitHub #1);
// the engine refreshes it and sends the request again.
func TestEmbedded_CodexRenewsARejectedLogin(t *testing.T) {
	old := accessToken(time.Now().Add(24*time.Hour), 1)
	c := newCodexLogin(t, old, fakellm.Reply{Fail: http.StatusUnauthorized, FailBody: `{"detail":"token expired"}`}, fakellm.Reply{Text: "hello"})
	eng := embedded.New(embedded.Config{StateDir: c.StateDir, Provider: codexauth.Provider, Getenv: c.getenv})

	result, errs := c.run(t, eng)
	assert.Equal(t, core.StatusOK, result.Status, errs)
	assert.Equal(t, "hello", result.Answer)
	reqs := c.llm.Requests()
	require.Len(t, reqs, 2)
	assert.Equal(t, "Bearer "+old, reqs[0].Authorization)
	assert.Equal(t, "Bearer "+c.fresh, reqs[1].Authorization)
	assert.EqualValues(t, 1, c.calls.Load())
}

// TestEmbedded_RefreshesAnExpiredCodexLoginBeforeTheRun: uagent's
// preflight blocks an expired token, so the engine refreshes it first.
func TestEmbedded_RefreshesAnExpiredCodexLoginBeforeTheRun(t *testing.T) {
	c := newCodexLogin(t, accessToken(time.Now().Add(-time.Hour), 1), fakellm.Reply{Text: "hello"})
	result, errs := c.run(t, embedded.New(embedded.Config{StateDir: c.StateDir, Provider: codexauth.Provider, Getenv: c.getenv}))
	assert.Equal(t, core.StatusOK, result.Status, errs)
	require.Len(t, c.llm.Requests(), 1)
	assert.Equal(t, "Bearer "+c.fresh, c.llm.Requests()[0].Authorization)
	assert.EqualValues(t, 1, c.calls.Load())
}

// TestEmbedded_CodexCompressesItsRequests: the openai-codex client sends
// its bodies compressed with zstd, as Codex does.
func TestEmbedded_CodexCompressesItsRequests(t *testing.T) {
	c := newCodexLogin(t, accessToken(time.Now().Add(24*time.Hour), 1), fakellm.Reply{Text: "hello"})
	result, errs := c.run(t, embedded.New(embedded.Config{StateDir: c.StateDir, Provider: codexauth.Provider, Getenv: c.getenv}))
	assert.Equal(t, core.StatusOK, result.Status, errs)
	require.Len(t, c.llm.Arrivals(), 1)
	assert.Equal(t, "zstd", c.llm.Arrivals()[0].Encoding)
	assert.Equal(t, []string{"hi"}, c.llm.Requests()[0].UserTexts)
}

// TestEmbedded_CodexLoginThatCannotBeRefreshed says to sign in again.
func TestEmbedded_CodexLoginThatCannotBeRefreshed(t *testing.T) {
	c := newCodexLogin(t, accessToken(time.Now().Add(24*time.Hour), 1), fakellm.Reply{Fail: http.StatusUnauthorized})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	}))
	t.Cleanup(srv.Close)
	c.tokenURL = srv.URL
	result, errs := c.run(t, embedded.New(embedded.Config{StateDir: c.StateDir, Provider: codexauth.Provider, Getenv: c.getenv}))
	assert.NotEqual(t, core.StatusOK, result.Status)
	assert.Contains(t, errs, "Your ChatGPT login expired; run `codex login`")
	assert.NotContains(t, errs, "rt-1")
	assert.Len(t, c.llm.Requests(), 1, "a refused refresh is not retried")
}
