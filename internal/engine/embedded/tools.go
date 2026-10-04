package embedded

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/viktordanov/uah-core/harness/session"
	"github.com/viktordanov/uah-core/harness/tool"
	"github.com/viktordanov/uah-core/harness/tool/bash"
	"github.com/viktordanov/uah-core/harness/tool/viewimage"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/approval"
	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/mcp"
	"github.com/viktordanov/uah/internal/sandbox"
	uahsession "github.com/viktordanov/uah/internal/session"
)

// tools builds the registry the coordinator runs: Bash, ViewImage, and
// workspace skills, as the runner registers them, MCP tools, and Codex's
// apply_patch, the agent tools, request_user_input, and the goal tools, with PreToolUse
// hooks around them and the tool policy around those (policy.go). Its static definitions are the tools the model is offered,
// so a tool added or changed here reaches both. approvals bounds the hooks
// and approvals of the calls: the run's context, ended early by an
// interrupt.
func (w *wiring) tools(ctx, approvals context.Context, req core.Request, sessionID session.ID) (tool.Registry, error) {
	scope := w.e.scope(string(sessionID))
	req.SessionID = string(sessionID)
	policy := w.e.cfg.Tools
	req.DisallowedTools = policyDisallow(policy, scope.disallow(req.DisallowedTools))
	translators, err := w.translators(req, sessionID) //nolint:contextcheck,nolintlint // on Linux, the sandbox probes bwrap once per process, with its own timeout; not on darwin
	if err != nil {
		return nil, err
	}
	b, sandboxed := translators.Bash.(sandboxedBash)
	if sandboxed {
		b.ctx = approvals
		translators.Bash = b
	}
	var skills []tool.Skill
	var skillErrs []error
	if !w.e.cfg.NoSkills {
		skills, skillErrs = discoverSkills(req.Workspace, w.getenv)
	}
	registry := tool.NewRegistry(translators, toolNames(req, len(skills) > 0)...)
	if sandboxed && b.available() {
		registry = w.withSandbox(registry, req)
	}
	if err := registerSkills(registry, skills); err != nil {
		return nil, err
	}
	for _, err := range skillErrs {
		_, _ = fmt.Fprintf(w.l.Stderr, "skill error> %s\n", err)
	}
	mcpTools, err := w.mcpTools(ctx)
	if err != nil {
		return nil, err
	}
	never := w.e.cfg.Approver != nil && w.e.cfg.Approver.Policy() == approval.Never
	gate := w.mcpGate(approvals, never)
	gate.approved = scope.approvesTool
	resources := w.e.cfg.MCP != nil && w.e.cfg.MCP.HasServers()
	var servers []string
	if resources {
		servers = w.e.cfg.MCP.Names()
	}
	mp := newMCPPolicy(policy, servers, func(msg string) { _, _ = fmt.Fprintf(w.l.Stderr, "mcp> %s\n", msg) })
	if policy.Restricted() {
		gate.servers = mp.allowsServer
	}
	allowed := mp.tools(mcpTools)
	if policy.Restricted() {
		w.opAllowed = operationAllowed(policy, allowed, mp.allowsServer)
	}
	registry = withMCP(registry, scope.mcpTools(allowed), resources, scope.disallowResources(req.DisallowedTools), gate)
	registry = withPatch(registry, offersPatch(w.e.models, req), w.patchGate(approvals, req))
	registry = w.withAgents(registry, req)
	registry = withQuestions(registry, questionTranslator{offered: w.offersQuestions(req), root: !isSubagent(req.SessionID), ctx: approvals, ask: w.askUser})
	registry = withGoals(registry, goalTranslator{offered: w.offersGoals(req), root: !isSubagent(req.SessionID), ctx: approvals, goal: w.goal})

	registry = withPreToolUse(approvals, registry, w.e.cfg.Hooks, req, w.l.SessionsDir)

	return withPolicy(registry, policy, allowed, mcpTools), nil
}

// withSandbox offers Bash with the escalation arguments and a note on the
// sandbox, and has the switcher keep them in step with the mode.
func (w *wiring) withSandbox(registry tool.Registry, req core.Request) tool.Registry {
	policy := func() sandbox.Policy { return w.policy(req, w.mode.get().Sandbox()) }
	for _, d := range registry.StaticDefinitions() {
		if d.Tool.Name == tool.BashName {
			w.bashTools = bashTools(d.Tool, policy)
		}
	}

	return sandboxRegistry{Registry: registry, policy: policy}
}

// withAgents attaches the run to the subagents and adds the tools they
// offer it.
func (w *wiring) withAgents(registry tool.Registry, req core.Request) tool.Registry {
	a := w.e.cfg.Subagents
	if a == nil {
		return registry
	}
	emit := w.notify
	if emit == nil {
		emit = w.emit
	}
	if emit == nil {
		emit = func(core.Event) {}
	}
	offered := a.Attach(engine.AgentParent{
		SessionID: req.SessionID, Request: req, Settings: w.parentSettings(req),
		Ask: w.askAnytime, Emit: emit, Inject: w.inject, Grants: w.grants,
	})

	return withAgents(registry, offered, a.ToolNames(), req.DisallowedTools)
}

// parentSettings are the settings the run's children start with: the
// session's as they are when a child starts, or, for a run without them,
// the run's model, effort, service tier, and adaptive effort as it started
// and its permission mode now.
func (w *wiring) parentSettings(req core.Request) func() engine.LiveSettings {
	if w.settings != nil {
		return w.settings
	}
	start := engine.LiveSettings{Model: req.Model, Effort: req.Effort, ServiceTier: w.tier, AdaptiveEffort: w.adaptive}

	return func() engine.LiveSettings {
		s := start
		s.Mode = w.mode.get()

		return s
	}
}

// translators returns the built-in tools. Bash runs in the workspace with
// the user's shell and keeps its operation output under the session's
// operation directory.
func (w *wiring) translators(req core.Request, sessionID session.ID) (tool.StaticTranslators, error) {
	opsDir := filepath.Join(w.l.SessionsDir, "operations", string(sessionID))
	if err := os.MkdirAll(opsDir, 0o700); err != nil {
		return tool.StaticTranslators{}, fmt.Errorf("failed to create the operation directory: %w", err)
	}
	shell := w.shell()
	run := bash.New(bash.Config{Shell: shell, Directory: req.Workspace, BaseDirectory: opsDir})
	if w.e.cfg.Sandbox != nil {
		var err error
		if run, err = w.sandboxedBash(req, opsDir, shell); err != nil {
			return tool.StaticTranslators{}, err
		}
	}

	return tool.StaticTranslators{Bash: run, ViewImage: viewimage.New(viewimage.Config{Directory: req.Workspace})}, nil
}

// mcpGate asks about MCP calls whose approval_mode needs it, with the
// manager's live approval modes.
func (w *wiring) mcpGate(ctx context.Context, never bool) mcpGate {
	warn := func(msg string) { _, _ = fmt.Fprintf(w.l.Stderr, "mcp> %s\n", msg) }

	mode := w.mode

	g := mcpGate{ctx: ctx, ask: w.ask, never: never, m: w.e.cfg.MCP, warn: warn, yolo: func() bool { return mode.get().AsksNoOne() }}
	if g.m != nil {
		g.changes = &approval.Changes{}
	}

	return g
}

// mcpTools returns the MCP servers' tools, starting the servers on the
// first run unless an interactive session connected them as it opened; a
// server that fails to start is reported and left out.
func (w *wiring) mcpTools(ctx context.Context) ([]mcp.Tool, error) {
	m := w.e.cfg.MCP
	if m == nil {
		return nil, nil
	}
	tools, err := m.Tools(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to start MCP servers: %w", err)
	}
	for _, s := range m.Status() { //nolint:contextcheck // started above; servers outlive the run
		if s.State == mcp.StateFailed || s.State == mcp.StateNeedsLogin {
			_, _ = fmt.Fprintf(w.l.Stderr, "mcp> %s: %s\n", s.Name, s.Error)
		}
	}

	return tools, nil
}

// policy is the configured sandbox policy for the request's workspace,
// with its private temporary directory, the sandbox scripts read-only, and
// the session's grants as more writable roots (withGrants).
func (w *wiring) policy(req core.Request, mode sandbox.Mode) sandbox.Policy {
	p := *w.e.cfg.Sandbox
	p.Mode, p.Workspace = mode, req.Workspace
	p.TempDir = uahsession.TempDir(w.l.SessionsDir, req.SessionID)
	if dir := w.e.cfg.SandboxDir; dir != "" {
		// The sandbox scripts run outside the sandbox (sandbox.Shell).
		p.ReadOnly = append(slices.Clip(p.ReadOnly), dir)
	}
	if mode == sandbox.WorkspaceWrite {
		p = withGrants(p, w.grants.Roots())
	}

	return p
}

// withGrants adds the granted directories to the policy's writable roots,
// whatever order they were granted in, leaving out a grant that is, holds,
// or lies inside one of the policy's own roots, and then a grant that a protected
// path of any root, the other grants included, covers. So no grant opens
// part of a protected path, as Seatbelt's rule for an inner root and
// bubblewrap's later bind would, and none takes in a root's protected
// paths under a name the sandbox compares differently.
func withGrants(p sandbox.Policy, grants []string) sandbox.Policy {
	var roots []string
	for _, g := range grants {
		if !p.Holds(g) && !p.InRoot(g) {
			roots = append(roots, g)
		}
	}
	all := p
	all.WritableRoots = append(slices.Clip(p.WritableRoots), roots...)
	for _, g := range roots {
		if !all.Protects(g) {
			p.WritableRoots = append(slices.Clip(p.WritableRoots), g)
		}
	}

	return p
}

// toolNames returns the tools to enable: Bash, ViewImage, and SkillUse when
// the workspace has skills, less the request's disallowed tools.
func toolNames(req core.Request, hasSkills bool) []string {
	names := []string{tool.BashName, tool.ViewImageName}
	if hasSkills {
		names = append(names, tool.SkillUseName)
	}

	return slices.DeleteFunc(names, func(n string) bool { return slices.Contains(req.DisallowedTools, n) })
}

// registerSkills registers the skills when SkillUse is enabled.
func registerSkills(registry tool.Registry, skills []tool.Skill) error {
	if _, ok := registry.Resolve(tool.SkillUseName); !ok {
		return nil
	}
	for _, s := range skills {
		if _, err := registry.RegisterSkill(s); err != nil {
			return fmt.Errorf("failed to register skill %q: %w", s.Path, err)
		}
	}

	return nil
}
