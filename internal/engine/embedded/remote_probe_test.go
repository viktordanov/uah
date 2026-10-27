//go:build probe

package embedded

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/viktordanov/uah-core/harness/llm/clients/openaicodex"

	"github.com/viktordanov/uah/internal/engine/codexauth"
)

// TestProbeRemoteCompaction asks the ChatGPT backend for Codex's remote
// compaction (a compaction_trigger input item, rust-v0.159.1
// compact_remote_v2_attempt.rs) and continues from the returned item. The
// fact to recall lives only in a tool output, which the continuation drops.
// Run it by hand:
//
//	go test -tags probe -run TestProbeRemoteCompaction -v ./internal/engine/embedded/
//
// UAH_PROBE_MODEL overrides the model (default gpt-6.1-sol).
func TestProbeRemoteCompaction(t *testing.T) {
	login, err := codexauth.Open(os.Getenv)
	require.NoError(t, err)
	hc := codexHTTPClient(nil, login, openaicodex.BaseURL, nil)
	model := os.Getenv("UAH_PROBE_MODEL")
	if model == "" {
		model = "gpt-6.1-sol"
	}
	user := probeMessage("user", "input_text", "Look up the deploy codeword.")
	history := []any{
		user,
		map[string]any{"type": "function_call", "call_id": "call_1", "name": "shell", "arguments": `{"command":"cat codeword.txt"}`},
		map[string]any{"type": "function_call_output", "call_id": "call_1", "output": "PAPAYA-4217"},
		probeMessage("assistant", "output_text", "I read the codeword file."),
	}
	res := probeRequest(t, hc, model, append(history, map[string]any{"type": "compaction_trigger"}))
	t.Logf("compact: status=%d items=%v usage=%s took=%s", res.status, res.types, res.usage, res.took)
	require.Equal(t, http.StatusOK, res.status, res.errBody)
	require.NotNil(t, res.compaction, "no compaction item")
	var item struct {
		EncryptedContent string `json:"encrypted_content"`
	}
	require.NoError(t, json.Unmarshal(res.compaction, &item))
	t.Logf("compaction item: %d bytes, encrypted_content %d bytes, keys=%s", len(res.compaction), len(item.EncryptedContent), probeKeys(res.compaction))

	next := []any{user, json.RawMessage(res.compaction), probeMessage("user", "input_text", "What is the deploy codeword? Answer with just the codeword.")}
	res = probeRequest(t, hc, model, next)
	t.Logf("continue: status=%d items=%v usage=%s took=%s answer=%q", res.status, res.types, res.usage, res.took, res.text)
	require.Equal(t, http.StatusOK, res.status, res.errBody)
}

func probeMessage(role, kind, text string) map[string]any {
	return map[string]any{"type": "message", "role": role, "content": []any{map[string]any{"type": kind, "text": text}}}
}

func probeKeys(raw json.RawMessage) string {
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}

	return strings.Join(keys, ",")
}

type probeResult struct {
	status     int
	errBody    string
	types      []string
	compaction json.RawMessage
	text       string
	usage      string
	took       time.Duration
}

func probeRequest(t *testing.T, hc *http.Client, model string, input []any) probeResult {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"model": model, "instructions": "You are a coding agent.", "input": input,
		"tools": []any{map[string]any{
			"type": "function", "name": "shell", "description": "Run a shell command.",
			"parameters": map[string]any{"type": "object", "properties": map[string]any{"command": map[string]any{"type": "string"}}, "required": []string{"command"}},
		}},
		"tool_choice": "auto", "parallel_tool_calls": true, "reasoning": map[string]any{"effort": "low", "summary": "auto"},
		"store": false, "stream": true, "include": []string{"reasoning.encrypted_content"}, "prompt_cache_key": uuid.NewString(),
	})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, openaicodex.BaseURL+"/responses", bytes.NewReader(body))
	require.NoError(t, err)
	req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	req.Header.Set(headerContentType, contentJSON)
	req.Header.Set("originator", "uah-core")
	req.Header.Set("User-Agent", "uah-core")
	if os.Getenv("UAH_PROBE_NOBETA") == "" {
		req.Header.Set("x-codex-beta-features", "remote_compaction_v2")
	}
	start := time.Now()
	resp, err := hc.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	res := probeResult{status: resp.StatusCode}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2000))
		res.errBody = string(b)

		return res
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 1<<20), 16<<20)
	for sc.Scan() {
		payload, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		var ev struct {
			Type     string          `json:"type"`
			Item     json.RawMessage `json:"item"`
			Delta    string          `json:"delta"`
			Response struct {
				Usage json.RawMessage `json:"usage"`
				Error json.RawMessage `json:"error"`
			} `json:"response"`
		}
		if json.Unmarshal([]byte(payload), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "response.output_item.done":
			var head struct {
				Type string `json:"type"`
			}
			_ = json.Unmarshal(ev.Item, &head)
			res.types = append(res.types, head.Type)
			if head.Type == "compaction" || head.Type == "compaction_summary" {
				res.compaction = ev.Item
			}
		case "response.output_text.delta":
			res.text += ev.Delta
		case "response.completed", "response.failed", "response.incomplete":
			res.usage = string(ev.Response.Usage) + string(ev.Response.Error)
		}
	}
	res.took = time.Since(start).Round(time.Millisecond)

	return res
}
