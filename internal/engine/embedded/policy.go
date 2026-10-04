package embedded

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/viktordanov/uah-core/harness/llm"
	"github.com/viktordanov/uah-core/harness/operation"
	"github.com/viktordanov/uah-core/harness/tool"

	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/mcp"
	"github.com/viktordanov/uah/internal/patch"
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
// request's disallowed tools, but for the runner's own (Bash, ViewImage,
// SkillUse): the runner registers no translator for a disallowed tool,
// and a resumed session's recorded calls need theirs to load, so those
// stay registered and policyRegistry hides and refuses them.
func policyDisallow(p toolpolicy.Policy, disallowed []string) []string {
	for _, name := range toolpolicy.Builtins {
		if slices.Contains(runnerTools, name) {
			continue
		}
		if !p.Allows(name) && !slices.Contains(disallowed, name) {
			disallowed = append(disallowed, name)
		}
	}

	return disallowed
}

// mcpPolicy applies the policy to MCP tools under a restriction. It
// authorizes a tool by its identity, the configured server and the
// server's own name for it (toolpolicy.Policy.AllowsMCP), never by the
// name the model sees. And it offers no tool whose exposed name is not
// unambiguously its own, since mcp's namer would otherwise hash one side
// of a collision and could hand the plain name to the other side once the
// tool lists change: a tool whose name was hashed or shared with another
// tool, a tool of a server whose exposed name another configured server
// shares, and a tool of a server whose exposed name has "__". Each one
// left out is reported once per run.
type mcpPolicy struct {
	policy toolpolicy.Policy
	// shared are the exposed server names that more than one configured
	// server has, or that have "__".
	shared map[string]bool
	// warn reports a tool left out (nil: none).
	warn func(string)
}

func newMCPPolicy(p toolpolicy.Policy, servers []string, warn func(string)) mcpPolicy {
	seen, shared := map[string]bool{}, map[string]bool{}
	for _, s := range servers {
		name := mcp.Sanitize(s)
		shared[name] = seen[name] || strings.Contains(name, "__")
		seen[name] = true
	}

	return mcpPolicy{policy: p, shared: shared, warn: warn}
}

// allowsServer reports whether the resource tools may reach a server.
func (m mcpPolicy) allowsServer(server string) bool {
	return !m.shared[mcp.Sanitize(server)] && m.policy.AllowsServer(server)
}

// tools are the MCP tools the policy allows, each with a name of its own.
func (m mcpPolicy) tools(tools []mcp.Tool) []mcp.Tool {
	if !m.policy.Restricted() {
		return tools
	}
	byName := map[string]int{}
	for _, t := range tools {
		byName[exposed(t)]++
	}
	var out []mcp.Tool
	for _, t := range tools {
		var why string
		switch {
		case m.shared[mcp.Sanitize(t.Server)]:
			why = "its server's name is ambiguous: another server has the same exposed name, or it has \"__\""
		case byName[exposed(t)] > 1:
			why = "another tool has the same exposed name"
		case t.Name != exposed(t):
			why = "its exposed name is shortened"
		case !m.policy.AllowsMCP(t.Server, t.Tool):
			continue
		default:
			out = append(out, t)

			continue
		}
		if m.warn != nil && m.policy.AllowsMCP(t.Server, t.Tool) {
			m.warn(fmt.Sprintf("tool policy: left out %s (server %q, tool %q): %s", t.Name, t.Server, t.Tool, why))
		}
	}

	return out
}

// exposed is the name the model sees for a tool when nothing collides.
func exposed(t mcp.Tool) string {
	return mcp.Prefix + mcp.Sanitize(t.Server) + "__" + mcp.Sanitize(t.Tool)
}

// runnerTools are the tools the runner's registry defines itself.
var runnerTools = []string{tool.BashName, tool.ViewImageName, tool.SkillUseName}

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

// withPolicy wraps the registry when the policy restricts anything:
// allowed are the run's MCP tools the policy allows (mcpPolicy.tools),
// tools all of them. A call resolves through the run's own table, so an
// exposed name means the same tool for the whole run.
func withPolicy(r tool.Registry, p toolpolicy.Policy, allowed, tools []mcp.Tool) tool.Registry {
	if !p.Restricted() {
		return r
	}
	names := map[string]bool{}
	for _, t := range tools {
		names[t.Name] = false
	}
	for _, t := range allowed {
		names[t.Name] = true
	}

	return policyRegistry{Registry: r, policy: p, mcp: names}
}

func (r policyRegistry) allows(name string) bool {
	if allowed, ok := r.mcp[name]; ok || strings.HasPrefix(name, mcp.Prefix) {
		return allowed // a name no tool of the run has allows nothing
	}

	return r.policy.Allows(name)
}

// Skills are none when the policy does not allow SkillUse, so the prompt
// lists none.
func (r policyRegistry) Skills() []tool.Skill {
	if !r.policy.Allows(tool.SkillUseName) {
		return nil
	}

	return r.Registry.Skills()
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

// policyOperations checks each operation the coordinator adds against the
// policy, the run's own tool table included, and cancels one the policy
// does not allow before it starts. A new call's operation passed the
// policy as it was translated; this catches one a resumed session
// restores, which was submitted under another policy (or none) and the
// coordinator adds again without translating it.
type policyOperations struct {
	operation.Manager

	allowed func(operation.Operation) bool
}

// withPolicyOperations wraps the run's operations when allowed is set.
func withPolicyOperations(m operation.Manager, allowed func(operation.Operation) bool) operation.Manager {
	if allowed == nil {
		return m
	}

	return policyOperations{Manager: m, allowed: allowed}
}

func (m policyOperations) Add(op operation.Operation) error {
	if (op.Status == operation.StatusReady || op.Status == operation.StatusAwaiting) && !m.allowed(op) {
		op.Status = operation.StatusCanceling // every handler stops a canceling operation without running it
	}

	return m.Manager.Add(op)
}

// operationAllowed reports whether the policy allows the tool an operation
// runs: shell for Bash, view_image, skill_use, and the remote jobs by their
// plans, an MCP call by its identity in the run's table and a resource
// request by its server. The goal and question jobs carry a result the
// session already applied, and a value operation runs nothing.
func operationAllowed(p toolpolicy.Policy, mcpAllowed []mcp.Tool, servers func(string) bool) func(operation.Operation) bool {
	identities := map[[2]string]bool{}
	for _, t := range mcpAllowed {
		identities[[2]string{t.Server, t.Tool}] = true
	}

	return func(op operation.Operation) bool {
		switch op.Type {
		case operation.TypeShell:
			return p.Allows(tool.BashName)
		case operation.TypeViewImage:
			return p.Allows(tool.ViewImageName)
		case operation.TypeSkillUse:
			return p.Allows(tool.SkillUseName)
		case operation.TypeRemoteJob:
		default:
			return true
		}
		state, err := operation.DecodeRemoteJobState(op)
		if err != nil {
			return false
		}
		switch state.Plan.Type {
		case mcpPlanType:
			var plan mcpPlan
			if json.Unmarshal(state.Plan.Data, &plan) != nil {
				return false
			}
			switch plan.Op {
			case "":
				return identities[[2]string{plan.Server, plan.Tool}]
			case opListResources:
				return p.Allows(mcp.ListResourcesTool) && servers(plan.Server)
			case opListTemplates:
				return p.Allows(mcp.ListResourceTemplatesTool) && servers(plan.Server)
			}

			return p.Allows(mcp.ReadResourceTool) && servers(plan.Server)
		case agentPlanType:
			var plan agentPlan
			return json.Unmarshal(state.Plan.Data, &plan) == nil && p.Allows(plan.Tool)
		case engine.PatchPlanType:
			return p.Allows(patch.ToolName)
		}

		return true
	}
}
