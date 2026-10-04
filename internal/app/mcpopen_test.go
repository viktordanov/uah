package app_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/app"
	"github.com/viktordanov/uah/internal/mcp"
	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/testing/fakellm"
	"github.com/viktordanov/uah/testing/harnesstest"
)

// TestInteractiveSession_MCPConnectsAtOpen pins that an interactive session
// connects its MCP servers as it opens, before any message or model
// request, so a host that waits for a server's initialize can start uah;
// that runs and /clear use that connection; and that /new and resuming
// another session, which set the session up again, reconnect once after
// the old connection closed.
func TestInteractiveSession_MCPConnectsAtOpen(t *testing.T) {
	e, in := setupEnv(t)
	log := filepath.Join(e.StateDir, "mcp.log")
	writeConfig(t, &in, "[mcp_servers.test]\ncommand = \""+harnesstest.MCPServer(t)+"\"\nenv = { MCPSERVER_LOG = \""+log+"\", MCPSERVER_LEGACY = \"1\" }\n")
	echo := func(text string) fakellm.Reply {
		return fakellm.Reply{Calls: []fakellm.Call{{Name: "mcp__test__echo", Args: `{"text":"` + text + `"}`}}}
	}
	llm := fakellm.New(t, echo("first"), fakellm.Reply{Text: "one"}, echo("second"), fakellm.Reply{Text: "two"})
	in.Provider, in.Model, in.BaseURL = "openai", "gpt-test", llm.URL
	t.Setenv("OPENAI_API_KEY", "test-key")

	s, started, _ := openInteractive(t, in)
	require.Len(t, started.Servers, 1)
	assert.Equal(t, mcp.StateReady, started.Servers[0].State)
	assert.Equal(t, handshake, methods(t, log), "connected at open")
	assert.Empty(t, llm.Requests(), "no model request")

	_, err := s.Submit("echo first")
	require.NoError(t, err)
	waitFinished(t, s)
	require.NoError(t, s.Clear())
	_, err = s.Submit("echo second")
	require.NoError(t, err)
	waitFinished(t, s)
	reqs := llm.Requests()
	require.Len(t, reqs, 4)
	assert.Contains(t, strings.Join(reqs[1].ToolOutputs, "\n"), "echo: first")
	assert.Contains(t, strings.Join(reqs[3].ToolOutputs, "\n"), "echo: second")
	assert.Equal(t, append(slices.Clone(handshake), "tools/call", "tools/call"), methods(t, log),
		"the turns and /clear used the connection from the open")

	// /new: the TUI closes the session, then sets up and opens a new one.
	first := s.ID()
	require.NoError(t, s.Close())
	s, started, _ = openInteractive(t, in)
	assert.Equal(t, mcp.StateReady, started.Servers[0].State)
	lines := logLines(t, log)
	assert.Equal(t, 2, count(lines, "initialize"), "one reconnection")
	closed := slices.Index(lines, pidOf(lines, 0)+" closed")
	require.GreaterOrEqual(t, closed, 0, "the old connection closed")
	assert.Equal(t, handshake, methodsOf(lines[closed+1:]), "then the new one opened")

	// Resuming the first session connects at open too.
	require.NoError(t, s.Close())
	in.SessionRef = first
	s, started, _ = openInteractive(t, in)
	assert.Equal(t, first, s.ID())
	assert.Equal(t, mcp.StateReady, started.Servers[0].State)
	assert.Equal(t, 3, count(logLines(t, log), "initialize"))
	assert.Len(t, llm.Requests(), 4, "still no model request")
	require.NoError(t, s.Close())
}

// TestInteractiveSession_MCPFailuresAtOpen pins that a server that cannot
// start is reported as the session opens, an error for a required server
// and a warning otherwise, while the others connect.
func TestInteractiveSession_MCPFailuresAtOpen(t *testing.T) {
	e, in := setupEnv(t)
	missing := filepath.Join(e.StateDir, "no-such-server")
	writeConfig(t, &in, "[mcp_servers.test]\ncommand = \""+harnesstest.MCPServer(t)+"\"\n"+
		"[mcp_servers.needed]\ncommand = \""+missing+"\"\nrequired = true\n"+
		"[mcp_servers.optional]\ncommand = \""+missing+"\"\n")

	s, started, notices := openInteractive(t, in)
	t.Cleanup(func() { _ = s.Close() })
	states := map[string]mcp.State{}
	for _, sv := range started.Servers {
		states[sv.Name] = sv.State
	}
	assert.Equal(t, map[string]mcp.State{"needed": mcp.StateFailed, "optional": mcp.StateFailed, "test": mcp.StateReady}, states)
	require.Len(t, notices, 2)
	assert.Equal(t, session.LevelError, notices[0].Level)
	assert.Contains(t, notices[0].Message, "the required MCP server needed did not start, so messages fail")
	assert.Equal(t, session.LevelWarning, notices[1].Level)
	assert.Contains(t, notices[1].Message, "MCP server optional did not start")
}

// handshake is what a client sends a server from before the 2026-07-28
// protocol (MCPSERVER_LEGACY) as it connects, as a host's server sees it:
// the tools, then the prompts the test server offers.
var handshake = []string{"server/discover", "initialize", "notifications/initialized", "tools/list", "prompts/list"}

// openInteractive sets up and opens a session as the TUI does, and waits
// until its MCP servers have started, with the notices shown by then.
func openInteractive(t *testing.T, in app.Inputs) (*session.Session, session.MCPStarted, []session.Notice) {
	t.Helper()
	res, err := app.Setup(context.Background(), in, io.Discard)
	require.NoError(t, err)
	opts := res.Options
	opts.Source, opts.Interactive, opts.Stream = session.SourceTUI, true, true
	s, err := session.Open(context.Background(), res.Engine, opts)
	require.NoError(t, err)
	var notices []session.Notice
	deadline := time.After(30 * time.Second)
	for {
		select {
		case ev := <-s.Events():
			switch e := ev.(type) {
			case session.Notice:
				notices = append(notices, e)
			case session.MCPStarted:
				return s, e, notices
			}
		case <-deadline:
			t.Fatal("the MCP servers did not start")
		}
	}
}

// logLines reads the test MCP server's log (MCPSERVER_LOG).
func logLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)

	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

// methods are the logged methods without the process IDs.
func methods(t *testing.T, path string) []string {
	t.Helper()

	return methodsOf(logLines(t, path))
}

func methodsOf(lines []string) []string {
	var out []string
	for _, l := range lines {
		_, m, _ := strings.Cut(l, " ")
		out = append(out, m)
	}

	return out
}

func count(lines []string, method string) int {
	n := 0
	for _, l := range lines {
		if strings.HasSuffix(l, " "+method) {
			n++
		}
	}

	return n
}

// pidOf is the process ID on the line.
func pidOf(lines []string, i int) string {
	pid, _, _ := strings.Cut(lines[i], " ")

	return pid
}
