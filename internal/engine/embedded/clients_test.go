package embedded_test

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/viktordanov/uah-core/harness/llm"
	"github.com/viktordanov/uah-core/harness/llm/clients/fireworks"
	"github.com/viktordanov/uah-core/harness/llm/clients/ollama"
	"github.com/viktordanov/uah-core/harness/llm/clients/openai"
	"github.com/viktordanov/uah-core/harness/llm/clients/openaicodex"
	"github.com/viktordanov/uah-core/harness/llm/clients/openrouter"

	"github.com/viktordanov/uah/internal/engine/embedded"
)

// sent is one request as the server saw it, its body decoded.
type sent struct {
	Method, Path string
	Header       http.Header
	Body         string
	Encoding     string
}

// recorder answers every request with one completed response and keeps
// what it was sent.
type recorder struct {
	mu   sync.Mutex
	sent []sent
}

func (r *recorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	h := req.Header.Clone()
	h.Del("Content-Length")
	encoding := h.Get("Content-Encoding")
	if encoding == "zstd" {
		dec, _ := zstd.NewReader(nil)
		body, _ = dec.DecodeAll(body, nil)
		dec.Close()
		h.Del("Content-Encoding")
	}
	r.mu.Lock()
	r.sent = append(r.sent, sent{Method: req.Method, Path: req.URL.Path, Header: h, Body: string(body), Encoding: encoding})
	r.mu.Unlock()
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"id":"r","object":"response","status":"completed","output":[],`+
		`"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`+"\n\n")
}

func (r *recorder) last(t *testing.T) sent {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	require.NotEmpty(t, r.sent)

	return r.sent[len(r.sent)-1]
}

// codexToken is a test access token (not a real one) with an account and
// an expiry in its claims.
func codexToken() string {
	claims := `{"exp":` + strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10) + `,"https://api.openai.com/auth":{"chatgpt_account_id":"acct-1"}}`

	return "x." + base64.RawURLEncoding.EncodeToString([]byte(claims)) + ".y"
}

// TestClients_MatchTheRunner: each provider's client sends what the
// runner's own client sends, byte for byte, headers included; the
// openai-codex client compresses its body, as Codex does.
func TestClients_MatchTheRunner(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(rec)
	t.Cleanup(srv.Close)
	attempts := 1
	env := map[string]string{"OPENAI_CODEX_ACCESS_TOKEN": codexToken()}
	getenv := func(k string) string { return env[k] }
	runner := map[string]func() (llm.Adapter, error){
		"ollama": func() (llm.Adapter, error) {
			return ollama.NewClient(ollama.Config{BaseURL: srv.URL, MaxAttempts: &attempts})
		},
		"openai": func() (llm.Adapter, error) {
			return openai.NewClient(openai.Config{APIKey: "k", BaseURL: srv.URL, MaxAttempts: &attempts})
		},
		"openai-codex": func() (llm.Adapter, error) {
			c, err := openaicodex.EnvironmentConfig(getenv)
			require.NoError(t, err)
			c.BaseURL, c.MaxAttempts = srv.URL, &attempts

			return openaicodex.NewClient(c)
		},
		"openrouter": func() (llm.Adapter, error) {
			return openrouter.NewClient(openrouter.Config{APIKey: "k", BaseURL: srv.URL, MaxAttempts: &attempts})
		},
		"fireworks": func() (llm.Adapter, error) {
			return fireworks.NewClient(fireworks.Config{APIKey: "k", BaseURL: srv.URL, MaxAttempts: &attempts})
		},
	}
	req := llm.Request{
		Model: llm.Model{ID: "m", ReasoningEffort: llm.ReasoningEffortHigh},
		Input: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleUser, Text: "hi"}}},
	}
	opts := llm.RequestOptions{CacheKey: "session-1"}
	for _, p := range embedded.DefaultProviders() {
		t.Run(p.Name, func(t *testing.T) {
			theirs, err := runner[p.Name]()
			require.NoError(t, err)
			if c, ok := theirs.(io.Closer); ok {
				t.Cleanup(func() { _ = c.Close() })
			}
			_, err = theirs.Respond(context.Background(), req, opts)
			require.NoError(t, err)
			want := rec.last(t)

			ours, err := p.NewClient(embedded.ClientConfig{APIKey: "k", BaseURL: srv.URL, MaxAttempts: attempts, Getenv: getenv})
			require.NoError(t, err)
			t.Cleanup(func() { _ = ours.Close() })
			_, err = ours.Respond(context.Background(), req, opts)
			require.NoError(t, err)
			got := rec.last(t)
			if p.Name == "openai-codex" {
				assert.Equal(t, "zstd", got.Encoding)
				got.Encoding = ""
			}
			assert.Equal(t, want, got)
		})
	}
}
