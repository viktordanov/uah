package embedded

import (
	"fmt"
	"slices"

	"github.com/viktordanov/uah-core/harness/llm"
	"github.com/viktordanov/uah-core/harness/tool"

	"github.com/viktordanov/uah/internal/mcp"
	"github.com/viktordanov/uah/internal/toolpolicy"
)

// The tool policy (Config.Tools) applies to every session on the engine,
// subagents' and forks' included, on top of a session's scope: the
// built-in tools it does not allow join the request's disallowed tools,
// which every part of the registry honors, its MCP tools are filtered by
// server, and policyRegistry, outside the PreToolUse hooks, refuses a call
// to any tool the run did not offer before a hook, an approval, or a job
// sees it.

// policyDisallow adds the built-in tools the policy does not allow to the
// request's disallowed tools.
func policyDisallow(p toolpolicy.Policy, disallowed []string) []string {
	for _, name := range toolpolicy.Builtins {
		if !p.Allows(name) && !slices.Contains(disallowed, name) {
			disallowed = append(disallowed, name)
		}
	}

	return disallowed
}

// policyMCPTools are the MCP tools the policy allows, matched by their
// server, so a pattern for one server never takes another's tool.
func policyMCPTools(p toolpolicy.Policy, tools []mcp.Tool) []mcp.Tool {
	if !p.Restricted() {
		return tools
	}

	return slices.DeleteFunc(slices.Clone(tools), func(t mcp.Tool) bool { return !p.AllowsTool(t.Name, t.Server) })
}

// policyRegistry offers only the tools the policy allows and refuses a call
// to any other name before it reaches the hooks, the approvals, or a job:
// a call the model was not offered, forced or made up. A name the registry
// resolves still resolves, so a session with past calls to it resumes.
type policyRegistry struct {
	tool.Registry

	policy toolpolicy.Policy
	// servers are the MCP tools' servers by name, for the policy's server
	// patterns.
	servers map[string]string
}

// withPolicy wraps the registry when the policy restricts anything.
func withPolicy(r tool.Registry, p toolpolicy.Policy, mcpTools []mcp.Tool) tool.Registry {
	if !p.Restricted() {
		return r
	}
	servers := map[string]string{}
	for _, t := range mcpTools {
		servers[t.Name] = t.Server
	}

	return policyRegistry{Registry: r, policy: p, servers: servers}
}

func (r policyRegistry) allows(name string) bool {
	return r.policy.AllowsTool(name, r.servers[name])
}

func (r policyRegistry) StaticDefinitions() []tool.Definition {
	return slices.DeleteFunc(r.Registry.StaticDefinitions(), func(d tool.Definition) bool { return !r.allows(d.Tool.Name) })
}

func (r policyRegistry) Resolve(name string) (tool.Translator, bool) {
	t, ok := r.Registry.Resolve(name)
	if !ok || r.offered(name) {
		return t, ok
	}

	why := "the run does not offer it"
	if !r.allows(name) {
		why = "the tool policy does not allow it"
	}

	return policyRefusal{Translator: t, name: name, why: why}, true
}

// offered reports whether the run offers the tool: the policy allows it
// and the registry defines it.
func (r policyRegistry) offered(name string) bool {
	return r.allows(name) && slices.ContainsFunc(r.Registry.StaticDefinitions(), func(d tool.Definition) bool { return d.Tool.Name == name })
}

// policyRefusal refuses a call the tool policy does not allow; the result
// of a past call still reads through the tool's own translator.
type policyRefusal struct {
	tool.Translator

	name, why string
}

func (t policyRefusal) Translate(tool.Context, llm.ToolCall) tool.CallStatus {
	return tool.ErrorStatus(fmt.Sprintf("tool %q is not available in this session: %s", t.name, t.why), 0)
}
