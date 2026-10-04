package state_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/mcp"
	"github.com/viktordanov/uah/internal/tui/state"
)

func TestReduce_MCP(t *testing.T) {
	s, effects := apply(opened(), state.Submit{Text: "/mcp"})
	assert.Equal(t, []state.Effect{state.EffListMCP{}}, effects)
	_, effects = apply(s, state.Submit{Text: "/mcp verbose"})
	assert.Equal(t, []state.Effect{state.EffListMCP{Verbose: true}}, effects)
	s, effects = apply(s, state.Submit{Text: "/mcp all"})
	assert.Empty(t, effects)
	assert.Equal(t, "Usage: /mcp [verbose]", s.Items[len(s.Items)-1].Text)

	servers := []mcp.ServerStatus{
		{Name: "docs", State: mcp.StateReady, Tools: []mcp.Tool{{Name: "mcp__docs__search"}, {Name: "mcp__docs__get"}}},
		{Name: "broken", State: mcp.StateFailed, Error: "did not start within 30s"},
	}
	s, _ = apply(s, state.MCPListed{Supported: true, Verbose: true, Servers: servers})
	last := s.Items[len(s.Items)-1]
	require.Equal(t, state.KindMCP, last.Kind)
	assert.Equal(t, servers, last.MCP)
	assert.True(t, last.Final, "verbose")

	s, _ = apply(s, state.MCPListed{Supported: false})
	assert.Equal(t, "MCP servers need the embedded engine", s.Items[len(s.Items)-1].Text)
	s, _ = apply(s, state.MCPListed{Supported: true})
	assert.Contains(t, s.Items[len(s.Items)-1].Text, "no MCP servers are configured")
}

var reviewPrompt = mcp.Prompt{
	Command: "mcp__docs__review", Server: "docs", Prompt: "review", Description: "Review a file.",
	Arguments: []mcp.PromptArgument{{Name: "file", Required: true}, {Name: "focus"}},
}

// MCP prompts are commands: "/" loads them each time it starts a draft,
// the menu offers them after the built-in ones, and running one gets the
// prompt with its arguments, quoted ones kept together.
func TestReduce_MCPPrompts(t *testing.T) {
	s, effects := apply(opened(), state.DraftChanged{Draft: "/"})
	require.Equal(t, []state.Effect{state.EffLoadMCPPrompts{}}, effects)
	_, effects = apply(s, state.DraftChanged{Draft: "/m"})
	assert.Empty(t, effects, "loading already")
	s, _ = apply(s, state.MCPPromptsLoaded{Prompts: []mcp.Prompt{reviewPrompt, {Command: "mcp__docs__hello", Server: "docs", Prompt: "hello"}}})
	_, effects = apply(s, state.DraftChanged{Draft: "/mcp__"})
	assert.Empty(t, effects, "loaded")
	_, effects = apply(s, state.DraftChanged{Draft: "/"})
	assert.Equal(t, []state.Effect{state.EffLoadMCPPrompts{}}, effects, "again for a new draft")

	items := s.Suggestions("/mc")
	assert.Equal(t, []string{"/mcp [verbose]", "/mcp__docs__review <file> [focus]", "/mcp__docs__hello"}, labels(items))
	assert.Equal(t, "Review a file.", items[1].Help)
	assert.Equal(t, "/mcp__docs__review ", items[1].Draft, "an argument to type")

	_, effects = apply(s, state.Submit{Text: `/mcp__docs__review "a b.go" errors`})
	assert.Equal(t, []state.Effect{state.EffRunPrompt{Prompt: reviewPrompt, Args: []string{"a b.go", "errors"}}}, effects)
	_, effects = apply(s, state.MenuEnter{Draft: "/mcp__docs__he"})
	assert.Equal(t, []state.Effect{state.EffSetDraft{Text: ""}, state.EffRunPrompt{Prompt: mcp.Prompt{Command: "mcp__docs__hello", Server: "docs", Prompt: "hello"}}}, effects,
		"enter runs a prompt without arguments")
	s, effects = apply(s, state.Submit{Text: "/mcp__docs__nope"})
	assert.Empty(t, effects)
	assert.Contains(t, s.Items[len(s.Items)-1].Text, "unknown command /mcp__docs__nope")
}

// After "@" the MCP resources come before the files; accepting one puts
// "@server:uri" in the draft, and never attaches it as an image file.
func TestMenu_MCPResources(t *testing.T) {
	s := opened()
	s, _ = apply(s, state.FilesLoaded{Paths: []string{"README.md", "logo.png"}})
	s, _ = apply(s, state.MCPResourcesLoaded{Resources: []mcp.ResourceRef{
		{Server: "docs", URI: "test://greeting", Name: "Greeting"},
		{Server: "docs", URI: "test://logo.png", Name: "Logo"},
	}})
	items := s.Suggestions("see @")
	assert.Equal(t, []string{"docs:test://greeting", "docs:test://logo.png", "README.md", "logo.png"}, labels(items))
	assert.Equal(t, "see @docs:test://greeting ", items[0].Draft)
	assert.Equal(t, "Greeting", items[0].Help)
	assert.Equal(t, "docs:test://greeting", s.Suggestions("see @greet")[0].Label)

	s, _ = apply(s, state.MenuMove{Draft: "see @", Delta: 1})
	_, effects := apply(s, state.MenuAccept{Draft: "see @"})
	assert.Equal(t, []state.Effect{state.EffSetDraft{Text: "see @docs:test://logo.png "}}, effects)
}
