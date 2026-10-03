package render_test

import (
	"testing"

	"github.com/viktordanov/uah/internal/mcp"
	"github.com/viktordanov/uah/internal/tui/state"
)

func mcpServers() []mcp.ServerStatus {
	return []mcp.ServerStatus{
		{Name: "docs", State: mcp.StateReady, Transport: mcp.TransportStdio, Target: "npx -y docs-mcp", Auth: mcp.AuthUnsupported, Tools: []mcp.Tool{
			{Name: "mcp__docs__get", Description: "Get a page.", Approval: mcp.ApprovalAuto, ReadOnly: true},
			{Name: "mcp__docs__write", Description: "Write a page.", Approval: mcp.ApprovalWrites},
		}, Prompts: []mcp.Prompt{
			{Command: "mcp__docs__review", Description: "Review a page.", Arguments: []mcp.PromptArgument{{Name: "page", Required: true}}},
		}, HasResources: true, Resources: []mcp.ResourceRef{{Server: "docs", URI: "docs://index", Name: "Index"}}},
		{Name: "flaky", State: mcp.StateRestarting, Restarts: 2, Transport: mcp.TransportStdio, Target: "flaky", Error: "the server stopped: EOF", Tools: []mcp.Tool{
			{Name: "mcp__flaky__ping", Approval: mcp.ApprovalAuto},
		}},
		{Name: "tracker", State: mcp.StateNeedsLogin, Transport: mcp.TransportHTTP, Target: "https://mcp.example.com/mcp", Auth: mcp.AuthNotLoggedIn},
		{Name: "broken", State: mcp.StateFailed, Transport: mcp.TransportStdio, Target: "missing", Error: "failed to connect: exec: \"missing\": not found"},
		{Name: "off", State: mcp.StateDisabled, Transport: mcp.TransportStdio, Target: "off"},
	}
}

// TestScreen_MCP draws /mcp compact, and /mcp verbose with each tool's
// approval mode, each prompt, and each resource; a server that stopped
// shows its restart.
func TestScreen_MCP(t *testing.T) {
	s := apply(base(), state.MCPListed{Supported: true, Servers: mcpServers()})
	golden(t, "mcp", screen(s, ""))
	s = apply(base(), state.MCPListed{Supported: true, Verbose: true, Servers: mcpServers()})
	golden(t, "mcp-verbose", screen(s, ""))
}
