package embedded

import (
	"context"
	"encoding/json"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/viktordanov/uah-core/harness/llm"
	"github.com/viktordanov/uah-core/harness/operation"
	"github.com/viktordanov/uah-core/harness/tool"
)

// The remote job plan a background shell's start, wait_shell, and
// kill_shell run as.
const (
	shellPlanType    operation.RemoteJobPlanType    = "uah.shell"
	shellPlanVersion operation.RemoteJobPlanVersion = 1
)

// The shell tools and Bash's background argument.
const (
	toolWaitShell = "wait_shell"
	toolKillShell = "kill_shell"
	argBackground = "background"
)

// ShellToolNames are the background shell tools' names.
var ShellToolNames = []string{toolWaitShell, toolKillShell}

const backgroundDescription = "true starts the command as a background shell and returns at once with its ID and log file: " +
	"for servers, watchers, and long jobs you keep working beside, not for a command whose result you need next. " +
	"It runs on after this call and this turn, until it exits or you stop it with kill_shell. " +
	"You are told when it exits (<background_shell_notification>), so do not poll it; read its log with tail or grep."

const waitShellDescription = "Wait for background shells to exit. Returns once one of them has exited or the timeout passes, " +
	"with each shell's state, exit code, and the end of its output. Use it only when you have nothing else to do until a shell ends; " +
	"its notification comes anyway."

const killShellDescription = "Stop a background shell: its whole process group gets SIGTERM, then SIGKILL after 3 seconds. Returns how it ended."

// shellPlan is the remote job's plan: start a command, wait for shells,
// or stop one.
type shellPlan struct {
	Op string `json:"op"`
	// Start: the shell that runs Command (a sandboxing shell or the real
	// one) and the directory.
	Shell     string `json:"shell,omitempty"`
	Command   string `json:"command,omitempty"`
	Directory string `json:"directory,omitempty"`
	// CallID is the Bash call that starts it.
	CallID string `json:"call_id,omitempty"`
	// Wait and stop: the shells, and how long a wait lasts.
	IDs       []string `json:"shell_ids,omitempty"`
	TimeoutMS int64    `json:"timeout_ms,omitempty"`
}

const (
	shellOpStart = "start"
	shellOpWait  = "wait"
	shellOpKill  = "kill"
)

// backgroundRequested reports whether a Bash call asks for a background
// shell.
func backgroundRequested(arguments string) bool {
	var args struct {
		Background bool `json:"background"`
	}

	return json.Unmarshal([]byte(arguments), &args) == nil && args.Background
}

// startShell submits a Bash call's command as a background shell run by
// shellPath.
func startShell(tc tool.Context, call llm.ToolCall, shellPath, dir string) tool.CallStatus {
	var args struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil || args.Command == "" {
		return tool.ErrorStatus(`bash argument "command" must be set`, 0)
	}

	return submitShellPlan(tc, shellPlan{Op: shellOpStart, Shell: shellPath, Command: args.Command, Directory: dir, CallID: call.CallID})
}

func submitShellPlan(tc tool.Context, plan shellPlan) tool.CallStatus {
	data, err := json.Marshal(plan)
	if err != nil {
		return tool.ErrorStatus(fmt.Sprintf("failed to encode the shell call: %v", err), 0)
	}
	spec, err := operation.NewRemoteJobSpec(operation.RemoteJobPlan{Type: shellPlanType, Version: shellPlanVersion, Data: jsontext.Value(data)})
	if err != nil {
		return tool.ErrorStatus(fmt.Sprintf("failed to build the shell call: %v", err), 0)
	}

	return tool.CallStatus{WaitingFor: []operation.ID{tc.Submit(spec)}}
}

// shellJobResult is the result of a call that ran as a shell job; ok is
// false for any other operation, which the translator under it reads.
func shellJobResult(callID string, status tool.CallStatus, ops []operation.Operation) (llm.ToolResult, bool) {
	if status.Error != "" || len(ops) != 1 || ops[0].Type != operation.TypeRemoteJob {
		return llm.ToolResult{}, false
	}
	state, err := operation.DecodeRemoteJobState(ops[0])
	if err != nil || state.Plan.Type != shellPlanType {
		return llm.ToolResult{}, false
	}
	var text string
	switch ops[0].Status {
	case operation.StatusReady, operation.StatusAwaiting, operation.StatusCanceling:
		text = "The shell call is still running."
	case operation.StatusFailed:
		text = "Error: " + state.TerminalError
	case operation.StatusCanceled:
		text = "Error: the shell call was canceled."
	case operation.StatusCompleted:
		text = state.TerminalResult
	}

	return llm.ToolResult{CallID: callID, Output: []llm.ToolResultOutput{{Kind: llm.ToolResultText, Value: text}}}, true
}

// withShells offers Bash's background argument and the shell tools, and
// resolves the tools' names also when they are not offered, so a session
// with past calls resumes where they are not.
func withShells(r tool.Registry, offered bool, disallowed []string) tool.Registry {
	return shellRegistry{Registry: r, offered: offered, disallowed: disallowed}
}

type shellRegistry struct {
	tool.Registry

	offered    bool
	disallowed []string
}

func (r shellRegistry) StaticDefinitions() []tool.Definition {
	defs := r.Registry.StaticDefinitions()
	if !r.offered {
		return defs
	}
	for i, d := range defs {
		if d.Tool.Name == tool.BashName {
			defs[i].Tool = bashWithBackground(d.Tool)
		}
	}
	waitParams := objectSchema(map[string]any{
		"shell_ids":  property("array", "The background shells to wait for, such as [\"shell-1\"].", "items", map[string]any{schemaType: jsonString}),
		"timeout_ms": property("number", fmt.Sprintf("How long to wait, in milliseconds: %d by default and at most, %d at least.", maxShellWait.Milliseconds(), minShellWait.Milliseconds())),
	}, "shell_ids")
	killParams := objectSchema(map[string]any{
		"shell_id": property(jsonString, "The background shell to stop, such as \"shell-1\"."),
	}, "shell_id")

	defs = append(defs, tool.Definition{Tool: llm.Tool{Type: llm.ToolFunction, Name: toolWaitShell, Description: waitShellDescription, Parameters: waitParams}})
	if !slices.Contains(r.disallowed, toolKillShell) {
		defs = append(defs, tool.Definition{Tool: llm.Tool{Type: llm.ToolFunction, Name: toolKillShell, Description: killShellDescription, Parameters: killParams}})
	}

	return defs
}

// schemaProperties is a JSON schema's properties key.
const schemaProperties = "properties"

// objectSchema is a JSON schema object with these properties and one
// required.
func objectSchema(props map[string]any, required string) map[string]any {
	return map[string]any{schemaType: "object", schemaProperties: props, "required": []any{required}}
}

// bashWithBackground is Bash with the background argument.
func bashWithBackground(t llm.Tool) llm.Tool {
	params := maps.Clone(t.Parameters)
	props, _ := params[schemaProperties].(map[string]any)
	props = maps.Clone(props)
	props[argBackground] = property("boolean", backgroundDescription)
	params[schemaProperties] = props
	t.Parameters = params

	return t
}

func (r shellRegistry) Resolve(name string) (tool.Translator, bool) {
	if name != toolWaitShell && name != toolKillShell {
		return r.Registry.Resolve(name)
	}

	return shellTranslator{name: name, offered: r.offered && !slices.Contains(r.disallowed, name)}, true
}

// shellTranslator submits a wait_shell or kill_shell call as a shell job.
type shellTranslator struct {
	name    string
	offered bool
}

func (t shellTranslator) Translate(tc tool.Context, call llm.ToolCall) tool.CallStatus {
	if !t.offered {
		return tool.ErrorStatus(fmt.Sprintf("tool %q is not available in this session", t.name), 0)
	}
	var args struct {
		IDs       []string `json:"shell_ids"`
		ID        string   `json:"shell_id"`
		TimeoutMS *float64 `json:"timeout_ms"`
	}
	if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil {
		return tool.ErrorStatus("the arguments must be a JSON object: "+err.Error(), 0)
	}
	if t.name == toolKillShell {
		if args.ID == "" {
			return tool.ErrorStatus(`"shell_id" must be set`, 0)
		}

		return submitShellPlan(tc, shellPlan{Op: shellOpKill, IDs: []string{args.ID}})
	}
	if len(args.IDs) == 0 {
		return tool.ErrorStatus(`"shell_ids" must name at least one shell`, 0)
	}
	timeout := maxShellWait
	if args.TimeoutMS != nil {
		timeout = min(max(time.Duration(*args.TimeoutMS)*time.Millisecond, minShellWait), maxShellWait)
	}

	return submitShellPlan(tc, shellPlan{Op: shellOpWait, IDs: args.IDs, TimeoutMS: timeout.Milliseconds()})
}

func (t shellTranslator) TranslateResult(callID string, status tool.CallStatus, ops []operation.Operation) (llm.ToolResult, error) {
	if status.Error != "" {
		return llm.ToolResult{CallID: callID, Output: []llm.ToolResultOutput{{Kind: llm.ToolResultText, Value: "Error: " + status.Error}}}, nil
	}
	if result, ok := shellJobResult(callID, status, ops); ok {
		return result, nil
	}

	return llm.ToolResult{}, fmt.Errorf("shell call %q has no shell job", callID)
}

// plainBackground is Bash outside any sandbox (no sandbox configured) with
// the background argument: such a call starts a shell with the real shell
// when the run offers background shells. A shell call's result reads
// either way, so a session with past ones loads in a run that offers none.
type plainBackground struct {
	tool.Translator

	shell, dir string
	offered    bool
}

func (b plainBackground) Translate(tc tool.Context, call llm.ToolCall) tool.CallStatus {
	if b.offered && backgroundRequested(call.Arguments) {
		return startShell(tc, call, b.shell, b.dir)
	}

	return b.Translator.Translate(tc, call)
}

func (b plainBackground) TranslateResult(callID string, status tool.CallStatus, ops []operation.Operation) (llm.ToolResult, error) {
	if result, ok := shellJobResult(callID, status, ops); ok {
		return result, nil
	}

	return b.Translator.TranslateResult(callID, status, ops)
}

var errShellInterrupted = errors.New("interrupted: the run stopped while this shell call was running, and it was not repeated")

// shellJobs is the runner's RemoteJobHandler for background shells: a
// start returns once the command runs, a wait on its own goroutine.
type shellJobs struct {
	ctx    context.Context
	shells *shells
	// session is the run's session's shells (shells.attach), which its
	// starts go to.
	session   *sessionShells
	sessionID string
	logDir    string
	updates   chan operation.Operation

	mu      sync.Mutex
	cancels map[operation.ID]context.CancelFunc
}

func newShellJobs(ctx context.Context, s *shells, session *sessionShells, sessionID, logDir string) *shellJobs {
	return &shellJobs{ctx: ctx, shells: s, session: session, sessionID: sessionID, logDir: logDir, updates: make(chan operation.Operation), cancels: map[operation.ID]context.CancelFunc{}}
}

func (*shellJobs) RemoteJobPlanType() operation.RemoteJobPlanType       { return shellPlanType }
func (*shellJobs) RemoteJobPlanVersion() operation.RemoteJobPlanVersion { return shellPlanVersion }
func (j *shellJobs) RemoteJobUpdates() <-chan operation.Operation       { return j.updates }

// AddRemoteJob runs a call. A call that had already started before a
// restart fails instead of running twice.
func (j *shellJobs) AddRemoteJob(op operation.Operation) error {
	state, err := operation.DecodeRemoteJobState(op)
	if err != nil {
		return err // the runner's own error
	}
	var plan shellPlan
	if err := json.Unmarshal(state.Plan.Data, &plan); err != nil {
		return err // the operation manager reports it
	}
	if op.Status != operation.StatusReady {
		go j.finish(operation.FailRemoteJob(op, errShellInterrupted))

		return nil
	}
	ctx, cancel := context.WithCancel(j.ctx)
	j.mu.Lock()
	j.cancels[op.ID] = cancel
	j.mu.Unlock()
	go j.run(ctx, op, state, plan)

	return nil
}

// CancelRemoteJob cancels a running call; it then reports canceled. A
// started shell runs on: only kill_shell stops it.
func (j *shellJobs) CancelRemoteJob(id operation.ID, _ string) error {
	j.mu.Lock()
	cancel := j.cancels[id]
	j.mu.Unlock()
	if cancel != nil {
		cancel()
	}

	return nil
}

func (j *shellJobs) run(ctx context.Context, op operation.Operation, state operation.RemoteJobState, plan shellPlan) {
	defer func() {
		j.mu.Lock()
		j.cancels[op.ID]()
		delete(j.cancels, op.ID)
		j.mu.Unlock()
	}()
	step, err := operation.UpdateRemoteJob(op, state, operation.StatusAwaiting)
	if err != nil || !j.send(*step.Operation) {
		return
	}
	op = *step.Operation
	out, err := j.call(ctx, plan)
	switch {
	case ctx.Err() != nil && j.ctx.Err() == nil && plan.Op != shellOpStart: // canceled by the coordinator; a started shell is reported
		j.finish(operation.CancelRemoteJob(op))
	case err != nil:
		j.finish(operation.FailRemoteJob(op, err))
	default:
		state.TerminalResult = out
		j.finish(operation.UpdateRemoteJob(op, state, operation.StatusCompleted))
	}
}

func (j *shellJobs) call(ctx context.Context, plan shellPlan) (string, error) {
	switch plan.Op {
	case shellOpStart:
		if j.session == nil {
			return "", errors.New("background shells are not available in this run")
		}
		sh, err := j.shells.start(j.session, plan.CallID, plan.Shell, plan.Command, plan.Directory, j.logDir)
		if err != nil {
			return "", err
		}

		return fmt.Sprintf("Background shell %s started (process group %d). It runs on after this call; its output goes to %s, which you can read with tail or grep. "+
			"You will get a <background_shell_notification> when it exits, so do not poll it; wait_shell waits for it and kill_shell stops it.", sh.id, sh.group, sh.log), nil
	case shellOpWait:
		ends, timedOut, err := j.shells.waitFor(ctx, time.Duration(plan.TimeoutMS)*time.Millisecond, j.sessionID, plan.IDs)
		if err != nil {
			return "", err
		}
		data, err := json.Marshal(struct {
			Shells   any  `json:"shells"`
			TimedOut bool `json:"timed_out"`
		}{ends, timedOut})

		return string(data), err
	case shellOpKill:
		if len(plan.IDs) != 1 {
			return "", errors.New("kill_shell names one shell")
		}
		end, err := j.shells.stop(j.sessionID, plan.IDs[0], true)
		if err != nil {
			return "", err
		}
		data, err := json.Marshal(end)

		return string(data), err
	}

	return "", fmt.Errorf("unknown shell call %q", plan.Op)
}

// finish sends a step's operation; a step that cannot be built was already
// validated when the job was added, so it is dropped.
func (j *shellJobs) finish(step operation.Step, err error) {
	if err == nil && step.Operation != nil {
		j.send(*step.Operation)
	}
}

func (j *shellJobs) send(op operation.Operation) bool {
	select {
	case j.updates <- op:
		return true
	case <-j.ctx.Done():
		return false
	}
}
