package embedded_test

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/engine/embedded"
	"github.com/viktordanov/uah/internal/hooks"
	"github.com/viktordanov/uah/internal/mcp"
	"github.com/viktordanov/uah/internal/sandbox"
	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/internal/toolbridge"
	"github.com/viktordanov/uah/internal/toolpolicy"
	"github.com/viktordanov/uah/testing/fakellm"
)

// bridgeEngine is an engine with the test MCP server and a full-access
// sandbox policy: the bridge needs the sandbox's script, which gives
// commands its token, as every session uah opens has.
func bridgeEngine(t *testing.T, e *env, change func(*embedded.Config)) *embedded.Engine {
	t.Helper()
	policy := sandbox.Policy{Mode: sandbox.FullAccess, Workspace: e.Workspace}
	getenv := func(k string) string { // commands find uah on PATH, as in a session
		if k == "PATH" {
			return os.Getenv("PATH")
		}

		return e.getenv(k)
	}
	cfg := embedded.Config{StateDir: e.StateDir, Provider: "openai", Getenv: getenv, MCP: mcpManager(t, e, mcp.ServerConfig{}),
		Sandbox: &policy, SandboxDir: filepath.Join(e.StateDir, "sandbox")}
	if change != nil {
		change(&cfg)
	}

	return embedded.New(cfg)
}

// bridgeDir runs one turn, so the session's tools and its bridge are set
// up, and returns the session's $TMPDIR, where the bridge listens, and a
// request sender with the session's token, as its commands have it.
func bridgeDir(t *testing.T, e *env, eng *embedded.Engine) (*session.Session, *events, string, func(toolbridge.Request) toolbridge.Response) {
	t.Helper()
	s, ev := e.open(t, eng, "")
	_, err := s.Submit("hello")
	require.NoError(t, err)
	ev.finished()
	dir := session.TempDir(filepath.Join(e.StateDir, "sessions"), s.ID())
	token := embedded.BridgeToken(eng, s.ID())
	do := func(req toolbridge.Request) toolbridge.Response {
		req.Token = token
		resp, err := toolbridge.Do(t.Context(), dir, req)
		require.NoError(t, err)

		return resp
	}

	return s, ev, dir, do
}

// TestBridge_CallsTheSessionsMCPTools: a command reaches the session's MCP
// tools through the socket in its $TMPDIR: it lists them, describes one,
// and calls it; an unknown tool and arguments that are not an object are
// refused; Bash's description names uah tool; the call reaches the
// session's stream; closing the session closes the bridge.
func TestBridge_CallsTheSessionsMCPTools(t *testing.T) {
	e := newEnv(t, fakellm.Reply{Text: "hi"})
	s, ev, dir, do := bridgeDir(t, e, bridgeEngine(t, e, nil))

	list := do(toolbridge.Request{Op: toolbridge.OpList})
	require.True(t, list.OK, list.Error)
	var names []string
	for _, tl := range list.Tools {
		names = append(names, tl.Name)
		assert.Nil(t, tl.InputSchema, "list leaves the schemas out")
	}
	assert.Contains(t, names, "mcp__test__echo")
	assert.Contains(t, names, mcp.ReadResourceTool)

	desc := do(toolbridge.Request{Op: toolbridge.OpDescribe, Name: "mcp__test__echo"})
	require.True(t, desc.OK, desc.Error)
	assert.Equal(t, "Echo the text.", desc.Tool.Description)
	assert.NotNil(t, desc.Tool.InputSchema)

	got := do(toolbridge.Request{Op: toolbridge.OpCall, Name: "mcp__test__echo", Arguments: json.RawMessage(`{"text":"bridged"}`)})
	require.True(t, got.OK, got.Error)
	assert.Contains(t, got.Text, "bridged")
	called := ev.until("ToolBridgeCalled", isA[engine.ToolBridgeCalled]).(engine.ToolBridgeCalled)
	assert.Equal(t, "mcp__test__echo", called.Name)
	assert.True(t, called.OK)

	unknown := do(toolbridge.Request{Op: toolbridge.OpCall, Name: "mcp__test__nope"})
	assert.False(t, unknown.OK)
	assert.Contains(t, unknown.Error, "uah tool list")
	bad := do(toolbridge.Request{Op: toolbridge.OpCall, Name: "mcp__test__echo", Arguments: json.RawMessage(`[1]`)})
	assert.Contains(t, bad.Error, "JSON object")

	if len(filepath.Join(dir, toolbridge.SocketName)) > 100 { // the test's state directory is long
		target, err := os.Readlink(filepath.Join(dir, toolbridge.SocketName))
		require.NoError(t, err)
		assert.True(t, strings.HasPrefix(target, "/tmp/"), "the short socket is under /tmp, not the inherited $TMPDIR: %s", target)
	}

	reqs := e.llm.Requests()
	assert.Contains(t, toolDescription(t, reqs[0], "Bash"), "`uah tool list`")

	stranger, err := toolbridge.Do(t.Context(), dir, toolbridge.Request{Op: toolbridge.OpList})
	require.NoError(t, err)
	assert.False(t, stranger.OK, "a request without the session's token is refused")
	assert.Contains(t, stranger.Error, toolbridge.TokenVar)

	require.NoError(t, s.Close())
	_, err = toolbridge.Do(t.Context(), dir, toolbridge.Request{Op: toolbridge.OpList})
	assert.ErrorIs(t, err, toolbridge.ErrNoSession)
	_, statErr := os.Lstat(filepath.Join(dir, toolbridge.SocketName))
	assert.True(t, os.IsNotExist(statErr), "the socket is removed")
}

// TestBridge_RefusesWhatTheModelWouldBeRefused: a PreToolUse hook that
// blocks the tool blocks a script's call too, and the tool policy leaves a
// denied tool out of the list and refuses its call.
func TestBridge_RefusesWhatTheModelWouldBeRefused(t *testing.T) {
	runner, err := hooks.New([]hooks.Hook{{Event: hooks.PreToolUse, Matcher: "mcp__test__echo", Command: "echo 'not from scripts' >&2; exit 2", Source: hooks.SourceUser}}, nil, "")
	require.NoError(t, err)
	e := newEnv(t, fakellm.Reply{Text: "hi"})
	_, _, _, do := bridgeDir(t, e, bridgeEngine(t, e, func(c *embedded.Config) { c.Hooks = runner }))
	blocked := do(toolbridge.Request{Op: toolbridge.OpCall, Name: "mcp__test__echo", Arguments: json.RawMessage(`{"text":"x"}`)})
	assert.False(t, blocked.OK)
	assert.Contains(t, blocked.Error, "not from scripts")

	e2 := newEnv(t, fakellm.Reply{Text: "hi"})
	_, _, _, do2 := bridgeDir(t, e2, bridgeEngine(t, e2, func(c *embedded.Config) { c.Tools = toolpolicy.Policy{Deny: []string{"mcp__test__echo"}} }))
	list := do2(toolbridge.Request{Op: toolbridge.OpList})
	for _, tl := range list.Tools {
		assert.NotEqual(t, "mcp__test__echo", tl.Name)
	}
	denied := do2(toolbridge.Request{Op: toolbridge.OpCall, Name: "mcp__test__echo", Arguments: json.RawMessage(`{}`)})
	assert.False(t, denied.OK)
}

// TestBridge_Off: with the bridge off, no MCP server, or no sandbox
// policy, there is no socket and Bash's description does not name uah
// tool.
func TestBridge_Off(t *testing.T) {
	e := newEnv(t, fakellm.Reply{Text: "hi"})
	_, _, dir, _ := bridgeDir(t, e, bridgeEngine(t, e, func(c *embedded.Config) { c.NoToolBridge = true }))
	_, err := toolbridge.Do(t.Context(), dir, toolbridge.Request{Op: toolbridge.OpList})
	assert.ErrorIs(t, err, toolbridge.ErrNoSession)
	assert.NotContains(t, toolDescription(t, e.llm.Requests()[0], "Bash"), "uah tool")

	plain := newEnv(t, fakellm.Reply{Text: "hi"})
	_, _, dir, _ = bridgeDir(t, plain, bridgeEngine(t, plain, func(c *embedded.Config) { c.MCP = nil }))
	_, err = toolbridge.Do(t.Context(), dir, toolbridge.Request{Op: toolbridge.OpList})
	assert.ErrorIs(t, err, toolbridge.ErrNoSession, "no MCP server, no bridge")

	bare := newEnv(t, fakellm.Reply{Text: "hi"})
	_, _, dir, _ = bridgeDir(t, bare, bare.withMCP(mcpManager(t, bare, mcp.ServerConfig{}), nil))
	_, err = toolbridge.Do(t.Context(), dir, toolbridge.Request{Op: toolbridge.OpList})
	assert.ErrorIs(t, err, toolbridge.ErrNoSession, "no sandbox policy, no bridge: commands would get no token")
}

// toolDescription is a tool's description in a request.
func toolDescription(t *testing.T, r fakellm.Request, name string) string {
	t.Helper()
	for _, d := range r.ToolDefs {
		var def struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		}
		require.NoError(t, json.Unmarshal(d, &def))
		if def.Name == name {
			return def.Description
		}
	}
	t.Fatalf("no tool %s in the request", name)

	return ""
}

// TestBridge_ResourceToolsOneByOne: a scope that offers read_mcp_resource
// alone offers the bridge that one, and refuses the listing tools.
func TestBridge_ResourceToolsOneByOne(t *testing.T) {
	e := newEnv(t, fakellm.Reply{Text: "hi"})
	_, _, _, do := bridgeDir(t, e, bridgeEngine(t, e, func(c *embedded.Config) {
		c.Tools = toolpolicy.Policy{Deny: []string{mcp.ListResourcesTool, mcp.ListResourceTemplatesTool}}
	}))
	var names []string
	for _, tl := range do(toolbridge.Request{Op: toolbridge.OpList}).Tools {
		names = append(names, tl.Name)
	}
	assert.Contains(t, names, mcp.ReadResourceTool)
	assert.NotContains(t, names, mcp.ListResourcesTool)
	refused := do(toolbridge.Request{Op: toolbridge.OpCall, Name: mcp.ListResourcesTool, Arguments: json.RawMessage(`{}`)})
	assert.False(t, refused.OK)
}

// TestBridge_CloseEndsAHalfSentRequest: a command that connects and never
// sends its request does not hold the session's close.
func TestBridge_CloseEndsAHalfSentRequest(t *testing.T) {
	e := newEnv(t, fakellm.Reply{Text: "hi"})
	s, _, dir, _ := bridgeDir(t, e, bridgeEngine(t, e, nil))
	wd, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	conn, err := net.Dial("unix", toolbridge.SocketName)
	require.NoError(t, os.Chdir(wd))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	closed := make(chan error, 1)
	go func() { closed <- s.Close() }()
	select {
	case err := <-closed:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("the close waited for the half-sent request")
	}
}

// TestBridge_ClosedWhenARunNoLongerOffersIt: a later run whose scope drops
// Bash closes the bridge an earlier run opened, so a command holding its
// token, such as a background shell, can call nothing.
func TestBridge_ClosedWhenARunNoLongerOffersIt(t *testing.T) {
	e := newEnv(t, fakellm.Reply{Text: "hi"}, fakellm.Reply{Text: "again"})
	eng := bridgeEngine(t, e, nil)
	s, ev, dir, do := bridgeDir(t, e, eng)
	require.True(t, do(toolbridge.Request{Op: toolbridge.OpList}).OK)

	eng.SetScope(s.ID(), engine.Scope{Tools: []string{"ViewImage"}})
	_, err := s.Submit("again")
	require.NoError(t, err)
	ev.finished()
	_, err = toolbridge.Do(t.Context(), dir, toolbridge.Request{Op: toolbridge.OpList})
	assert.ErrorIs(t, err, toolbridge.ErrNoSession)
}

// TestBridge_NotWithoutPATH: an environment policy that drops PATH leaves
// commands no way to find uah, so the bridge is not offered.
func TestBridge_NotWithoutPATH(t *testing.T) {
	e := newEnv(t, fakellm.Reply{Text: "hi"})
	_, _, dir, _ := bridgeDir(t, e, bridgeEngine(t, e, func(c *embedded.Config) { c.Env = sandbox.EnvPolicy{Inherit: sandbox.InheritNone} }))
	_, err := toolbridge.Do(t.Context(), dir, toolbridge.Request{Op: toolbridge.OpList})
	assert.ErrorIs(t, err, toolbridge.ErrNoSession)
	assert.NotContains(t, toolDescription(t, e.llm.Requests()[0], "Bash"), "uah tool")
}
