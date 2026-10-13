package state

import (
	"fmt"
	"strings"
	"time"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/session"
)

// onAgentUpdated shows a subagent as one line, added where it was spawned
// and updated in place: KindAgent with Name the nickname, Label the role,
// and Detail its state. A /review's reviewer gets no line: its review's
// item shows it.
func (s *State) onAgentUpdated(e engine.AgentUpdated) {
	if e.Review {
		return
	}
	key := "agent:" + e.ID
	set := func(it *Item) {
		if e.State == engine.AgentCompleted && it.Detail == engine.AgentRunning && !e.Started.IsZero() {
			it.Duration = e.At.Sub(e.Started)
		}
		if e.Message == "" && it.Agent != nil && settle(it.Detail, e.State) == engine.AgentErrored {
			e.Message = it.Agent.Message // a shutdown keeps why the child failed
		}
		it.Detail, it.Started, it.Agent = settle(it.Detail, e.State), e.Started, &e
		if e.State != engine.AgentRunning {
			for i := range it.Sub {
				if it.Sub[i].Tool == ToolCalled || it.Sub[i].Tool == ToolRunning {
					it.Sub[i].Tool = ToolStopped
				}
			}
		}
	}
	if s.update(key, set) {
		return
	}
	it := Item{Kind: KindAgent, Key: key, Name: e.Nickname, Label: e.Role, Text: e.ID}
	set(&it)
	s.put(it)
	s.agentIDs = append(s.agentIDs, e.ID)
	s.labelSpawn(e)
}

// maxAgentTools is how many of a subagent's latest tool calls its line
// keeps for the detailed view.
const maxAgentTools = 30

// onAgentActivity folds a subagent's tool event into its line's tool calls.
func (s *State) onAgentActivity(e engine.AgentActivity) {
	s.update("agent:"+e.ID, func(it *Item) {
		switch v := e.Event.(type) {
		case core.ToolCalled:
			it.Sub = append(it.Sub, Item{Kind: KindTool, Key: v.CallID, Name: v.Name, Label: v.Label, Tool: ToolCalled, Started: v.At})
			it.Sub = it.Sub[max(len(it.Sub)-maxAgentTools, 0):]
		case core.ToolStarted:
			subTool(it, v.CallID, func(t *Item) { t.Tool, t.Started = ToolRunning, v.At })
		case core.ToolFinished:
			state := ToolOK
			if !v.OK {
				state = ToolFailed
			}
			subTool(it, v.CallID, func(t *Item) { t.Tool, t.Detail, t.Duration = state, v.Detail, v.Duration })
		}
	})
}

func subTool(it *Item, callID string, fn func(*Item)) {
	for i := range it.Sub {
		if it.Sub[i].Key == callID {
			fn(&it.Sub[i])
		}
	}
}

// listAgents lists the session's subagents with their state.
func listAgents(s *State) {
	var b strings.Builder
	for _, it := range s.Agents() {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "%s", it.Name)
		if it.Label != "" {
			fmt.Fprintf(&b, " (%s)", it.Label)
		}
		fmt.Fprintf(&b, " · %s", it.Detail)
		if it.Agent != nil && it.Agent.Message != "" {
			fmt.Fprintf(&b, ": %s", it.Agent.Message)
		}
		if it.Detail == engine.AgentRunning {
			fmt.Fprintf(&b, " %s", s.Now.Sub(it.Started).Round(time.Second))
		}
		fmt.Fprintf(&b, " · %s", it.Text)
	}
	if b.Len() == 0 {
		s.notice(session.LevelInfo, "no subagents in this session; the agent starts them with spawn_agent when you ask it to delegate")

		return
	}
	b.WriteString("\n/agents <name> shows one's transcript as it works; alt+← and alt+→ switch agents")
	s.notice(session.LevelInfo, b.String())
}
