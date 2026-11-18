package models_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/engine/embedded"
	"github.com/viktordanov/uah/internal/models"
)

// env serves a map as getenv.
func env(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

// codexHome writes a Codex auth file for account acct-test and returns its
// directory.
func codexHome(t *testing.T, account string) string {
	t.Helper()
	dir := t.TempDir()
	claims := `{"exp":` + strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10) +
		`,"https://api.openai.com/auth":{"chatgpt_account_id":"` + account + `"}}`
	auth := `{"tokens":{"access_token":"x.` + base64.RawURLEncoding.EncodeToString([]byte(claims)) + `.y"}}`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "auth.json"), []byte(auth), 0o600))

	return dir
}

// serve answers path with body and records the last request.
func serve(t *testing.T, path, body string, last **http.Request) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*last = r
		if r.URL.Path != path {
			http.NotFound(w, r)

			return
		}
		w.Header().Set("ETag", `"v1"`)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	return srv
}

func TestSources(t *testing.T) {
	cases := map[string]struct {
		provider, path, body string
		vars                 map[string]string
		baseSuffix           string
		want                 []models.Model
		headers              map[string]string
	}{
		"openai-codex: the ChatGPT backend, as Codex asks it": {
			provider: models.ProviderCodex, path: "/models",
			body: `{"models":[{"slug":"gpt-6-luna","display_name":"GPT-6-Luna","visibility":"list","priority":3,"context_window":272000,
				"max_context_window":872000,"default_reasoning_level":"medium","supported_reasoning_levels":[{"effort":"low","description":"Fast responses"},{"effort":"medium"}],
				"service_tiers":[{"id":"priority","name":"Fast"}],"minimal_client_version":"0.155.0","available_in_plans":["plus"]},
				{"slug":"gpt-6-sol","visibility":"hide","priority":2,"additional_speed_tiers":["fast"],"max_context_window":400000}]}`,
			want: []models.Model{
				{ID: "gpt-6-sol", ContextWindow: 400000, MaxContextWindow: 400000, ServiceTiers: []string{"priority"}, Priority: 2, Hidden: true},
				{
					ID: "gpt-6-luna", DisplayName: "GPT-6-Luna", ContextWindow: 272000, MaxContextWindow: 872000, ReasoningLevels: []string{"low", "medium"},
					ReasoningHelp: map[string]string{"low": "Fast responses"}, DefaultEffort: "medium", ServiceTiers: []string{"priority"}, Priority: 3, Plans: []string{"plus"}, MinClientVersion: "0.155.0",
				},
			},
			headers: map[string]string{"Authorization": "Bearer x.", "ChatGPT-Account-ID": "acct-test", "Originator": "uah-core", "User-Agent": "uah-core"},
		},
		"openai: /v1/models, with bundled metadata for known models": {
			provider: models.ProviderOpenAI, path: "/v1/models", baseSuffix: "/v1", vars: map[string]string{"OPENAI_API_KEY": "sk-test"},
			body: `{"object":"list","data":[{"id":"whisper-1","object":"model"},{"id":"gpt-5.5","object":"model"}]}`,
			want: []models.Model{
				{ID: "gpt-5.5", DisplayName: "GPT-5.5", ContextWindow: 272000, DefaultEffort: "medium", Priority: 13},
				{ID: "whisper-1"},
			},
			headers: map[string]string{"Authorization": "Bearer sk-test"},
		},
		"openrouter: /api/v1/models with context_length": {
			provider: models.ProviderOpenRouter, path: "/api/v1/models", baseSuffix: "/api/v1", vars: map[string]string{"UAH_LLM_API_KEY": "or-key"},
			body:    `{"data":[{"id":"openai/gpt-5.5","name":"OpenAI: GPT-5.5","context_length":400000,"top_provider":{"context_length":272000}}]}`,
			want:    []models.Model{{ID: "openai/gpt-5.5", DisplayName: "OpenAI: GPT-5.5", ContextWindow: 400000}},
			headers: map[string]string{"Authorization": "Bearer or-key"},
		},
		"fireworks: its OpenAI-style list": {
			provider: models.ProviderFireworks, path: "/inference/v1/models", baseSuffix: "/inference/v1", vars: map[string]string{"FIREWORKS_API_KEY": "fw"},
			body: `{"data":[{"id":"accounts/fireworks/models/kimi-k2","object":"model","context_length":131072}]}`,
			want: []models.Model{{ID: "accounts/fireworks/models/kimi-k2", ContextWindow: 131072}},
		},
		"ollama: /api/tags at the host's root": {
			provider: models.ProviderOllama, path: "/api/tags", baseSuffix: "/v1",
			body: `{"models":[{"name":"qwen3:8b","model":"qwen3:8b"},{"name":"llama3.2:latest","model":"llama3.2:latest"}]}`,
			want: []models.Model{{ID: "llama3.2:latest", DisplayName: "llama3.2:latest"}, {ID: "qwen3:8b", DisplayName: "qwen3:8b"}},
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			var last *http.Request
			srv := serve(t, c.path, c.body, &last)
			vars := map[string]string{"CODEX_HOME": codexHome(t, "acct-test")}
			for k, v := range c.vars {
				vars[k] = v
			}
			src, err := models.NewSource(c.provider, srv.URL+c.baseSuffix, env(vars))
			require.NoError(t, err)

			got, err := src.Fetch(context.Background(), "")

			require.NoError(t, err)
			assert.Equal(t, `"v1"`, got.ETag)
			if c.provider == models.ProviderOpenAI {
				enriched := models.Enrich(c.provider, got.Models)
				require.Len(t, enriched, 2)
				for i, m := range enriched {
					assert.Equal(t, c.want[i], models.Model{
						ID: m.ID, DisplayName: m.DisplayName, ContextWindow: m.ContextWindow, DefaultEffort: m.DefaultEffort, Priority: m.Priority,
					})
				}
			} else {
				assert.Equal(t, c.want, got.Models)
			}
			for k, v := range c.headers {
				assert.Contains(t, last.Header.Get(k), v, k)
			}
			if c.provider == models.ProviderCodex {
				assert.Equal(t, models.CodexClientVersion, last.URL.Query().Get("client_version"))
			}
		})
	}
}

func TestNewSource_Errors(t *testing.T) {
	_, err := models.NewSource(models.ProviderOpenAI, "", env(nil))
	require.ErrorContains(t, err, "UAH_LLM_API_KEY or OPENAI_API_KEY must be set")
	_, err = models.NewSource("bedrock", "", env(nil))
	require.ErrorIs(t, err, models.ErrUnsupported)
	_, err = models.NewSource(models.ProviderCodex, "https://example.com/codex", env(map[string]string{"CODEX_HOME": codexHome(t, "a")}))
	require.ErrorContains(t, err, "loopback", "the token goes only to the ChatGPT backend or a local test server")
	_, err = models.NewSource(models.ProviderOpenRouter, "", env(nil))
	require.NoError(t, err, "OpenRouter's list is public")
}

func TestUltrafastUsesModelCatalogAndWorkspacePolicy(t *testing.T) {
	for _, tc := range []struct {
		name, fragment string
		want           bool
	}{
		{"allowed", "", true},
		{"disabled", "[features]\nultrafast_mode=false", false},
		{"legacy disable", "[features]\nfast_mode=false", false},
		{"unreadable", "[broken", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/models":
					_, _ = w.Write([]byte(`{"models":[{"slug":"future","service_tiers":[{"id":"priority"},{"id":"ultrafast"}]},{"slug":"other","service_tiers":[{"id":"priority"}]}]}`))
				case "/wham/config/bundle":
					require.NotEmpty(t, r.Header.Get("Authorization"))
					_ = json.NewEncoder(w).Encode(map[string]any{"requirements_toml": map[string]any{"enterprise_managed": []map[string]string{{"id": "rbac-fast-mode", "contents": tc.fragment}}}})
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			src, err := models.NewSource(models.ProviderCodex, srv.URL, env(map[string]string{"CODEX_HOME": codexHome(t, "workspace")}))
			require.NoError(t, err)
			f, err := src.Fetch(t.Context(), "")
			require.NoError(t, err)
			c := models.Catalog{Origin: models.OriginLive, Models: f.Models}
			assert.Equal(t, tc.want, c.OffersTier("future", "ultrafast"))
			assert.False(t, c.OffersTier("other", "ultrafast"))
			assert.True(t, c.OffersTier("future", "priority"))
		})
	}
}

// TestNewSource_Identity changes with the provider, base URL, and account,
// and never holds the key.
func TestNewSource_Identity(t *testing.T) {
	id := func(provider, base string, vars map[string]string) string {
		src, err := models.NewSource(provider, base, env(vars))
		require.NoError(t, err)

		return src.Identity()
	}
	a := id(models.ProviderOpenAI, "", map[string]string{"OPENAI_API_KEY": "sk-a"})
	assert.NotContains(t, a, "sk-a")
	assert.Equal(t, a, id(models.ProviderOpenAI, "", map[string]string{"OPENAI_API_KEY": "sk-a"}))
	assert.NotEqual(t, a, id(models.ProviderOpenAI, "", map[string]string{"OPENAI_API_KEY": "sk-b"}))
	assert.NotEqual(t, a, id(models.ProviderOpenAI, "http://127.0.0.1:1/v1", map[string]string{"OPENAI_API_KEY": "sk-a"}))
	codexA := id(models.ProviderCodex, "", map[string]string{"CODEX_HOME": codexHome(t, "acct-a")})
	assert.NotEqual(t, codexA, id(models.ProviderCodex, "", map[string]string{"CODEX_HOME": codexHome(t, "acct-b")}))
}

// TestEndpoints_MatchTheEngine keeps the catalog's base URLs and key
// variables in step with the engine's provider table.
func TestEndpoints_MatchTheEngine(t *testing.T) {
	providers := embedded.DefaultProviders()
	require.Len(t, models.Endpoints, len(providers))
	for _, p := range providers {
		ep, ok := models.Endpoints[p.Name]
		require.True(t, ok, p.Name)
		assert.Equal(t, p.BaseURL, ep.BaseURL, p.Name)
		assert.Equal(t, p.APIKeyEnv, ep.KeyEnv, p.Name)
	}
}
