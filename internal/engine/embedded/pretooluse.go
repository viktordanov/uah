package embedded

import (
	"context"
	"encoding/json"
	"path/filepath"

	"github.com/viktordanov/uah-core/harness/llm"
	"github.com/viktordanov/uah-core/harness/tool"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/hooks"
)

// hookedRegistry runs PreToolUse hooks before each tool call is translated.
// A block becomes the tool's error result, and updatedInput replaces the
// arguments. The hooks of a response's calls run at once, each before its
// call's approval (prefetch.go), so a slow hook delays its own call up to
// its timeout.
type hookedRegistry struct {
	tool.Registry

	ctx   context.Context
	hooks *hooks.Runner
	base  hooks.Input
}

func withPreToolUse(ctx context.Context, r tool.Registry, runner *hooks.Runner, req core.Request, sessionsDir string) tool.Registry {
	if !runner.Has(hooks.PreToolUse, "") {
		return r
	}

	return hookedRegistry{Registry: r, ctx: ctx, hooks: runner, base: hooks.Input{
		Event: hooks.PreToolUse, SessionID: req.SessionID, RunID: req.RunID, Cwd: req.Workspace,
		Model: req.Model, Effort: req.Effort, TranscriptPath: filepath.Join(sessionsDir, req.SessionID+".session.jsonl"),
	}}
}

func (r hookedRegistry) Resolve(name string) (tool.Translator, bool) {
	t, ok := r.Registry.Resolve(name)
	if !ok || !r.hooks.Has(hooks.PreToolUse, name) {
		return t, ok
	}

	return hookedTranslator{Translator: t, name: name, r: r}, true
}

// hookAllowKey marks the context of a call that a PreToolUse hook allowed
// ("permissionDecision": "allow"): the call's approval, if it needs one,
// is given without asking, as in Claude Code. The rules still apply.
type hookAllowKey struct{}

// hookAllowed reports whether a PreToolUse hook allowed the call decided
// under ctx.
func hookAllowed(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	allowed, _ := ctx.Value(hookAllowKey{}).(bool)

	return allowed
}

type hookedTranslator struct {
	tool.Translator

	name string
	r    hookedRegistry
}

// hookShaper is a tool whose hook tool_input differs from its arguments,
// such as apply_patch's {"command": patch}.
type hookShaper interface {
	hookInput(arguments string) json.RawMessage
	fromHookInput(updated json.RawMessage) (string, error)
}

func (t hookedTranslator) Translate(ctx tool.Context, call llm.ToolCall) tool.CallStatus {
	return t.decide(t.r.ctx, call)(ctx)
}

// decide runs the hooks under ctx, then the tool's own decision, if it
// has one, with the arguments the hooks left.
func (t hookedTranslator) decide(ctx context.Context, call llm.ToolCall) submit {
	in := t.r.base
	in.ToolName, in.ToolUseID = t.name, call.CallID
	shaper, shaped := t.Translator.(hookShaper)
	switch {
	case shaped:
		in.ToolInput = shaper.hookInput(call.Arguments)
	case json.Valid([]byte(call.Arguments)):
		in.ToolInput = json.RawMessage(call.Arguments)
	}
	d := t.r.hooks.Run(ctx, in)
	if d.Block {
		return refuse(tool.CallStatus{Error: "blocked by a PreToolUse hook: " + d.Reason})
	}
	if ctx.Err() != nil {
		// An interrupt stopped the hooks before they decided: the call
		// never runs unchecked.
		return refuse(tool.CallStatus{Error: "the run stopped before the PreToolUse hooks finished; the call did not run"})
	}
	if d.Allow {
		ctx = context.WithValue(ctx, hookAllowKey{}, true)
	}
	switch {
	case len(d.UpdatedInput) == 0:
	case shaped:
		args, err := shaper.fromHookInput(d.UpdatedInput)
		if err != nil {
			return refuse(tool.CallStatus{Error: "a PreToolUse hook's updatedInput is invalid: " + err.Error()})
		}
		call.Arguments = args
	default:
		call.Arguments = string(d.UpdatedInput)
	}
	if g, ok := t.Translator.(gatedTranslator); ok {
		return g.decide(ctx, call)
	}

	return func(tc tool.Context) tool.CallStatus { return t.Translator.Translate(tc, call) }
}
