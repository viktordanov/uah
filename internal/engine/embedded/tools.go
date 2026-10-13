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
	"github.com/viktordanov/uah/internal/hooks"
	"github.com/viktordanov/uah/internal/mcp"
	"github.com/viktordanov/uah/internal/sandbox"
	uahsession "github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/internal/toolbridge"
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
	w.openBridge(scope, req.DisallowedTools, string(sessionID)) //nolint:contextcheck // first, as the sandbox's shells name its socket; the bridge outlives the run
	translators, err := w.translators(req, sessionID)           //nolint:contextcheck,nolintlint // on Linux, the sandbox probes bwrap once per process, with its own timeout; not on darwin
	if err != nil {
		return nil, err
	}
	var (
		background, sandboxed bool
		b                     sandboxedBash
	)
	translators.Bash, b, sandboxed, background = w.wireBash(ctx, approvals, req, scope, string(sessionID), translators.Bash)
	var skills []tool.Skill
	var skillErrs []error
	if !w.e.cfg.NoSkills {
		skills, skillErrs = discoverSkills(req.Workspace, w.getenv)
	}
	registry := withShells(tool.NewRegistry(translators, toolNames(req, len(skills) > 0)...), background, req.DisallowedTools)
	if w.bridge != nil {
		registry = withBridgeNote(registry) // before the sandbox layer reads Bash's definition
	}
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
	if w.bridge != nil {
		w.updateBridge(req, scope.mcpTools(allowed), bridgeResources(resources, scope.disallowResources(req.DisallowedTools)), gate)
	}
	registry = withPatch(registry, offersPatch(w.e.models, req), w.patchGate(approvals, req))
	registry = w.withAgents(registry, req)
	registry = withQuestions(registry, questionTranslator{offered: w.offersQuestions(req), root: !isSubagent(req.SessionID), ctx: approvals, ask: w.askUser})
	registry = withGoals(registry, goalTranslator{offered: w.offersGoals(req), root: !isSubagent(req.SessionID), ctx: approvals, goal: w.goal})

	registry = withPreToolUse(approvals, registry, w.e.cfg.Hooks, req, w.l.SessionsDir)

	registry = withPolicy(registry, policy, allowed, mcpTools)
	if scope.closed() {
		registry = closedRegistry{registry}
	}

	return registry, nil
}

// offersBridge reports whether the run's commands may call the session's
// tools with `uah tool`: the bridge is on, the session has MCP servers,
// its commands run through the sandbox's script, which gives them the
// token and uah on PATH (every session uah opens has a sandbox policy,
// full access included), and the run offers Bash with no limit on each
// command, which a /review's reviewer has. Where bwrap leaves the host's
// /proc visible (some containers), a sandboxed command could read any
// session's token through /proc/<pid>/root, so no session gets one; and
// where the environment policy drops PATH, commands could not find uah,
// so the model is not told of it.
func (w *wiring) offersBridge(scope *scope, disallowed []string) bool {
	return !w.e.cfg.NoToolBridge && w.e.cfg.Sandbox != nil && w.e.cfg.MCP != nil && w.e.cfg.MCP.HasServers() && sandbox.ProcIsolated() &&
		w.e.cfg.Env.Getenv(w.getenv, "PATH")("PATH") != "" &&
		!scope.closed() && scope.commandTimeout() == 0 && !slices.Contains(disallowed, tool.BashName)
}

// wireBash puts the run's commands, background shells, and the scope's
// limit around Bash: it returns Bash, its sandboxed form when it has one,
// and whether the run offers background shells, which come with Bash and
// wait_shell. A scope with a limit on each command (a /review's reviewer)
// gets none, since they would outlive the limit.
func (w *wiring) wireBash(ctx, approvals context.Context, req core.Request, scope *scope, sessionID string, bash tool.Translator) (tool.Translator, sandboxedBash, bool, bool) {
	w.commands = newCommands(ctx, scope.commandTimeout())
	w.commands.shells = func(group int) bool { return w.e.shells.owns(sessionID, group) }
	background := !scope.closed() && scope.commandTimeout() == 0 &&
		!slices.Contains(req.DisallowedTools, tool.BashName) && !slices.Contains(req.DisallowedTools, toolWaitShell)
	w.shellSession = w.e.shells.attach(sessionID, w.inject, w.notify)
	b, sandboxed := bash.(sandboxedBash)
	if sandboxed {
		b.ctx, b.commands, b.background = approvals, w.commands, background
		bash = b
	} else {
		bash = plainBackground{Translator: bash, shell: w.shell(), dir: req.Workspace, offered: background}
	}
	if w.commands.limit > 0 {
		bash = timedBash{Translator: bash, c: w.commands}
	}

	return bash, b, sandboxed, background
}

// openBridge opens the session's tool bridge when the run offers it, and
// closes one an earlier run opened when this run does not: a background
// shell holding the old token can call nothing this run's scope forbids.
func (w *wiring) openBridge(scope *scope, disallowed []string, sessionID string) {
	if !w.offersBridge(scope, disallowed) {
		w.e.bridges.forget(sessionID) // a bridge an earlier run opened must not keep its permissions

		return
	}
	br, err := w.e.bridges.open(sessionID, uahsession.TempDir(w.l.SessionsDir, sessionID), filepath.Join(w.l.SessionsDir, "operations", sessionID))
	if err != nil {
		_, _ = fmt.Fprintf(w.l.Stderr, "embedded: %v; commands cannot call the session's tools\n", err)

		return
	}
	w.bridge = br
}

// bridgeResources are the resource tools the run offers, each as the
// direct registry offers it (withMCP).
func bridgeResources(offered bool, disallowed []string) []string {
	if !offered {
		return nil
	}
	var out []string
	for _, d := range resourceTools() {
		if !slices.Contains(disallowed, d.Tool.Name) {
			out = append(out, d.Tool.Name)
		}
	}

	return out
}

// updateBridge gives the session's bridge what this run allows: its MCP
// tools, the resource tools, the gate with the run's whole approval chain
// (the scope's approvals, the auto-reviewer, then the user, as for the
// model's own call), and the PreToolUse hooks. A call that needs the user
// after the run ended, from a background shell, is refused with the
// reason, as the run's ask is gone.
func (w *wiring) updateBridge(req core.Request, tools []mcp.Tool, resources []string, gate mcpGate) {
	tools = slices.DeleteFunc(slices.Clone(tools), func(t mcp.Tool) bool { return slices.Contains(req.DisallowedTools, t.Name) })
	notify := w.notify
	if notify == nil {
		notify = w.emit
	}
	w.bridge.update(bridgeState{
		manager: w.e.cfg.MCP, tools: tools, resources: resources, servers: gate.servers, gate: gate,
		hooks: w.e.cfg.Hooks, hookBase: hooks.Input{
			Event: hooks.PreToolUse, SessionID: req.SessionID, RunID: req.RunID, Cwd: req.Workspace,
			Model: req.Model, Effort: req.Effort, TranscriptPath: filepath.Join(w.l.SessionsDir, req.SessionID+".session.jsonl"),
		},
		notify: notify,
	})
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
	switch mode {
	case sandbox.WorkspaceWrite:
		p = withGrants(p, w.grants.Roots())
	case sandbox.ReadOnly:
		// network_access is [sandbox_workspace_write]'s, as in Codex,
		// whose read-only sandbox has no network: a /review's reviewer
		// never reaches it.
		p.Network = false
	}
	if w.bridge != nil { // `uah tool` reaches the session's socket in every mode
		p.Sockets = append(slices.Clip(p.Sockets), w.bridge.sockets...)
		p.BinDir = w.bridge.bin
		p.SetEnv = map[string]string{toolbridge.TokenVar: w.bridge.token}
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
