package main_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/testing/fakellm"
)

// callerServer is a streamable HTTP MCP server with a read tool that
// answers with the caller identity it received, and a delete tool that
// records each call it gets.
type callerServer struct {
	URL string

	mu      sync.Mutex
	deletes int
}

func newCallerServer(t *testing.T) *callerServer {
	t.Helper()
	c := &callerServer{}
	server := sdk.NewServer(&sdk.Implementation{Name: "caller", Version: "1"}, nil)
	object := map[string]any{"type": "object"}
	server.AddTool(&sdk.Tool{Name: "whoami", InputSchema: object, Annotations: &sdk.ToolAnnotations{ReadOnlyHint: true}},
		func(_ context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			text := "auth=" + req.Extra.Header.Get("Authorization") + " caller=" + req.Extra.Header.Get("X-Caller")
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: text}}}, nil
		})
	server.AddTool(&sdk.Tool{Name: "delete", InputSchema: object, Annotations: &sdk.ToolAnnotations{ReadOnlyHint: true}},
		func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			c.mu.Lock()
			defer c.mu.Unlock()
			c.deletes++

			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "deleted"}}}, nil
		})
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c.URL = srv.URL

	return c
}

// quickQueryEnv is a home whose configuration names the caller server,
// with the caller's identity in environment variables, and a fake model.
func quickQueryEnv(t *testing.T, llm *fakellm.Server, srv *callerServer) (workspace string, env []string) {
	t.Helper()
	e, env := modelEnv(t, llm)
	home := filepath.Join(e.StateDir, "..", "home")
	require.NoError(t, os.MkdirAll(home, 0o700))
	cfg := "[mcp_servers.qq]\nurl = \"" + srv.URL + "\"\nbearer_token_env_var = \"QQ_TOKEN\"\nenv_http_headers = { \"X-Caller\" = \"QQ_CALLER\" }\n"
	require.NoError(t, os.WriteFile(filepath.Join(home, "config.toml"), []byte(cfg), 0o600))

	return e.Workspace, append(env, "OPENAI_API_KEY=test-key", "QQ_TOKEN=caller-token", "QQ_CALLER=alice")
}

// TestExecToolPolicyQuickQuery is a headless, read-only query that may use
// only one caller-scoped MCP read tool: the model is offered that tool and
// nothing else, the tool reaches the server with the caller's identity,
// forced calls to anything else are refused before they reach a server or
// a shell, and --json, the model, and the effort work as without a policy.
func TestExecToolPolicyQuickQuery(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	llm := fakellm.New(t,
		fakellm.Reply{Calls: []fakellm.Call{
			{Name: "mcp__qq__whoami", Args: `{}`},
			{Name: "mcp__qq__delete", Args: `{}`},
			{Name: "Bash", Args: `{"command":"touch ` + filepath.Join(dir, "ran") + `"}`},
			{Name: "spawn_agent", Args: `{"message":"help"}`},
		}},
		fakellm.Reply{Text: "you are alice"},
		fakellm.Reply{Calls: []fakellm.Call{{Name: "mcp__qq__whoami", Args: `{}`}}},
		fakellm.Reply{Text: "still alice"},
	)
	srv := newCallerServer(t)
	ws, env := quickQueryEnv(t, llm, srv)

	res := uahWith(t, env, "", "exec", "--json", "--ephemeral", "--provider", "openai", "-m", "gpt-test", "-e", "low",
		"--tools", "mcp__qq__whoami", "--no-skills", "-C", ws, "who am I?")

	require.Equal(t, 0, res.code, res.stderr)
	reqs := llm.Requests()
	require.Len(t, reqs, 2)
	assert.Equal(t, []string{"mcp__qq__whoami"}, reqs[0].ToolNames, "one tool, and no hosted search")
	assert.Equal(t, "gpt-test", reqs[0].Model)
	assert.Equal(t, "low", reqs[0].Effort)
	outputs := strings.Join(reqs[1].ToolOutputs, "\n")
	assert.Contains(t, outputs, "auth=Bearer caller-token caller=alice", "the caller's identity reached the server")
	assert.Contains(t, outputs, `tool "mcp__qq__delete" is not available in this session: the tool policy does not allow it`)
	assert.Contains(t, outputs, `tool "Bash" is not available`)
	assert.Contains(t, outputs, `tool "spawn_agent" is not available`)
	assert.NoFileExists(t, filepath.Join(dir, "ran"))
	srv.mu.Lock()
	assert.Zero(t, srv.deletes, "the refused call never reached the server")
	srv.mu.Unlock()

	var types []string
	for line := range strings.Lines(strings.TrimSpace(res.stdout)) {
		var ev struct {
			Type string `json:"type"`
		}
		require.NoError(t, json.Unmarshal([]byte(line), &ev), line)
		types = append(types, ev.Type)
	}
	assert.Equal(t, "session_opened", types[0])
	assert.Contains(t, res.stdout, "you are alice")

	res = uahWith(t, env, "", "exec", "--ephemeral", "--provider", "openai", "-m", "gpt-test", "--tools", "mcp__qq__whoami", "-C", ws, "again")
	require.Equal(t, 0, res.code, res.stderr)
	assert.Equal(t, "still alice\n", res.stdout)
	assert.Contains(t, res.stderr, "mcp__qq__whoami", "the progress shows the call")
	assert.Contains(t, res.stderr, "run ok")
}

// TestExecToolPolicyFlags: a name uah does not know is a usage error, and
// an empty --tools offers no tools at all.
func TestExecToolPolicyFlags(t *testing.T) {
	t.Parallel()
	llm := fakellm.New(t, fakellm.Reply{Text: "no tools"})
	ws, env := quickQueryEnv(t, llm, newCallerServer(t))

	res := uahWith(t, env, "", "exec", "--provider", "openai", "-m", "gpt-test", "--deny-tools", "bash", "-C", ws, "hi")
	assert.Equal(t, 2, res.code)
	assert.Contains(t, res.stderr, `unknown tool "bash"`)

	res = uahWith(t, env, "", "exec", "-q", "--provider", "openai", "-m", "gpt-test", "--tools", "", "-C", ws, "hi")
	require.Equal(t, 0, res.code, res.stderr)
	assert.Equal(t, "no tools\n", res.stdout)
	assert.Empty(t, lastRequest(t, llm).ToolDefs)
}

// TestExecToolPolicyCancel: an interrupt stops a run under a policy as it
// stops any other.
func TestExecToolPolicyCancel(t *testing.T) {
	t.Parallel()
	gate := make(chan struct{})
	t.Cleanup(func() { close(gate) })
	llm := fakellm.New(t, fakellm.Reply{Gate: gate, Text: "never"})
	ws, env := quickQueryEnv(t, llm, newCallerServer(t))

	cmd := exec.Command(uahBin, "exec", "--provider", "openai", "-m", "gpt-test", "--tools", "mcp__qq__whoami", "-C", ws, "hi")
	cmd.Env = append(os.Environ(), env...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	require.NoError(t, cmd.Start())
	require.Eventually(t, func() bool { return len(llm.Arrivals()) > 0 }, 30*time.Second, 20*time.Millisecond, "the model request went out")
	require.NoError(t, cmd.Process.Signal(syscall.SIGINT))
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		require.Error(t, err, "an interrupted run fails")
		assert.NotContains(t, stderr.String(), "never")
	case <-time.After(30 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("the interrupt did not stop the run")
	}
}
