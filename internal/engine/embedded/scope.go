package embedded

import (
	"context"
	"slices"
	"strings"

	"github.com/viktordanov/uah-core/harness/tool"

	"github.com/viktordanov/uah/internal/approval"
	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/mcp"
	"github.com/viktordanov/uah/internal/patch"
	"github.com/viktordanov/uah/internal/rules"
)

var _ engine.Scoper = (*Engine)(nil)

// builtinTools are the tools a scope can leave out through the request's
// disallowed tools; MCP tools are filtered by name.
var builtinTools = []string{tool.BashName, tool.ViewImageName, tool.SkillUseName, patch.ToolName, engine.QuestionToolName}

// SetScope narrows a session's tools and pre-approves some of its actions
// from its next run (engine.Scoper).
func (e *Engine) SetScope(sessionID string, s engine.Scope) {
	if s.Tools == nil && len(s.Approve) == 0 && !s.NeverAsk {
		e.scopes.Delete(sessionID)

		return
	}
	e.scopes.Store(sessionID, newScope(s))
}

// scope is the session's scope, nil when it has none.
func (e *Engine) scope(sessionID string) *scope {
	v, _ := e.scopes.Load(sessionID)
	s, _ := v.(*scope)

	return s
}

// scope is an engine.Scope ready to apply: the pre-approved commands as
// allow rules.
type scope struct {
	engine.Scope

	commands *rules.Policy
}

func newScope(s engine.Scope) *scope {
	var prefixes []string
	for _, a := range s.Approve {
		if !strings.HasPrefix(a, mcp.Prefix) {
			prefixes = append(prefixes, a)
		}
	}
	var allow []rules.Rule
	for _, p := range prefixes { // one at a time, so a bad prefix drops only itself
		if r, err := rules.FromPrefixes([]string{p}, rules.Allow, "agent"); err == nil {
			allow = append(allow, r...)
		}
	}

	return &scope{Scope: s, commands: rules.New(allow...)}
}

// offers reports whether the scope offers the tool.
func (s *scope) offers(name string) bool {
	return s == nil || s.Tools == nil || matchTool(s.Tools, name)
}

// disallow adds the built-in tools the scope does not offer to the
// request's disallowed tools.
func (s *scope) disallow(disallowed []string) []string {
	for _, name := range builtinTools {
		if !s.offers(name) && !slices.Contains(disallowed, name) {
			disallowed = append(disallowed, name)
		}
	}

	return disallowed
}

// disallowResources adds the MCP resource tools the scope does not offer
// to the request's disallowed tools.
func (s *scope) disallowResources(disallowed []string) []string {
	for _, name := range []string{mcp.ListResourcesTool, mcp.ListResourceTemplatesTool, mcp.ReadResourceTool} {
		if !s.offers(name) && !slices.Contains(disallowed, name) {
			disallowed = append(disallowed, name)
		}
	}

	return disallowed
}

// mcpTools are the MCP tools the scope offers.
func (s *scope) mcpTools(tools []mcp.Tool) []mcp.Tool {
	if s == nil || s.Tools == nil {
		return tools
	}

	return slices.DeleteFunc(slices.Clone(tools), func(t mcp.Tool) bool { return !s.offers(t.Name) })
}

// approvesTool reports whether the scope approved the MCP tool in advance;
// the MCP gate then runs it as with approval_mode approve.
func (s *scope) approvesTool(name string) bool {
	return s != nil && matchTool(s.Approve, name)
}

// approves reports whether a command or a patch is approved in advance:
// each of its simple commands starts with an approved prefix. The
// prefixes are the approver's kind of prefix rule, matched the same way,
// but they answer the prompt rather than allow: an allow rule runs a
// command outside the sandbox, and a scope never widens the mode.
func (s *scope) approves(p approval.Prompt) bool {
	if p.MCPTool != "" {
		return false // the MCP gate applies the scope before it asks
	}
	commands, ok := rules.Split(p.Command)
	if !ok {
		return false
	}
	r, matched := s.commands.Check(commands)

	return matched && r.Decision == rules.Allow
}

// ask answers what the scope approves in advance and passes the rest on.
// An escalation in read only mode is still asked, so read only stays read
// only. The rules and the approval policy decide before any ask, so a
// forbid rule and the policy never still hold.
func (s *scope) ask(next approval.Ask, mode func() approval.Mode) approval.Ask {
	return func(ctx context.Context, p approval.Prompt) approval.Answer {
		if s.approves(p) && (!p.Escalation || mode() != approval.ModeReadOnly) {
			return approval.Approve
		}
		if next == nil {
			return approval.DeclineBecause("no user can approve it in this headless run.")
		}

		return next(ctx, p)
	}
}

// neverAsk declines what would ask: the scope's session has no one to
// approve anything (engine.Scope.NeverAsk).
func neverAsk(context.Context, approval.Prompt) approval.Answer {
	return approval.DeclineBecause("this session never asks for approval; stay within the sandbox.")
}

// matchTool reports whether a tool name is in the list: by its name, or,
// for an MCP tool, by its server's mcp__<server> or mcp__<server>__*.
func matchTool(list []string, name string) bool {
	for _, n := range list {
		server := strings.TrimSuffix(strings.TrimSuffix(n, "*"), "__")
		switch {
		case n == name:
			return true
		case strings.HasPrefix(n, mcp.Prefix) && strings.Count(server, "__") == 1 && strings.HasPrefix(name, server+"__"):
			return true
		}
	}

	return false
}
