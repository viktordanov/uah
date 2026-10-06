package embedded

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"

	"github.com/viktordanov/uah-core/harness/llm"
	"github.com/viktordanov/uah-core/harness/operation"
	"github.com/viktordanov/uah-core/harness/tool"
	"github.com/viktordanov/uah-core/harness/tool/bash"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/approval"
	"github.com/viktordanov/uah/internal/sandbox"
)

// Escalation arguments, as Codex's shell tool names them.
const (
	argSandboxPermissions = "sandbox_permissions"
	argJustification      = "justification"
	argPrefixRule         = "prefix_rule"
	permEscalated         = "require_escalated"
)

// sandboxedBash is the runner's Bash translator with the approver in front:
// each command runs through the sandboxing shell of the run's permission
// mode, through the real shell when a rule or the user allows it outside
// the sandbox, or not at all. The mode is read for each command, so a
// change applies from the next one, and so are the session's grants. The
// runner's translator reads only its own arguments, so the escalation
// arguments pass through it untouched.
type sandboxedBash struct {
	// Translator runs commands outside the sandbox and translates every
	// result; the sandboxed translators differ only in their shell.
	tool.Translator

	// boxes are the sandboxed translators and shells by sandbox mode, nil
	// without a sandbox on this system.
	boxes    *boxes
	mode     *modeCell
	approver *approval.Approver
	ask      approval.Ask
	// grants are the session's grants; policy is the sandbox policy of a
	// mode with them, for the paths an escalated command names.
	grants *sandbox.Grants
	policy func(sandbox.Mode) sandbox.Policy
	// ctx bounds the approval of a call decided in Translate: the run's,
	// ended early by an interrupt (prefetch.go decides most calls).
	ctx  context.Context
	cwd  string
	warn io.Writer
	// commands are the run's commands: a kill of the session's own ones
	// runs outside the sandbox (commands.ownKill).
	commands *commands
}

// sandboxShell is a translator that runs commands through a sandboxing
// shell.
type sandboxShell struct {
	tool.Translator

	shell string
}

// boxes are a run's sandboxing shells, one per sandbox mode, built again
// when the session's grants change, so a new writable root reaches the
// next command.
type boxes struct {
	build  func(sandbox.Mode) (sandboxShell, error)
	grants *sandbox.Grants
	warn   io.Writer

	mu      sync.Mutex
	version uint64
	byMode  map[sandbox.Mode]sandboxShell
	// built are the modes of every shell built in this run, by shell.
	built map[string]sandbox.Mode
}

// get is the mode's shell for the grants as they are now. When the grants
// changed, it builds every mode's shell again and keeps them only when all
// were built; otherwise it fails, and tries again with the next command,
// so a shell with a dropped grant is never used.
func (b *boxes) get(mode sandbox.Mode) (sandboxShell, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if v := b.grants.Version(); v != b.version {
		fresh := make(map[sandbox.Mode]sandboxShell, len(b.byMode))
		for m := range b.byMode {
			box, err := b.build(m)
			if err != nil {
				return sandboxShell{}, false, fmt.Errorf("failed to build the sandbox for the session's writable directories: %w", err)
			}
			fresh[m] = box
		}
		for m, box := range fresh {
			b.byMode[m], b.built[box.shell] = box, m
		}
		b.version = v
	}
	box, ok := b.byMode[mode]

	return box, ok, nil
}

// modeOf is the mode of a shell built in this run.
func (b *boxes) modeOf(shell string) (sandbox.Mode, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	m, ok := b.built[shell]

	return m, ok
}

// sandboxedModes are the sandbox modes that have a sandboxing shell.
var sandboxedModes = []sandbox.Mode{sandbox.ReadOnly, sandbox.WorkspaceWrite}

// sandboxedBash builds the Bash translator for the configured sandbox,
// with a sandboxing shell for each mode a permission mode can pick. The
// unsandboxed shell still applies the environment policy. Without a
// sandbox on this system, every command no rule allows asks first.
func (w *wiring) sandboxedBash(req core.Request, opsDir, realShell string) (tool.Translator, error) {
	newBash := func(shell string) tool.Translator {
		return bash.New(bash.Config{Shell: shell, Directory: req.Workspace, BaseDirectory: opsDir})
	}
	plain, err := sandbox.Shell(w.e.cfg.SandboxDir, w.policy(req, sandbox.FullAccess), w.e.cfg.Env, realShell)
	if err != nil {
		return nil, err
	}
	b := sandboxedBash{
		Translator: newBash(plain), mode: w.mode, approver: w.e.cfg.Approver, ask: w.ask, grants: w.grants,
		policy: func(m sandbox.Mode) sandbox.Policy { return w.policy(req, m) },
		ctx:    context.Background(), cwd: req.Workspace, warn: w.l.Stderr,
	}
	build := func(mode sandbox.Mode) (sandboxShell, error) {
		shell, err := sandbox.Shell(w.e.cfg.SandboxDir, w.policy(req, mode), w.e.cfg.Env, realShell)
		if err != nil {
			return sandboxShell{}, err
		}

		return sandboxShell{Translator: newBash(shell), shell: shell}, nil
	}
	boxes := &boxes{
		build: build, grants: w.grants, warn: w.l.Stderr, version: w.grants.Version(),
		byMode: map[sandbox.Mode]sandboxShell{}, built: map[string]sandbox.Mode{},
	}
	for _, mode := range sandboxedModes {
		box, err := build(mode)
		switch {
		case errors.Is(err, sandbox.ErrUnavailable):
			if w.mode.get().Sandbox() != sandbox.FullAccess {
				_, _ = fmt.Fprintf(w.l.Stderr, "embedded: %v; each command asks for approval unless a rule allows it\n", err)
			}

			return b, nil
		case err != nil:
			return nil, err
		}
		boxes.byMode[mode], boxes.built[box.shell] = box, mode
	}
	b.boxes = boxes

	return b, nil
}

// available reports whether this system has a sandbox.
func (b sandboxedBash) available() bool { return b.boxes != nil }

// current is the sandbox mode the next command runs in and its
// translator: the unsandboxed one in yolo mode or without a sandbox.
func (b sandboxedBash) current() (sandbox.Mode, sandboxShell, error) {
	mode := b.mode.get().Sandbox()
	box, err := b.box(mode)

	return mode, box, err
}

// box is the mode's translator for the grants as they are now.
func (b sandboxedBash) box(mode sandbox.Mode) (sandboxShell, error) {
	if b.boxes != nil {
		box, ok, err := b.boxes.get(mode)
		if err != nil || ok {
			return box, err
		}
	}

	return sandboxShell{Translator: b.Translator}, nil
}

// Translate asks the approver how the command runs.
func (b sandboxedBash) Translate(ctx tool.Context, call llm.ToolCall) tool.CallStatus {
	return b.decide(b.ctx, call)(ctx)
}

// decide asks the approver how the command runs, under ctx.
func (b sandboxedBash) decide(ctx context.Context, call llm.ToolCall) submit {
	var args struct {
		Command       string   `json:"command"`
		Permissions   string   `json:"sandbox_permissions"`
		Justification string   `json:"justification"`
		PrefixRule    []string `json:"prefix_rule"`
	}
	if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil || args.Command == "" {
		return func(tc tool.Context) tool.CallStatus { return b.Translator.Translate(tc, call) } // the runner's translator reports bad arguments
	}
	mode, box, err := b.current()
	sandboxed := box.shell != ""
	escalated := args.Permissions == permEscalated && sandboxed
	if err == nil && escalated && b.grantWorktrees(mode, args.Command) {
		// The command now runs in the sandbox, which writes there; if it
		// needs more, it fails with the hint and the model escalates again.
		escalated = false
		_, err = b.box(mode) // the new shell builds; the command takes it when it starts
	}
	if err != nil {
		return refuse(tool.ErrorStatus(err.Error(), 0))
	}
	if run, ok := b.killOwn(call, args.Command, sandboxed); ok {
		return run
	}
	d := b.approver.Decide(ctx, approval.Request{
		Command: args.Command, Cwd: b.cwd, Justification: args.Justification, PrefixRule: args.PrefixRule,
		Escalated: escalated,
		NoSandbox: !sandboxed && mode != sandbox.FullAccess, Bypass: b.mode.get().AsksNoOne(),
		Approved: hookAllowed(ctx),
	}, b.ask)
	if d.Run != approval.Deny && d.Reason != "" {
		_, _ = fmt.Fprintf(b.warn, "embedded: %s\n", d.Reason)
	}
	switch d.Run {
	case approval.Unsandboxed:
		return func(tc tool.Context) tool.CallStatus { return b.Translator.Translate(tc, call) }
	case approval.Sandboxed:
		// The shell for the grants when the command starts, not when it
		// was decided: one dropped meanwhile must not reach it.
		return func(tc tool.Context) tool.CallStatus {
			box, err := b.box(mode)
			if err != nil {
				return tool.ErrorStatus(err.Error(), 0)
			}

			return box.Translate(tc, call)
		}
	case approval.Deny:
	}

	return refuse(tool.CallStatus{Error: d.Reason})
}

// killOwn runs a kill of the session's own commands outside the sandbox
// (commands.ownKill), unless a forbid rule refuses it: the checked command,
// checked again as it runs, since a prefetched decision may be older than
// a target's exit. ok is false for any other command.
func (b sandboxedBash) killOwn(call llm.ToolCall, command string, sandboxed bool) (submit, bool) {
	kill, own := b.commands.ownKill(command)
	if !sandboxed || !own || b.forbidden(command) {
		return nil, false
	}
	arguments, ok := withCommand(call.Arguments, kill)
	if !ok {
		return nil, false
	}
	call.Arguments = arguments

	return func(tc tool.Context) tool.CallStatus {
		if again, own := b.commands.ownKill(command); !own || again != kill {
			return tool.ErrorStatus("the command it would stop has ended, so nothing was signalled", 0)
		}

		return b.Translator.Translate(tc, call)
	}, true
}

// withCommand is a Bash call's arguments with another command; the rest
// stay as they were. ok is false when they do not encode again.
func withCommand(arguments, command string) (string, bool) {
	var args map[string]any
	if json.Unmarshal([]byte(arguments), &args) != nil || args == nil {
		return "", false
	}
	args["command"] = command
	data, err := json.Marshal(args)

	return string(data), err == nil
}

// forbidden reports whether a forbid rule refuses the command.
func (b sandboxedBash) forbidden(command string) bool {
	if b.approver == nil {
		return false
	}
	_, forbidden := b.approver.Forbidden(command)

	return forbidden
}

// TranslateResult adds a hint when the sandbox likely blocked the command.
// The hint goes by the shell the command ran in, not by the sandboxes of
// this run: a resumed session renders its history again, and a saved
// compaction covers it as it was (compaction.Record), so a result must not
// change when a new policy or version gives the sandbox a new script.
func (b sandboxedBash) TranslateResult(callID string, status tool.CallStatus, ops []operation.Operation) (llm.ToolResult, error) {
	result, err := b.Translator.TranslateResult(callID, status, ops)
	if err != nil || len(ops) != 1 {
		return result, err // the coordinator wraps tool errors
	}
	state, derr := operation.DecodeShellState(ops[0])
	if derr != nil || state.Result == nil || state.Result.ExitCode == 0 || !sandbox.Denied(state.Result.ExitCode, state.Result.Out, state.Result.Err) {
		return result, nil
	}
	if mode, ok := b.sandboxOf(state.Input.Shell); ok {
		result.Output = append(result.Output, llm.ToolResultOutput{Kind: llm.ToolResultText, Value: fmt.Sprintf(
			"\nuah: the %s sandbox likely blocked this. If the command needs more access, run it again with %s %q and a %s.",
			mode, argSandboxPermissions, permEscalated, argJustification)})
	}

	return result, nil
}

// sandboxOf is the sandbox mode a command's shell ran it in: a sandboxing
// shell of this run, or one uah wrote for an earlier policy (by its
// header); ok is false outside a sandbox.
func (b sandboxedBash) sandboxOf(shell string) (sandbox.Mode, bool) {
	if b.boxes != nil {
		if mode, ok := b.boxes.modeOf(shell); ok {
			return mode, true
		}
	}
	mode, ok := sandbox.ScriptMode(shell)

	return mode, ok && slices.Contains(sandboxedModes, mode)
}
