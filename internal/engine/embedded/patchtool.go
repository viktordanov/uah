package embedded

import (
	"context"
	"encoding/json"
	"encoding/json/jsontext"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/viktordanov/uah-core/harness/llm"
	"github.com/viktordanov/uah-core/harness/operation"
	"github.com/viktordanov/uah-core/harness/tool"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/approval"
	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/models"
	"github.com/viktordanov/uah/internal/patch"
	"github.com/viktordanov/uah/internal/sandbox"
)

const patchPlanVersion operation.RemoteJobPlanVersion = 1

// patchPlan is the remote job's plan: the patch, where it applies, the
// path each of its files was approved at (patch.Targets), which the job
// writes without following symlinks, and the directories the patch made
// writable for the session, which its result tells the model.
type patchPlan struct {
	Patch   string            `json:"patch"`
	Cwd     string            `json:"cwd"`
	Targets map[string]string `json:"targets,omitempty"`
	Granted []string          `json:"granted,omitempty"`
}

// patchRegistry offers Codex's apply_patch tool, a custom tool whose input
// is the raw patch, sampled from Codex's Lark grammar, and resolves its name
// even when it is not offered, so a session with past calls resumes on any
// model.
type patchRegistry struct {
	tool.Registry

	offered bool
	gate    patchGate
}

func withPatch(r tool.Registry, offered bool, gate patchGate) tool.Registry {
	return patchRegistry{Registry: r, offered: offered, gate: gate}
}

func (r patchRegistry) StaticDefinitions() []tool.Definition {
	defs := r.Registry.StaticDefinitions()
	if r.offered {
		defs = append(defs, tool.Definition{Tool: llm.Tool{
			Type: llm.ToolCustom, Name: patch.ToolName, Description: patch.Description,
			Grammar: &llm.ToolGrammar{Syntax: "lark", Definition: patch.Grammar},
		}})
	}

	return defs
}

func (r patchRegistry) Resolve(name string) (tool.Translator, bool) {
	if t, ok := r.Registry.Resolve(name); ok || name != patch.ToolName {
		return t, ok
	}

	return patchTranslator{offered: r.offered, gate: r.gate}, true
}

// offersPatch reports whether the run's model gets apply_patch: as its
// catalog says (Manager.ApplyPatch), unless the request disallows it.
func offersPatch(catalog *models.Manager, req core.Request) bool {
	return catalog.ApplyPatch(req.Provider, req.Model) && !slices.Contains(req.DisallowedTools, patch.ToolName)
}

// patchGate builds the run's gate: the sandbox policy of the run's
// permission mode, read for each call as the Bash tool reads it, and the
// approver and ask that Bash escalations use (the ask lets the
// auto-reviewer decide alone in auto mode).
func (w *wiring) patchGate(ctx context.Context, req core.Request) patchGate {
	g := patchGate{ctx: ctx, cwd: req.Workspace, approver: w.e.cfg.Approver, ask: w.ask, grants: w.grants}
	if w.e.cfg.Sandbox != nil {
		g.policy = func() sandbox.Policy { return w.policy(req, w.mode.get().Sandbox()) }
	}

	return g
}

// patchTranslator checks a patch and its approval, then submits it as a
// remote job that applies it.
type patchTranslator struct {
	offered bool
	gate    patchGate
}

func (t patchTranslator) Translate(ctx tool.Context, call llm.ToolCall) tool.CallStatus {
	return t.decide(t.gate.ctx, call)(ctx)
}

// decide checks the patch, and its approval under ctx.
func (t patchTranslator) decide(ctx context.Context, call llm.ToolCall) submit {
	if !t.offered {
		return refuse(tool.ErrorStatus(fmt.Sprintf("tool %q is not available in this session", patch.ToolName), 0))
	}
	text, err := patch.ParseArgs(call.Arguments)
	if err != nil && call.Custom {
		// A call's input is the patch; Parse says what is wrong with it.
		text, err = call.Arguments, nil
	}
	if err != nil {
		return refuse(tool.ErrorStatus(err.Error(), 0))
	}
	// Checked before asking, as Codex verifies a patch before its approval,
	// so the user never approves a patch that cannot apply.
	hunks, err := patch.Parse(text)
	if err == nil {
		_, err = patch.Compute(t.gate.cwd, hunks)
	}
	if err != nil {
		return refuse(tool.ErrorStatus("apply_patch verification failed: "+err.Error(), 0))
	}
	targets, granted, approved, reason := t.gate.check(ctx, hunks, call.Arguments)
	if reason != "" {
		return refuse(tool.ErrorStatus(reason, 0))
	}
	data, err := json.Marshal(patchPlan{Patch: text, Cwd: t.gate.cwd, Targets: targets, Granted: granted})
	if err != nil {
		return refuse(tool.ErrorStatus(fmt.Sprintf("failed to encode the patch: %v", err), 0))
	}
	spec, err := operation.NewRemoteJobSpec(operation.RemoteJobPlan{Type: engine.PatchPlanType, Version: patchPlanVersion, Data: jsontext.Value(data)})
	if err != nil {
		return refuse(tool.ErrorStatus(fmt.Sprintf("failed to build the patch job: %v", err), 0))
	}

	return func(tc tool.Context) tool.CallStatus {
		if reason := t.gate.recheck(targets, approved); reason != "" {
			return tool.ErrorStatus(reason, 0)
		}

		return tool.CallStatus{WaitingFor: []operation.ID{tc.Submit(spec)}}
	}
}

func (patchTranslator) TranslateResult(callID string, status tool.CallStatus, ops []operation.Operation) (llm.ToolResult, error) {
	result := llm.ToolResult{CallID: callID}
	text := func(s string) {
		result.Output = append(result.Output, llm.ToolResultOutput{Kind: llm.ToolResultText, Value: s})
	}
	if status.Error != "" {
		text(status.Error)

		return result, nil
	}
	if len(ops) != 1 {
		return result, fmt.Errorf("apply_patch call %q has %d operations, want 1", callID, len(ops))
	}
	state, err := operation.DecodeRemoteJobState(ops[0])
	if err != nil {
		return result, fmt.Errorf("failed to decode apply_patch call %q: %w", callID, err)
	}
	switch ops[0].Status {
	case operation.StatusReady, operation.StatusAwaiting, operation.StatusCanceling:
		text("The patch is still being applied.")
	case operation.StatusFailed:
		text(state.TerminalError)
	case operation.StatusCanceled:
		text("apply_patch was canceled.")
	case operation.StatusCompleted:
		text(state.TerminalResult)
	}

	return result, nil
}

// hookInput is what PreToolUse hooks see: Codex's {"command": patch}.
func (patchTranslator) hookInput(arguments string) json.RawMessage { return patch.HookInput(arguments) }

// fromHookInput turns a hook's updatedInput back into arguments.
func (patchTranslator) fromHookInput(updated json.RawMessage) (string, error) {
	return patch.FromHookInput(updated) // the patch package's own message
}

// patchGate decides whether a patch applies without asking: writes inside
// the writable roots apply, as Codex auto-approves a patch constrained to
// writable paths (assess_patch_safety in codex-rs/core/src/safety.rs); any
// other write goes through the approver like a Bash escalation. A forbid
// rule refuses a patch before either, in every mode. In the
// workspace-write sandbox, a write into a worktree of the workspace's
// repository first makes that worktree writable for the session, asking
// no one, and the user may allow writes to a directory for the session
// when asked about a patch.
type patchGate struct {
	// ctx bounds the approval of a patch decided in Translate.
	ctx context.Context
	cwd string
	// policy is the sandbox policy of the current permission mode, with
	// the session's grants; nil without a sandbox, when every patch
	// applies.
	policy   func() sandbox.Policy
	approver *approval.Approver
	ask      approval.Ask
	// grants are the session's grants (nil: none, and none added).
	grants *sandbox.Grants
}

// check returns the patch's targets and the directories it made writable
// for the session, or why the patch may not apply. The targets are its
// paths with their symlinks resolved, as the sandbox checks them; the job
// writes there without following symlinks, so a directory a command swaps
// for a symlink after the check fails the patch instead of leading it out
// of the checked directories. check blocks while the user decides, at most
// until ctx ends.
func (g patchGate) check(ctx context.Context, hunks []patch.Hunk, arguments string) (targets patch.Targets, granted, approved []string, reason string) {
	paths := patch.Paths(g.cwd, hunks)
	targets = patch.Targets{}
	for _, p := range paths {
		targets[p] = sandbox.ResolvePath(p)
	}
	if reason := g.forbidden(hunks, paths, targets); reason != "" {
		return nil, nil, nil, reason
	}
	if g.policy == nil {
		return targets, nil, nil, ""
	}
	policy := g.policy()
	if policy.Mode == sandbox.FullAccess {
		return targets, nil, nil, ""
	}
	outside := outsidePaths(policy, paths, targets)
	if len(outside) > 0 && g.grants != nil && policy.Mode == sandbox.WorkspaceWrite {
		if granted = grantWorktrees(g.grants, policy, resolvedPaths(outside, targets)); len(granted) > 0 {
			policy = g.policy()
			outside = outsidePaths(policy, paths, targets)
		}
	}
	if len(outside) == 0 {
		return targets, granted, nil, ""
	}
	why := "the patch writes outside the writable roots"
	if policy.Mode == sandbox.ReadOnly {
		why = "the sandbox is read-only"
	}
	req := approval.Request{
		Command: patchCommand(outside), Cwd: g.cwd, Escalated: true, Justification: why,
		Tool: patch.ToolName, Input: patch.HookInput(arguments), Approved: hookAllowed(ctx),
		GrantRoot: g.offer(policy, resolvedPaths(outside, targets)),
	}
	if g.approver == nil {
		return nil, nil, nil, "apply_patch rejected: " + why + ", and no one can approve it."
	}
	d := g.approver.Decide(ctx, req, g.ask)
	if d.Run == approval.Deny {
		return nil, nil, nil, d.Reason
	}
	if d.Grant != "" && g.grants.Add(d.Grant, sandbox.GrantApproved) {
		granted = append(granted, d.Grant)
	}

	return targets, granted, outside, ""
}

// recheck returns why the patch may no longer apply when it starts: a path
// the policy let it write when it was checked, and not approved, that the
// policy no longer lets it write, as when a granted directory was replaced
// while the patch waited for another call, or the mode became stricter.
func (g patchGate) recheck(targets patch.Targets, approved []string) string {
	if g.policy == nil {
		return ""
	}
	policy := g.policy()
	for p, target := range targets {
		if !slices.Contains(approved, p) && !policy.CanWriteResolved(target) {
			return "apply_patch rejected: " + p + " is no longer inside the writable roots; a directory or the permission mode changed since the patch was checked. Check the files and apply the patch again."
		}
	}

	return ""
}

// outsidePaths are the paths whose targets the policy does not let the
// patch write.
func outsidePaths(policy sandbox.Policy, paths []string, targets patch.Targets) []string {
	var outside []string
	for _, p := range paths {
		if !policy.CanWriteResolved(targets[p]) {
			outside = append(outside, p)
		}
	}

	return outside
}

func resolvedPaths(paths []string, targets patch.Targets) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		out = append(out, targets[p])
	}

	return out
}

// offer is the directory the user may allow writes to for the session
// when asked about a patch that writes the resolved paths: the working
// tree that holds them, else their common directory (sandbox.GrantFor),
// offered only in the workspace-write sandbox and only when it would let
// the patch write every one of them, so never for a protected path.
func (g patchGate) offer(policy sandbox.Policy, resolved []string) string {
	if g.grants == nil || policy.Mode != sandbox.WorkspaceWrite {
		return ""
	}
	dir := sandbox.GrantFor(resolved)
	if dir == "" || policy.Holds(dir) || policy.InRoot(dir) {
		return ""
	}
	policy.WritableRoots = append(slices.Clip(policy.WritableRoots), dir)
	for _, p := range resolved {
		if !policy.CanWriteResolved(p) {
			return ""
		}
	}

	return dir
}

// forbidden returns why a forbid rule refuses the patch, or "": checked
// on the whole patch and on each path alone, as the patch writes it,
// absolute, and with its symlinks resolved, so a rule on any one path,
// relative or absolute or reached through a link, applies wherever that
// path is in the patch.
func (g patchGate) forbidden(hunks []patch.Hunk, paths []string, targets patch.Targets) string {
	if g.approver == nil {
		return ""
	}
	commands := []string{patchCommand(paths)}
	for _, h := range hunks {
		for _, name := range []string{h.Path, h.MovePath} {
			if name != "" && !filepath.IsAbs(name) {
				commands = append(commands, patchCommand([]string{name}))
			}
		}
	}
	for _, p := range paths {
		commands = append(commands, patchCommand([]string{p}))
		if targets[p] != p {
			commands = append(commands, patchCommand([]string{targets[p]}))
		}
	}
	for _, c := range commands {
		if reason, ok := g.approver.Forbidden(c); ok {
			return reason
		}
	}

	return ""
}

// patchCommand describes a patch's writes for the rules and the prompt:
// "apply_patch" and the paths, quoted for the shell where needed, so a
// rule on the prefix ["apply_patch"] allows or forbids patches outside the
// sandbox.
func patchCommand(paths []string) string {
	words := []string{patch.ToolName}
	for _, p := range paths {
		if strings.ContainsFunc(p, func(r rune) bool { return !isShellSafe(r) }) {
			p = "'" + strings.ReplaceAll(p, "'", `'\''`) + "'"
		}
		words = append(words, p)
	}

	return strings.Join(words, " ")
}

func isShellSafe(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./~+,:@%", r)
}
