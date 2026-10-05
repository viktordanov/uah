package bench

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/viktordanov/uah/internal/sessionfile"
)

// AgentUse counts how a uah run's main agent treated its subagents, from
// the session files under its uah-state: what it sent them, how it waited,
// and what it spent while they ran. The mining of the owner's sessions
// found parents polling with short waits and messaging running agents for
// status (docs/design/subagents.md, "Leaving agents alone").
type AgentUse struct {
	// Spawns, Messages (send_input without interrupt), Interrupts
	// (send_input with it), Closes, and Resumes are the main agent's agent
	// calls; ToRunning are the messages and interrupts that reached a child
	// while it worked, and ClosedRunning the closes of a working child.
	Spawns        int `json:"spawns"`
	Messages      int `json:"messages"`
	Interrupts    int `json:"interrupts"`
	ToRunning     int `json:"to_running"`
	Closes        int `json:"closes"`
	ClosedRunning int `json:"closed_running"`
	Resumes       int `json:"resumes"`
	// Waits are the wait_agent calls and WaitsTimedOut those that ended
	// without a finished agent.
	Waits         int `json:"waits"`
	WaitsTimedOut int `json:"waits_timed_out"`
	// Notes are the <subagent_notification> messages the main agent got.
	Notes int `json:"notes"`
	// BusyRequests are the main agent's model requests while at least one
	// child worked, with their tokens; AgentOnlyRequests those of them whose
	// only calls were wait_agent, send_input, or close_agent.
	BusyRequests      int    `json:"busy_requests"`
	BusyTokens        Tokens `json:"busy_tokens"`
	AgentOnlyRequests int    `json:"agent_only_requests"`
}

// Interventions are the messages, interrupts, and closes the main agent
// sent to working children.
func (a AgentUse) Interventions() int { return a.ToRunning + a.ClosedRunning }

// agentOp is the part of an agent call's operation the counts read.
type agentOp struct {
	ID     string
	Status string
	State  struct {
		TerminalResult string
	}
}

// workSpan is a time a child worked.
type workSpan struct{ from, to time.Time }

// CountAgentUse reads the main session and its children under stateDir.
func CountAgentUse(stateDir, mainSession string) (AgentUse, error) {
	var a AgentUse
	if mainSession == "" {
		return a, nil
	}
	dir := filepath.Join(stateDir, "sessions")
	files, err := filepath.Glob(filepath.Join(dir, "*.session.jsonl"))
	if err != nil {
		return a, err
	}
	busy := map[string][]workSpan{} // by child
	for _, path := range files {
		id := strings.TrimSuffix(filepath.Base(path), ".session.jsonl")
		if id == mainSession || parentOf(dir, id) != mainSession {
			continue
		}
		_, page, err := sessionfile.Read(path, sessionfile.BeforeFirst, 0)
		if err != nil {
			return a, err
		}
		busy[id] = childSpans(page.Items)
	}
	_, page, err := sessionfile.Read(filepath.Join(dir, mainSession+".session.jsonl"), sessionfile.BeforeFirst, 0)
	if err != nil {
		return a, err
	}
	a.count(page.Items, func(child string, at time.Time) bool {
		for id, spans := range busy {
			if (child == "" || id == child) && slices.ContainsFunc(spans, func(s workSpan) bool { return !at.Before(s.from) && !at.After(s.to) }) {
				return true
			}
		}

		return false
	})

	return a, nil
}

// Used says whether the main agent spawned or resumed any subagent.
func (a AgentUse) Used() bool { return a.Spawns > 0 || a.Resumes > 0 }

// parentOf is the parent a session's sidecar names, or "".
func parentOf(dir, id string) string {
	data, err := os.ReadFile(filepath.Join(dir, id+".uah.json"))
	if err != nil {
		return ""
	}
	var sc struct {
		Parent string `json:"parent"`
	}
	_ = json.Unmarshal(data, &sc)

	return sc.Parent
}

// childSpans are the times a child worked: from a message that reached it
// idle to its next answer, a response without tool calls. A message that
// comes while it works extends its work.
func childSpans(items []sessionfile.Item) []workSpan {
	var out []workSpan
	var cur *workSpan
	for _, it := range items {
		switch it.Kind {
		case sessionfile.KindInput:
			var in sessionfile.Input
			if cur == nil && it.Decode(&in) == nil && in.Kind == sessionfile.InputExternal {
				cur = &workSpan{from: it.RecordedAt}
			}
		case sessionfile.KindModelResponse:
			var r sessionfile.ModelResponse
			if cur == nil || it.Decode(&r) != nil {
				continue
			}
			cur.to = it.RecordedAt
			if !slices.ContainsFunc(r.Response.Output, func(o sessionfile.Output) bool { return o.Type == sessionfile.OutputToolCall }) {
				out = append(out, *cur)
				cur = nil
			}
		}
	}
	if cur != nil && !cur.to.IsZero() {
		out = append(out, *cur) // interrupted, or still working when the run ended
	}

	return out
}

// count reads the main session's items; working says whether the child
// (any child for "") worked at a time.
func (a *AgentUse) count(items []sessionfile.Item, working func(child string, at time.Time) bool) {
	ops := map[string]agentOp{}
	calls := map[string][]string{} // a call's operations
	var waits []string
	for _, it := range items {
		for _, raw := range it.Operations {
			var op agentOp
			if json.Unmarshal(raw, &op) == nil && (terminal(op.Status) || ops[op.ID].Status == "") {
				ops[op.ID] = op
			}
		}
		switch it.Kind {
		case sessionfile.KindInput:
			a.input(it)
		case sessionfile.KindToolCallStatus:
			var st sessionfile.ToolCallStatus
			if it.Decode(&st) != nil {
				continue
			}
			for _, w := range st.Status.WaitingFor {
				if !slices.Contains(calls[st.CallID], w) {
					calls[st.CallID] = append(calls[st.CallID], w)
				}
			}
		case sessionfile.KindModelResponse:
			waits = append(waits, a.response(it, working)...)
		}
	}
	for _, id := range waits {
		for _, op := range calls[id] {
			if strings.Contains(ops[op].State.TerminalResult, `"timed_out":true`) {
				a.WaitsTimedOut++
			}
		}
	}
}

// input counts a notification among the main agent's inputs.
func (a *AgentUse) input(it sessionfile.Item) {
	var in sessionfile.Input
	if it.Decode(&in) != nil || (in.Kind != sessionfile.InputExternal && in.Kind != sessionfile.InputDeveloper) {
		return
	}
	if text, err := in.Text(); err == nil && strings.Contains(text, "<subagent_notification>") {
		a.Notes++
	}
}

// response counts one model response of the main agent and returns its
// wait_agent calls' IDs.
func (a *AgentUse) response(it sessionfile.Item, working func(child string, at time.Time) bool) []string {
	var r sessionfile.ModelResponse
	if it.Decode(&r) != nil {
		return nil
	}
	at := it.RecordedAt
	var waits []string
	agentOnly, calls := true, 0
	for _, o := range r.Response.Output {
		var tc sessionfile.ToolCall
		if o.Type != sessionfile.OutputToolCall || o.Decode(&tc) != nil {
			continue
		}
		calls++
		if !a.call(tc, func(child string) bool { return working(child, at) }) {
			agentOnly = false
		}
		if tc.Name == toolWaitAgent {
			waits = append(waits, tc.CallID)
		}
	}
	if working("", at) {
		u := r.Response.Usage
		a.BusyRequests++
		a.BusyTokens = a.BusyTokens.Add(Tokens{Input: u.InputTokens, Cached: u.CachedInputTokens, Output: u.OutputTokens, Reasoning: u.ReasoningTokens})
		if calls > 0 && agentOnly {
			a.AgentOnlyRequests++
		}
	}

	return waits
}

// call counts one tool call of the main agent and reports whether it
// only waits on or steers an agent; working says whether a child worked
// when the call was made.
func (a *AgentUse) call(tc sessionfile.ToolCall, working func(child string) bool) bool {
	var args struct {
		Target    string `json:"target"`
		Interrupt bool   `json:"interrupt"`
	}
	_ = json.Unmarshal([]byte(tc.Arguments), &args)
	switch tc.Name {
	case "spawn_agent":
		a.Spawns++

		return false
	case "resume_agent":
		a.Resumes++

		return false
	case "send_input":
		if args.Interrupt {
			a.Interrupts++
		} else {
			a.Messages++
		}
		if working(args.Target) {
			a.ToRunning++
		}
	case "close_agent":
		a.Closes++
		if working(args.Target) {
			a.ClosedRunning++
		}
	case toolWaitAgent, "wait":
		a.Waits++
	default:
		return false
	}

	return true
}
