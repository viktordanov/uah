package embedded

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/viktordanov/uah-core/harness/llm"
)

type markerClient struct {
	adapterFunc
}

func (markerClient) Close() error { return nil }

func TestReviewMarkers(t *testing.T) {
	for _, provider := range []string{"openai-codex", "openai", "openrouter", "fireworks", "ollama"} {
		for _, enabled := range []bool{false, true} {
			for _, kind := range []string{kindTurn, kindDirect, kindReview} {
				t.Run(provider+"/"+kind+"/"+map[bool]string{true: "on", false: "off"}[enabled], func(t *testing.T) {
					var built variant
					sw, err := newSwitcher("m", variant{priority: true}, 1, func(v variant) (Client, error) {
						built = v
						return markerClient{adapterFunc(func(ctx context.Context, _ llm.Request, _ llm.RequestOptions) (llm.Response, error) {
							c, ok := ctx.Value(callKey{}).(*modelCall)
							require.True(t, ok)
							req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://localhost/responses",
								strings.NewReader(`{"service_tier":"priority","client_metadata":{"existing":"kept"}}`))
							require.NoError(t, err)
							req.Header.Set("originator", "uah-core")
							got, err := c.rewriteBody(req)
							require.NoError(t, err)
							defer got.Body.Close()
							body, err := io.ReadAll(got.Body)
							require.NoError(t, err)
							var fields map[string]any
							require.NoError(t, json.Unmarshal(body, &fields))
							metadata, ok := fields["client_metadata"].(map[string]any)
							require.True(t, ok)
							assert.Equal(t, "kept", metadata["existing"])
							assert.Equal(t, "uah-core", got.Header.Get("originator"))
							codex := enabled && provider == "openai-codex"
							if kind == kindReview {
								assert.NotContains(t, fields, "service_tier")
								assert.Equal(t, "guardian", got.Header.Get("x-openai-subagent"))
								assert.Equal(t, "guardian", metadata["x-openai-subagent"])
								assert.NotContains(t, metadata, "guardian_credits_requested")
							} else {
								assert.Equal(t, "priority", fields["service_tier"])
								assert.Empty(t, got.Header.Get("x-openai-subagent"))
								assert.NotContains(t, metadata, "x-openai-subagent")
							}
							if codex && kind == kindReview {
								assert.Equal(t, "reviewer", got.Header.Get("x-codex-guardian"))
								assert.Equal(t, "latest", metadata["parent_response_id"])
							} else {
								assert.Empty(t, got.Header.Get("x-codex-guardian"))
								assert.NotContains(t, metadata, "parent_response_id")
							}
							if codex && kind != kindReview {
								assert.Equal(t, "true", metadata["guardian_credits_requested"])
							} else {
								assert.NotContains(t, metadata, "guardian_credits_requested")
							}
							c.line([]byte(`data: {"type":"response.created","response":{"id":"next"}}`))
							return llm.Response{}, nil
						})}, nil
					})
					require.NoError(t, err)
					sw.guardianMarkers = guardianMarkers(provider, enabled)
					id := "old"
					sw.latestResponse.Store(&id)
					ctx, done := sw.observe(t.Context(), kindTurn)
					call, ok := ctx.Value(callKey{}).(*modelCall)
					require.True(t, ok)
					call.line([]byte(`data: {"type":"response.created","response":{"id":"latest"}}`))
					require.NoError(t, done(nil))
					var adapter llm.Adapter = sw
					if kind != kindTurn {
						adapter = sw.directKind(kind)
					}
					_, err = adapter.Respond(t.Context(), llm.Request{}, llm.RequestOptions{})
					require.NoError(t, err)
					assert.Equal(t, kind != kindReview, built.priority)
					want := "next"
					if kind == kindReview {
						want = "latest"
					}
					assert.Equal(t, want, *sw.latestResponse.Load())
					require.NoError(t, sw.Close())
				})
			}
		}
	}
}
