package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/viktordanov/uah/internal/engine"
)

// The tool names, Codex's v1 set.
const (
	ToolSpawn  = "spawn_agent"
	ToolSend   = "send_input"
	ToolWait   = "wait_agent"
	ToolClose  = "close_agent"
	ToolResume = "resume_agent"
	// toolWaitBefore is what uah called wait_agent before; the name still
	// resolves, so a session with past calls resumes, but it is not offered.
	toolWaitBefore = "wait"
)

// wait_agent's timeout bounds. Codex's are 30 s by default, 10 s to 1 h;
// its parents polled with 10 s waits. A wait without a timeout waits as long
// as one can: just under the embedded engine's 5-minute wake hold
// (embedded.wakeHold), which would otherwise wake the parent only to say the
// wait is still running.
const (
	waitMax     = 4*time.Minute + 30*time.Second
	waitDefault = waitMax
	waitMin     = time.Minute
)

// waitNote is a timed-out wait's note: the agents are fine, wait again.
const waitNote = "No agent finished during this wait. They are still working, and nothing is wrong: call wait_agent again to keep waiting. A message does not make an agent finish sooner."

// tool is one subagent tool: what the model is offered and how a call runs.
// A new tool is one more entry in tools.
type tool struct {
	name        string
	description func(m *Manager) string
	schema      string
	run         func(ctx context.Context, m *Manager, call engine.AgentCall) (any, error)
}

var tools = []tool{
	{ToolSpawn, func(m *Manager) string { return spawnDescription(m.cfg.Roles) }, spawnSchema, runSpawn},
	{ToolSend, fixed(sendDescription), sendSchema, runSend},
	{ToolWait, fixed(waitDescription), waitSchema, runWait},
	{ToolClose, fixed(closeDescription), closeSchema, runClose},
	{ToolResume, fixed(resumeDescription), resumeSchema, runResume},
}

func fixed(s string) func(*Manager) string { return func(*Manager) string { return s } }

// ToolNames are the tools' names and the names past calls may use.
func (m *Manager) ToolNames() []string { return ToolNames() }

// ToolNames are the subagent tools' names and the names past calls may use.
func ToolNames() []string {
	names := []string{toolWaitBefore}
	for _, t := range tools {
		names = append(names, t.name)
	}

	return names
}

// definitions are the tools as the engine offers them.
func (m *Manager) definitions() []engine.AgentTool {
	out := make([]engine.AgentTool, 0, len(tools))
	for _, t := range tools {
		var params map[string]any
		if err := json.Unmarshal([]byte(t.schema), &params); err != nil {
			panic(fmt.Sprintf("invalid %s schema: %v", t.name, err)) // a constant
		}
		out = append(out, engine.AgentTool{Name: t.name, Description: t.description(m), Parameters: params})
	}

	return out
}

// Call runs one tool call and returns its JSON result.
func (m *Manager) Call(ctx context.Context, call engine.AgentCall) (string, error) {
	for _, t := range tools {
		if t.name != call.Tool {
			continue
		}
		out, err := t.run(ctx, m, call)
		if err != nil {
			return "", err
		}
		data, err := json.Marshal(out)
		if err != nil {
			return "", fmt.Errorf("failed to encode the result: %w", err)
		}

		return string(data), nil
	}

	return "", fmt.Errorf("unknown agent tool %q", call.Tool)
}

// Arguments and results of the tools.
type (
	spawnArgs struct {
		Message   string `json:"message"`
		AgentType string `json:"agent_type"`
		Model     string `json:"model"`
		Effort    string `json:"reasoning_effort"`
		// ForkContext starts the child from a copy of the parent's history.
		ForkContext bool `json:"fork_context"`
	}
	spawnResult struct {
		AgentID  string `json:"agent_id"`
		Nickname string `json:"nickname"`
	}
	sendArgs struct {
		Target    string `json:"target"`
		Message   string `json:"message"`
		Interrupt bool   `json:"interrupt"`
	}
	sendResult struct {
		SubmissionID string `json:"submission_id"`
	}
	waitArgs struct {
		Targets   []string `json:"targets"`
		TimeoutMS *float64 `json:"timeout_ms"`
	}
	waitResult struct {
		Status   map[string]Status `json:"status"`
		TimedOut bool              `json:"timed_out"`
		Note     string            `json:"note,omitempty"`
	}
	closeArgs struct {
		Target string `json:"target"`
	}
	closeResult struct {
		PreviousStatus Status `json:"previous_status"`
	}
	resumeArgs struct {
		ID string `json:"id"`
	}
	resumeResult struct {
		Status Status `json:"status"`
	}
)

func runSpawn(ctx context.Context, m *Manager, call engine.AgentCall) (any, error) {
	a, err := decode[spawnArgs](call.Args)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(a.Message) == "" {
		return nil, errEmptyMessage
	}

	return m.spawn(ctx, call, a)
}

func runSend(_ context.Context, m *Manager, call engine.AgentCall) (any, error) {
	parentID := call.ParentID
	a, err := decode[sendArgs](call.Args)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(a.Message) == "" {
		return nil, errEmptyMessage
	}
	id, err := m.send(parentID, a.Target, a.Message, a.Interrupt)

	return sendResult{SubmissionID: id}, err
}

func runWait(ctx context.Context, m *Manager, call engine.AgentCall) (any, error) {
	parentID := call.ParentID
	a, err := decode[waitArgs](call.Args)
	if err != nil {
		return nil, err
	}
	if len(a.Targets) == 0 {
		return nil, errors.New("agent ids must be non-empty")
	}
	timeout := waitDefault
	if a.TimeoutMS != nil {
		if *a.TimeoutMS <= 0 {
			return nil, errors.New("timeout_ms must be greater than zero")
		}
		timeout = min(max(time.Duration(*a.TimeoutMS)*time.Millisecond, waitMin), waitMax)
	}
	statuses, timedOut, err := m.wait(ctx, parentID, a.Targets, timeout)

	return newWaitResult(statuses, timedOut), err
}

// newWaitResult is wait_agent's result; a timed-out one says the agents
// are fine.
func newWaitResult(statuses map[string]Status, timedOut bool) waitResult {
	res := waitResult{Status: bound(statuses), TimedOut: timedOut}
	if timedOut {
		res.Note = waitNote
	}

	return res
}

func runClose(_ context.Context, m *Manager, call engine.AgentCall) (any, error) {
	parentID := call.ParentID
	a, err := decode[closeArgs](call.Args)
	if err != nil {
		return nil, err
	}
	prev, err := m.closeAgent(parentID, a.Target)

	return closeResult{PreviousStatus: prev}, err
}

func runResume(ctx context.Context, m *Manager, call engine.AgentCall) (any, error) {
	parentID := call.ParentID
	a, err := decode[resumeArgs](call.Args)
	if err != nil {
		return nil, err
	}
	status, err := m.resume(ctx, parentID, strings.TrimSpace(a.ID))

	return resumeResult{Status: status}, err
}

var errEmptyMessage = errors.New("empty message can't be sent to an agent")

// decode decodes a tool's arguments.
func decode[A any](raw json.RawMessage) (A, error) {
	var a A
	if err := json.Unmarshal(raw, &a); err != nil {
		return a, fmt.Errorf("invalid arguments: %w", err)
	}

	return a, nil
}
