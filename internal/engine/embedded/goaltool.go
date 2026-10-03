package embedded

import (
	"context"
	"encoding/json"
	"encoding/json/jsontext"
	"fmt"
	"slices"

	"github.com/viktordanov/uah-core/harness/llm"
	"github.com/viktordanov/uah-core/harness/operation"
	"github.com/viktordanov/uah-core/harness/tool"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/goal"
)

// Codex's goal tools (codex-rs ext/goal/src/spec.rs and tool.rs):
// get_goal, create_goal, and update_goal, with Codex's names,
// descriptions, and schemas (internal/goal). The session owns the goal, so
// a call's decision (gatedTranslator) hands it to the session
// (engine.Options.Goal), which applies it on its loop and returns the
// result; the call then runs as a remote job that completes at once with
// that result, which the session file keeps. A job a restart left
// unfinished completes the same way without applying the call twice. See
// docs/design/goal.md.

const goalPlanVersion operation.RemoteJobPlanVersion = 1

// goalNotRoot refuses a call from a subagent: a goal belongs to the main
// agent's session and no child inherits it.
const goalNotRoot = "goals are only available in the main agent's session"

// goalParameters are the tools' schemas as the runner's tool takes them.
var goalParameters = func() map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, t := range goal.Tools() {
		var params map[string]any
		if err := json.Unmarshal([]byte(t.Schema), &params); err != nil {
			panic(fmt.Sprintf("invalid %s schema: %v", t.Name, err)) // a constant
		}
		out[t.Name] = params
	}

	return out
}()

// goalRegistry offers the goal tools, and resolves their names even when
// they are not offered, so a session with past calls resumes anywhere.
type goalRegistry struct {
	tool.Registry

	t goalTranslator
}

func withGoals(r tool.Registry, t goalTranslator) tool.Registry {
	return goalRegistry{Registry: r, t: t}
}

func (r goalRegistry) StaticDefinitions() []tool.Definition {
	defs := r.Registry.StaticDefinitions()
	if r.t.offered {
		for _, t := range goal.Tools() {
			defs = append(defs, tool.Definition{Tool: llm.Tool{
				Type: llm.ToolFunction, Name: t.Name, Description: t.Description, Parameters: goalParameters[t.Name],
			}})
		}
	}

	return defs
}

func (r goalRegistry) Resolve(name string) (tool.Translator, bool) {
	if t, ok := r.Registry.Resolve(name); ok || !slices.Contains(goal.ToolNames, name) {
		return t, ok
	}

	return r.t, true
}

// offersGoals reports whether the run's session gets the goal tools: with
// Config.Goals, in the main agent's session, and in a child forked from
// it, which keeps its parent's tools so its requests keep the parent's
// prompt cache, and is refused at the call. A scope or the request can
// leave them out.
func (w *wiring) offersGoals(req core.Request) bool {
	if !w.e.cfg.Goals || slices.ContainsFunc(goal.ToolNames, func(n string) bool { return slices.Contains(req.DisallowedTools, n) }) {
		return false
	}
	if !isSubagent(req.SessionID) {
		return true
	}
	f, ok := w.e.cfg.Subagents.(interface{ Forked(sessionID string) bool })

	return ok && f.Forked(req.SessionID)
}

// goalTranslator hands each call to the session, then submits its result
// as a job.
type goalTranslator struct {
	offered bool
	// root is the main agent's session; only it has a goal.
	root bool
	// ctx bounds a call decided in Translate: the run's approvals, ended
	// early by an interrupt.
	ctx  context.Context
	goal engine.GoalTool
}

func (t goalTranslator) Translate(ctx tool.Context, call llm.ToolCall) tool.CallStatus {
	return t.decide(t.ctx, call)(ctx)
}

// decide applies the call to the session's goal.
func (t goalTranslator) decide(ctx context.Context, call llm.ToolCall) submit {
	switch {
	case !t.offered:
		return refuse(tool.ErrorStatus(fmt.Sprintf("tool %q is not available in this session", call.Name), 0))
	case !t.root || t.goal == nil:
		return refuse(tool.ErrorStatus(goalNotRoot, 0))
	}
	result, err := t.goal(ctx, call.Name, call.Arguments)
	if err != nil {
		return refuse(tool.ErrorStatus(err.Error(), 0))
	}
	data, err := json.Marshal(engine.GoalPlan{Tool: call.Name, Result: result})
	if err != nil {
		return refuse(tool.ErrorStatus(fmt.Sprintf("failed to encode the goal's result: %v", err), 0))
	}
	spec, err := operation.NewRemoteJobSpec(operation.RemoteJobPlan{Type: engine.GoalPlanType, Version: goalPlanVersion, Data: jsontext.Value(data)})
	if err != nil {
		return refuse(tool.ErrorStatus(fmt.Sprintf("failed to build the goal's job: %v", err), 0))
	}

	return func(tc tool.Context) tool.CallStatus {
		return tool.CallStatus{WaitingFor: []operation.ID{tc.Submit(spec)}}
	}
}

func (goalTranslator) TranslateResult(callID string, status tool.CallStatus, ops []operation.Operation) (llm.ToolResult, error) {
	text := status.Error
	if text == "" {
		var err error
		if text, err = goalJobText(callID, ops); err != nil {
			return llm.ToolResult{CallID: callID}, err
		}
	}

	return llm.ToolResult{CallID: callID, Output: []llm.ToolResultOutput{{Kind: llm.ToolResultText, Value: text}}}, nil
}

// goalJobText is the result of a goal call's job: the session's result
// once it completed.
func goalJobText(callID string, ops []operation.Operation) (string, error) {
	if len(ops) != 1 {
		return "", fmt.Errorf("goal call %q has %d operations, want 1", callID, len(ops))
	}
	state, err := operation.DecodeRemoteJobState(ops[0])
	if err != nil {
		return "", fmt.Errorf("failed to decode goal call %q: %w", callID, err)
	}
	switch ops[0].Status {
	case operation.StatusCompleted:
		return state.TerminalResult, nil
	case operation.StatusFailed:
		return state.TerminalError, nil
	case operation.StatusCanceled:
		return "The goal call was cancelled.", nil
	case operation.StatusReady, operation.StatusAwaiting, operation.StatusCanceling:
	}

	return "The goal's update is still being recorded.", nil
}

// goalJobs is the runner's RemoteJobHandler for goal calls: a job
// completes at once with the result the session returned.
type goalJobs struct {
	ctx     context.Context
	updates chan operation.Operation
}

func newGoalJobs(ctx context.Context) *goalJobs {
	return &goalJobs{ctx: ctx, updates: make(chan operation.Operation)}
}

func (*goalJobs) RemoteJobPlanType() operation.RemoteJobPlanType { return engine.GoalPlanType }

func (*goalJobs) RemoteJobPlanVersion() operation.RemoteJobPlanVersion { return goalPlanVersion }

func (j *goalJobs) RemoteJobUpdates() <-chan operation.Operation { return j.updates }

// CancelRemoteJob has nothing to stop: a job completes at once.
func (*goalJobs) CancelRemoteJob(operation.ID, string) error { return nil }

func (j *goalJobs) AddRemoteJob(op operation.Operation) error {
	state, err := operation.DecodeRemoteJobState(op)
	if err != nil {
		return err // the runner's own error
	}
	var plan engine.GoalPlan
	if err := json.Unmarshal(state.Plan.Data, &plan); err != nil {
		return err // the operation manager reports it
	}
	go j.run(op, state, plan)

	return nil
}

func (j *goalJobs) run(op operation.Operation, state operation.RemoteJobState, plan engine.GoalPlan) {
	if op.Status == operation.StatusReady {
		step, err := operation.UpdateRemoteJob(op, state, operation.StatusAwaiting)
		if err != nil || !j.send(*step.Operation) {
			return
		}
		op = *step.Operation
	}
	state.TerminalResult = plan.Result
	step, err := operation.UpdateRemoteJob(op, state, operation.StatusCompleted)
	if err == nil && step.Operation != nil {
		j.send(*step.Operation)
	}
}

func (j *goalJobs) send(op operation.Operation) bool {
	select {
	case j.updates <- op:
		return true
	case <-j.ctx.Done():
		return false
	}
}
