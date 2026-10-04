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
// server (mcpPolicy), and policyRegistry, outside the PreToolUse hooks, refuses a call
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

// mcpPolicy applies the policy to MCP tools by their server and tool
// (toolpolicy.Policy.AllowsMCP). Servers whose exposed names are the same
// (a-b and a.b are both a_b) cannot be told apart by any name, and the
// names of their tools can pass from one to the other as the tool lists
// change, so a policy allows none of their tools, and the resource tools
// reach neither.
type mcpPolicy struct {
	policy toolpolicy.Policy
	// shared are the exposed server names that more than one configured
	// server has.
	shared map[string]bool
}

func newMCPPolicy(p toolpolicy.Policy, servers []string) mcpPolicy {
	seen, shared := map[string]bool{}, map[string]bool{}
	for _, s := range servers {
		name := mcp.Sanitize(s)
		shared[name] = seen[name]
		seen[name] = true
	}

	return mcpPolicy{policy: p, shared: shared}
}

// allows reports whether the policy allows an MCP tool.
func (m mcpPolicy) allows(t mcp.Tool) bool {
	return !m.shared[mcp.Sanitize(t.Server)] && m.policy.AllowsMCP(t.Name, t.Server, t.Tool)
}

// allowsServer reports whether the resource tools may reach a server.
func (m mcpPolicy) allowsServer(server string) bool {
	return !m.shared[mcp.Sanitize(server)] && m.policy.AllowsServer(server)
}

// tools are the MCP tools the policy allows.
func (m mcpPolicy) tools(tools []mcp.Tool) []mcp.Tool {
	if !m.policy.Restricted() {
		return tools
	}

	return slices.DeleteFunc(slices.Clone(tools), func(t mcp.Tool) bool { return !m.allows(t) })
}

// policyRegistry offers only the tools the policy allows and refuses a call
// to any other name before it reaches the hooks, the approvals, or a job:
// a call the model was not offered, forced or made up. A name the registry
// resolves still resolves, so a session with past calls to it resumes.
type policyRegistry struct {
	tool.Registry

	policy toolpolicy.Policy
	// mcp are the run's MCP tools by name, each with whether the policy
	// allows it (mcpPolicy).
	mcp map[string]bool
}

// withPolicy wraps the registry when the policy restricts anything; tools
// are the run's MCP tools, all of them.
func withPolicy(r tool.Registry, m mcpPolicy, tools []mcp.Tool) tool.Registry {
	if !m.policy.Restricted() {
		return r
	}
	allowed := map[string]bool{}
	for _, t := range tools {
		allowed[t.Name] = m.allows(t)
	}

	return policyRegistry{Registry: r, policy: m.policy, mcp: allowed}
}

func (r policyRegistry) allows(name string) bool {
	if allowed, ok := r.mcp[name]; ok {
		return allowed
	}

	return r.policy.Allows(name)
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
		why = toolpolicy.Refused
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
