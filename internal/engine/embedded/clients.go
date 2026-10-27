package embedded

// The providers' Responses clients, built as unreal-agent-runner v0.1.1
// builds them (harness/llm/clients/*/client.go and the HTTP client of
// harness/primitives/remote.go, MIT License, Copyright (c) 2026 Unreal
// Labs; see THIRD_PARTY_NOTICES.md) but over an *http.Client uah makes,
// whose transport sees each attempt of a model request (transport.go). The
// runner's constructors make their own and take no transport. The request
// each client sends is the runner's (TestClients_MatchTheRunner).

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/viktordanov/uah-core/harness/llm"
	"github.com/viktordanov/uah-core/harness/llm/clients/fireworks"
	"github.com/viktordanov/uah-core/harness/llm/clients/ollama"
	"github.com/viktordanov/uah-core/harness/llm/clients/openaicodex"
	"github.com/viktordanov/uah-core/harness/llm/responsesapi"
	"github.com/viktordanov/uah-core/harness/primitives"

	"github.com/viktordanov/uah/internal/engine/codexauth"
)

const (
	headerContentType = "Content-Type"
	contentJSON       = "application/json"
)

// remoteHTTPClient is primitives.NewRemoteClient's HTTP client over the
// model transport.
func remoteHTTPClient(ts *transports, headerTimeout time.Duration) *http.Client {
	return &http.Client{Transport: callTransport{base: ts.get(headerTimeout)}}
}

// headerTimeout is responseHeaderTimeout, or none for a loopback server,
// which may take long to load its model.
func headerTimeout(baseURL string) time.Duration {
	if u, err := url.Parse(baseURL); err == nil && loopback(u.Hostname()) {
		return 0
	}

	return responseHeaderTimeout
}

// codexHTTPClient is openaicodex.NewClient's HTTP client over the model
// transport, never forwarding subscription credentials through redirects.
// Under the observing transport, the login sets the credentials on each
// attempt and renews them after a 401 (codexauth.Login.Transport), where
// the runner's client sends the token it read once. Bodies go out
// compressed, as Codex sends them (compress.go).
func codexHTTPClient(ts *transports, login *codexauth.Login, baseURL string) *http.Client {
	return &http.Client{
		Transport:     callTransport{base: login.Transport(zstdTransport{base: ts.get(headerTimeout(baseURL))})},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// newClient builds a Responses client over hc with the attempt limit, for
// priority processing service_tier "priority", and for effort ultra the
// reasoning field as the runner's encoder writes it for the other efforts.
func newClient(hc *http.Client, c ClientConfig, config responsesapi.Config) (remoteAdapter, error) {
	remote := primitives.NewRemoteClientWithHTTPClient(hc)
	config.MaxAttempts = &c.MaxAttempts
	config.Extensions = maps.Clone(config.Extensions)
	if config.Extensions == nil {
		config.Extensions = map[string]jsontext.Value{}
	}
	if c.Priority {
		config.Extensions["service_tier"] = jsontext.Value(`"priority"`)
	}
	if c.Ultra {
		config.Extensions["reasoning"] = jsontext.Value(`{"effort":"ultra","summary":"auto"}`)
	}
	adapter, err := responsesapi.NewAdapter(remote, config)
	if err != nil {
		_ = remote.Close()

		return remoteAdapter{}, err // the provider wraps it
	}

	return remoteAdapter{Adapter: adapter, remote: remote}, nil
}

// keyedClient is the client of a provider with an API key: openai,
// openrouter, and fireworks, which name themselves in their errors.
func keyedClient(name string, c ClientConfig, cache responsesapi.CacheKeyPlacement, extensions map[string]jsontext.Value) (remoteAdapter, error) {
	if strings.TrimSpace(c.APIKey) == "" {
		return remoteAdapter{}, errors.New(name + " API key must be set")
	}
	baseURL := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if baseURL == "" {
		return remoteAdapter{}, errors.New(name + " base URL must be set")
	}

	return newClient(remoteHTTPClient(c.transports, headerTimeout(baseURL)), c, responsesapi.Config{
		Endpoint:          baseURL + "/responses",
		Headers:           map[string][]string{"Authorization": {"Bearer " + c.APIKey}, headerContentType: {contentJSON}},
		CacheKeyPlacement: cache,
		Extensions:        extensions,
	})
}

func openaiClient(c ClientConfig) (Client, error) {
	return keyedClient("OpenAI", c, responsesapi.CacheKeyPlacement{UsePromptCacheKeyField: true}, nil)
}

// openrouterClient asks for OpenRouter's one-hour prompt caching, as the
// runner's client does.
func openrouterClient(c ClientConfig) (Client, error) {
	return keyedClient("OpenRouter", c, responsesapi.CacheKeyPlacement{Header: "x-session-id"},
		map[string]jsontext.Value{"cache_control": jsontext.Value(`{"type":"ephemeral","ttl":"1h"}`)})
}

// fireworksClient keeps the runner's client's usage conversion, which
// wraps any adapter.
func fireworksClient(c ClientConfig) (Client, error) {
	ra, err := keyedClient("fireworks", c, responsesapi.CacheKeyPlacement{Header: "x-session-affinity"}, nil)
	if err != nil {
		return nil, err
	}

	return remoteAdapter{Adapter: &fireworks.Client{Adapter: ra.Adapter}, remote: ra.remote}, nil
}

func ollamaClient(c ClientConfig) (Client, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if baseURL == "" {
		baseURL = ollama.BaseURL
	}

	return newClient(remoteHTTPClient(c.transports, 0), c, responsesapi.Config{
		Endpoint: baseURL + "/responses",
		Headers:  map[string][]string{headerContentType: {contentJSON}},
	})
}

// codexClient is the openai-codex client: ChatGPT subscription credentials
// from the environment or Codex's auth file, which it refreshes.
func codexClient(c ClientConfig) (Client, error) {
	login, err := codexauth.Open(c.Getenv)
	if err != nil {
		return nil, err // the provider wraps it
	}
	baseURL, err := codexBaseURL(c.BaseURL)
	if err != nil {
		return nil, err
	}
	if c.MaxAttempts <= 0 {
		return nil, errors.New("max attempts must be positive")
	}
	if _, err := login.Check(); err != nil {
		return nil, err // the provider wraps it
	}
	ra, err := newClient(codexHTTPClient(c.transports, login, baseURL), c, responsesapi.Config{
		Endpoint: baseURL + "/responses",
		Headers: map[string][]string{
			headerContentType: {contentJSON},
			"originator":      {"uah-core"},
			"User-Agent":      {"uah-core"},
		},
		CacheKeyPlacement: responsesapi.CacheKeyPlacement{UsePromptCacheKeyField: true, Header: "session-id"},
	})
	if err != nil {
		return nil, err
	}

	return codexAdapter{remoteAdapter: ra, fromFile: login.FromFile()}, nil
}

// codexBaseURL accepts the ChatGPT backend or a loopback endpoint, as the
// runner's client does.
func codexBaseURL(value string) (string, error) {
	value = strings.TrimRight(strings.TrimSpace(value), "/")
	if value == "" || value == openaicodex.BaseURL {
		return openaicodex.BaseURL, nil
	}
	parsed, err := url.Parse(value)
	if err == nil && parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == "" &&
		(parsed.Scheme == "http" || parsed.Scheme == "https") && net.ParseIP(parsed.Hostname()) != nil && loopback(parsed.Hostname()) {
		return value, nil
	}

	return "", errors.New("codex base URL must be " + openaicodex.BaseURL + " or an explicit loopback IP endpoint")
}

// codexAdapter checks and explains as openaicodex.Client.Respond does. A
// 401 from a login uah refreshes, or a refresh the token endpoint refused,
// says to sign in again (codexauth.ErrLoginExpired).
type codexAdapter struct {
	remoteAdapter

	fromFile bool
}

func (a codexAdapter) Respond(ctx context.Context, req llm.Request, opts llm.RequestOptions) (llm.Response, error) {
	if err := ctx.Err(); err != nil {
		return llm.Response{}, err // as the runner's client
	}
	if req.Model.MaxOutputTokens != nil {
		return llm.Response{}, errors.New("codex does not support max_output_tokens")
	}
	resp, err := a.Adapter.Respond(ctx, req, opts)
	if errors.Is(err, codexauth.ErrLoginExpired) {
		return llm.Response{}, codexauth.ErrLoginExpired
	}
	if apiErr, ok := errors.AsType[*responsesapi.APIError](err); ok && apiErr.StatusCode == http.StatusUnauthorized {
		if a.fromFile {
			return llm.Response{}, fmt.Errorf("%w (%w)", codexauth.ErrLoginExpired, err)
		}

		return llm.Response{}, fmt.Errorf("codex credentials rejected; renew them externally and recreate the client: %w", err)
	}

	return resp, err // the coordinator wraps model errors
}
