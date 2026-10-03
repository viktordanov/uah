package state

import (
	"cmp"
	"strings"

	"github.com/sahilm/fuzzy"

	"github.com/viktordanov/uah/internal/mcp"
	"github.com/viktordanov/uah/internal/session"
)

// EffListMCP asks the session for its MCP servers.
type EffListMCP struct {
	Verbose bool
}

// EffLoadMCPPrompts asks for the MCP servers' prompts, which the "/" menu
// offers as commands.
type EffLoadMCPPrompts struct{}

// EffLoadMCPResources asks for the MCP servers' resources, which the "@" menu
// offers beside the files.
type EffLoadMCPResources struct{}

// EffRunPrompt gets an MCP prompt with the arguments typed after its
// command and sends the result as a message, as Claude Code runs
// /mcp__<server>__<prompt>.
type EffRunPrompt struct {
	Prompt mcp.Prompt
	Args   []string
}

func (EffListMCP) effect()          {}
func (EffLoadMCPPrompts) effect()   {}
func (EffLoadMCPResources) effect() {}
func (EffRunPrompt) effect()        {}

// MCPPromptsLoaded carries the MCP prompts for "/".
type MCPPromptsLoaded struct{ Prompts []mcp.Prompt }

// MCPResourcesLoaded carries the MCP resources for "@".
type MCPResourcesLoaded struct{ Resources []mcp.ResourceRef }

// MCPListed reports the MCP servers for /mcp. Supported is false when the
// engine does not run MCP servers; Verbose lists every tool.
type MCPListed struct {
	Servers   []mcp.ServerStatus
	Supported bool
	Verbose   bool
}

// cmdMCP is Codex's /mcp: a line per server, and with "verbose" its
// transport, auth, and tools.
func cmdMCP(s *State, args string) []Effect {
	switch args {
	case "":
		return []Effect{EffListMCP{}}
	case "verbose":
		return []Effect{EffListMCP{Verbose: true}}
	}
	s.notice(session.LevelWarning, "Usage: /mcp [verbose]")

	return nil
}

// showMCP adds the MCP panel: a KindMCP item holding the servers, drawn
// compact unless Final (verbose) or the detailed view is on.
func (s *State) showMCP(e MCPListed) {
	switch {
	case !e.Supported:
		s.notice(session.LevelWarning, "MCP servers need the embedded engine")

		return
	case len(e.Servers) == 0:
		s.notice(session.LevelInfo, "no MCP servers are configured; add one with `uah mcp add` or [mcp_servers.<name>] in the configuration")

		return
	}
	s.put(Item{Kind: KindMCP, Key: s.nextKey("mcp"), MCP: e.Servers, Final: e.Verbose})
}

// findPrompt returns the MCP prompt a command names.
func (s *State) findPrompt(name string) (mcp.Prompt, bool) {
	for _, p := range s.Menu.Prompts {
		if p.Command == name {
			return p, true
		}
	}

	return mcp.Prompt{}, false
}

// runPrompt runs an MCP prompt's command: its result goes out as a
// message, queued while the agent works, as a typed message would.
func (s *State) runPrompt(p mcp.Prompt, args string) []Effect {
	s.Scroll = 0

	return []Effect{EffRunPrompt{Prompt: p, Args: mcp.SplitArgs(args)}}
}

// promptSuggestions are the MCP prompts whose command starts with prefix.
func (s State) promptSuggestions(prefix string) []Suggestion {
	var out []Suggestion
	for _, p := range s.Menu.Prompts {
		if !strings.HasPrefix(p.Command, prefix) {
			continue
		}
		label, draft := "/"+p.Command, "/"+p.Command
		if usage := p.Usage(); usage != "" {
			label += " " + usage
			draft += " "
		}
		out = append(out, Suggestion{Label: label, Help: cmp.Or(p.Description, "MCP prompt of "+p.Server), Draft: draft})
	}

	return out
}

// resourceSuggestions are the MCP resources for an "@" word: all of them
// before the files when nothing is typed yet, else those the query
// matches. Accepting one puts "@server:uri" in the draft.
func (s State) resourceSuggestions(draft string, at int) []Suggestion {
	query := draft[at+1:]
	labels := make([]string, len(s.Menu.Resources))
	for i, r := range s.Menu.Resources {
		labels[i] = r.Server + ":" + r.URI
	}
	pick := func(i int) Suggestion {
		r := s.Menu.Resources[i]
		return Suggestion{Label: labels[i], Help: cmp.Or(r.Name, r.Description), Draft: draft[:at] + "@" + labels[i] + " ", Resource: true}
	}
	var out []Suggestion
	if query == "" {
		for i := range labels {
			out = append(out, pick(i))
		}

		return out
	}
	for _, m := range fuzzy.Find(query, labels) {
		out = append(out, pick(m.Index))
	}

	return out
}
