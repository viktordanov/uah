package render

import (
	"fmt"

	"github.com/charmbracelet/x/ansi"

	"github.com/viktordanov/uah/internal/mcp"
	"github.com/viktordanov/uah/internal/tui/state"
)

// mcpLines draws the /mcp panel as Codex's "MCP Tools" cell: a line per
// server with its state, transport, and tool count, then what to do about
// a failure. Verbose (/mcp verbose or the detailed view) adds the command
// or URL, an HTTP server's auth state, and each tool with its approval
// mode and description.
func (st *Styles) mcpLines(it state.Item, w int, details bool) []string {
	verbose := details || it.Final
	out := []string{"", st.bold.Render("• MCP servers")}
	for _, s := range it.MCP {
		out = append(out, ansi.Truncate(st.serverLine(s), w, "…"))
		if hint := serverHint(s); hint != "" {
			out = append(out, styleLines(wrapPrefixed(hint, w, "      ", "      "), st.stateStyle(s.State))...)
		}
		if !verbose {
			continue
		}
		if s.Transport == mcp.TransportHTTP {
			out = append(out, st.dimLine(w, "      url: "+s.Target), st.dimLine(w, "      auth: "+s.Auth.Text()))
		} else {
			out = append(out, st.dimLine(w, "      command: "+s.Target))
		}
		for _, t := range s.Tools {
			out = append(out, st.toolEntry(t, w))
		}
		for _, p := range s.Prompts {
			line := "      /" + p.Command
			if usage := p.Usage(); usage != "" {
				line += " " + usage
			}
			if p.Description != "" {
				line += st.dim.Render(" · " + oneLine(p.Description))
			}
			out = append(out, ansi.Truncate(line, w, "…"))
		}
		for _, r := range s.Resources {
			line := "      @" + s.Name + ":" + r.URI
			if r.Name != "" {
				line += st.dim.Render(" · " + oneLine(r.Name))
			}
			out = append(out, ansi.Truncate(line, w, "…"))
		}
	}
	if !verbose {
		out = append(out, st.dim.Render("  /mcp verbose (or ctrl+t) lists each tool and its approval mode, prompt, and resource"))
	}

	return out
}

// serverLine is "  • docs: ready · stdio · 3 tools · 1 prompt", or for a
// server that stopped, "restarting (2 of 5)".
func (st *Styles) serverLine(s mcp.ServerStatus) string {
	state := stateText(s.State)
	if s.State == mcp.StateRestarting {
		state += fmt.Sprintf(" (%d of %d)", s.Restarts, mcp.MaxRestarts)
	}
	line := fmt.Sprintf("  • %s: %s · %s", st.tool.Render(s.Name), st.stateStyle(s.State).Render(state), s.Transport)
	if s.State == mcp.StateReady || s.State == mcp.StateRestarting {
		line += " · " + count(len(s.Tools), "tool")
		if len(s.Prompts) > 0 {
			line += " · " + count(len(s.Prompts), "prompt")
		}
		if s.HasResources {
			line += " · resources"
		}
	}

	return line
}

func count(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}

	return fmt.Sprintf("%d %ss", n, what)
}

// serverHint says what went wrong and how to fix it.
func serverHint(s mcp.ServerStatus) string {
	switch s.State {
	case mcp.StateNeedsLogin:
		return "run `uah mcp login " + s.Name + "`; the next message or /mcp then reconnects it"
	case mcp.StateFailed, mcp.StateRestarting:
		return s.Error
	case mcp.StateStarting, mcp.StateReady, mcp.StateDisabled:
	}

	return ""
}

func stateText(st mcp.State) string {
	if st == mcp.StateNeedsLogin {
		return "needs login"
	}

	return string(st)
}

func (st *Styles) stateStyle(state mcp.State) interface{ Render(...string) string } {
	switch state {
	case mcp.StateReady:
		return st.ok
	case mcp.StateFailed:
		return st.bad
	case mcp.StateNeedsLogin, mcp.StateStarting, mcp.StateRestarting:
		return st.warn
	case mcp.StateDisabled:
	}

	return st.dim
}

// toolEntry is "      mcp__docs__search · approve · Search the docs.".
func (st *Styles) toolEntry(t mcp.Tool, w int) string {
	mode := string(t.Approval)
	if t.NeedsApproval() {
		mode += ", asks"
	}
	line := "      " + t.Name + st.dim.Render(" · "+mode)
	if t.Description != "" {
		line += st.dim.Render(" · " + oneLine(t.Description))
	}

	return ansi.Truncate(line, w, "…")
}

func (st *Styles) dimLine(w int, s string) string { return st.dim.Render(ansi.Truncate(s, w, "…")) }
