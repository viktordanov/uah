package agents_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/agents"
	"github.com/viktordanov/uah/internal/app"
	"github.com/viktordanov/uah/internal/approval"
	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/engine/embedded"
	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/testing/fakellm"
)

// TestAgents_SpawnWaitAnswer spawns a child, waits for it beside a command
// in the same turn, and gets its answer with the command's; the child
// cannot spawn and is hidden from the resume picker.
func TestAgents_SpawnWaitAnswer(t *testing.T) {
	gate := make(chan struct{})
	childIDs := make(chan string, 1) // the fake model's handler learns the child's ID
	e := newEnv(t, agents.Config{},
		fakellm.Reply{Calls: []fakellm.Call{call("spawn_agent", `{"message":"CHILD-A count the files"}`)}},
		fakellm.Reply{From: func(req fakellm.Request) fakellm.Reply {
			id := ids(req)[0]
			childIDs <- id

			return fakellm.Reply{Commands: []string{"echo side"}, Calls: []fakellm.Call{call("wait_agent", `{"targets":["`+id+`"]}`)}}
		}},
		fakellm.Reply{Text: "the agent counted"},
	)
	e.llm.Route("CHILD-A", fakellm.Reply{Gate: gate, Text: "forty-two"})
	s, ev := e.open(t, false)

	_, err := s.Submit("delegate")
	require.NoError(t, err)
	awaitRequests(t, e, "CHILD-A", 1)
	childID := <-childIDs
	require.Eventually(t, func() bool { return e.mgr.Waiting(s.ID(), childID) == 1 }, waitTimeout, time.Millisecond)
	_, timedOut, _ := e.mgr.Wait(context.Background(), s.ID(), []string{childID}, 50*time.Millisecond)
	close(gate)
	result := ev.finished()

	assert.Equal(t, core.StatusOK, result.Status)
	assert.Equal(t, "the agent counted", result.Answer)
	assert.Contains(t, lastOutputs(e), "side", "the command ran beside the wait, and its result waited for the turn's other call")
	assert.True(t, timedOut, "a wait on a running child times out")
	assert.Contains(t, lastOutputs(e), `{"status":{"`)
	assert.Contains(t, lastOutputs(e), `":{"completed":"forty-two"}},"timed_out":false}`)

	var child fakellm.Request
	for _, r := range e.llm.Requests() {
		if isChild(r) {
			child = r
		} else {
			assert.Contains(t, r.Tools, "spawn_agent")
			assert.Contains(t, r.Tools, "resume_agent")
		}
	}
	assert.NotContains(t, child.Tools, "spawn_agent", "depth 1: a child cannot spawn")
	assert.Equal(t, e.llm.Requests()[0].CacheKey, child.CacheKey, "as in Codex, every agent of a tree uses the root session's prompt cache key")
	assert.Contains(t, child.Tools, "Bash")

	updates := 0
	for _, x := range ev.all {
		if u, ok := x.(engine.AgentUpdated); ok {
			updates++
			assert.Equal(t, "Ada", u.Nickname)
		}
	}
	assert.Positive(t, updates, "the parent's stream shows the child's progress")

	infos, err := session.Sessions(e.StateDir)
	require.NoError(t, err)
	require.Len(t, infos, 2)
	i := slices.IndexFunc(infos, func(in session.Info) bool { return in.ID != s.ID() })
	assert.Equal(t, session.SourceSubagent, infos[i].Source)
	assert.Equal(t, s.ID(), infos[i].Parent)
	assert.True(t, strings.HasPrefix(infos[i].ID, session.SubagentIDPrefix), "a child's ID is subagent-<uuid>: %s", infos[i].ID)
	found, err := app.FindSession(context.Background(), e.StateDir, session.ShortID(infos[i].ID))
	require.NoError(t, err, "uah resume finds a child by the short ID uah sessions prints")
	assert.Equal(t, infos[i].ID, found.ID)
	picker := session.Interactive(infos)
	require.Len(t, picker, 1)
	assert.Equal(t, s.ID(), picker[0].ID)
}

// TestAgents_SendInput gives a finished child another task.
func TestAgents_SendInput(t *testing.T) {
	e := newEnv(t, agents.Config{},
		fakellm.Reply{Calls: []fakellm.Call{call("spawn_agent", `{"message":"CHILD-B first task"}`)}},
		callWith("wait_agent", `{"targets":["ID"]}`),
		callWith("send_input", `{"target":"ID","message":"second task"}`),
		callWith("wait_agent", `{"targets":["ID"]}`),
		fakellm.Reply{Text: "both done"},
	)
	e.llm.Route("CHILD-B", fakellm.Reply{Text: "first answer"}, fakellm.Reply{Text: "second answer"})
	s, ev := e.open(t, false)

	_, err := s.Submit("delegate twice")
	require.NoError(t, err)
	result := ev.finished()

	assert.Equal(t, "both done", result.Answer)
	outputs := lastOutputs(e)
	assert.Contains(t, outputs, `{"completed":"first answer"}`)
	assert.Contains(t, outputs, `{"submission_id":"`)
	assert.Contains(t, outputs, `{"completed":"second answer"}`)
}

// TestAgents_AMessageWhileRunningKeepsBothAnswers sends a running child a
// second message, which it reads after its first run: its completed status
// holds both runs' answers, so the parent does not lose the first.
func TestAgents_AMessageWhileRunningKeepsBothAnswers(t *testing.T) {
	gate := make(chan struct{})
	e := newEnv(t, agents.Config{},
		fakellm.Reply{Calls: []fakellm.Call{call("spawn_agent", `{"message":"CHILD-Q first task"}`)}},
		callWith("send_input", `{"target":"ID","message":"one more thing"}`),
		callWith("wait_agent", `{"targets":["ID"]}`),
		fakellm.Reply{Text: "both done"},
	)
	e.llm.Route("CHILD-Q", fakellm.Reply{Gate: gate, Text: "the inventory"}, fakellm.Reply{Text: "the addendum"})
	s, ev := e.open(t, false)
	_, err := s.Submit("delegate")
	require.NoError(t, err)
	awaitRequests(t, e, "CHILD-Q", 1)
	require.Eventually(t, func() bool { return strings.Contains(lastOutputs(e), "submission_id") }, waitTimeout, time.Millisecond)
	close(gate)

	assert.Equal(t, "both done", ev.finished().Answer)
	assert.Contains(t, lastOutputs(e), `{"completed":"the inventory\n\nthe addendum"}`)
}

// TestAgents_LimitAndClose refuses a spawn over the limit and accepts it
// once an agent is closed.
func TestAgents_LimitAndClose(t *testing.T) {
	e := newEnv(t, agents.Config{MaxThreads: 1},
		fakellm.Reply{Calls: []fakellm.Call{call("spawn_agent", `{"message":"CHILD-C one"}`)}},
		fakellm.Reply{Calls: []fakellm.Call{call("spawn_agent", `{"message":"CHILD-C two"}`)}},
		callWith("close_agent", `{"target":"ID"}`),
		fakellm.Reply{Calls: []fakellm.Call{call("spawn_agent", `{"message":"CHILD-C three"}`)}},
		fakellm.Reply{Text: "done"},
	)
	e.llm.Route("CHILD-C")
	s, ev := e.open(t, false)

	_, err := s.Submit("spawn three")
	require.NoError(t, err)
	ev.finished()

	outputs := lastOutputs(e)
	assert.Contains(t, outputs, "agent limit reached: 1 agents are open")
	assert.Contains(t, outputs, `{"previous_status":`)
	assert.Equal(t, 1, strings.Count(outputs, "agent limit reached"))
	assert.Len(t, ids(lastParent(e)), 2, "the spawn after the close worked")
}

// TestAgents_ChildApprovalAsksTheParent shows a child's escalation in the
// parent's session, labelled with the child's nickname.
func TestAgents_ChildApprovalAsksTheParent(t *testing.T) {
	e := newEnv(t, agents.Config{},
		fakellm.Reply{Calls: []fakellm.Call{call("spawn_agent", `{"message":"CHILD-D fetch"}`)}},
		callWith("wait_agent", `{"targets":["ID"]}`),
		fakellm.Reply{Text: "done"},
	)
	e.llm.Route("CHILD-D", fakellm.Reply{Escalated: []string{"echo fetched"}}, fakellm.Reply{Text: "fetched"})
	s, ev := e.open(t, true, e.sandboxed(t))

	_, err := s.Submit("delegate a fetch")
	require.NoError(t, err)
	req := ev.approval()
	assert.Equal(t, "agent Ada: it needs the network", req.Justification)
	require.NoError(t, s.Resolve(req.ID, approval.Approve))
	result := ev.finished()

	assert.Equal(t, "done", result.Answer)
	assert.Contains(t, lastOutputs(e), `{"completed":"fetched"}`)
	assert.Contains(t, childOutputs(e, "CHILD-D"), "fetched", "the approved command ran")
}

// TestAgents_ParallelSpawnsKeepTheLimit spawns three agents in one turn
// with room for two.
func TestAgents_ParallelSpawnsKeepTheLimit(t *testing.T) {
	e := newEnv(t, agents.Config{MaxThreads: 2},
		fakellm.Reply{Calls: []fakellm.Call{
			call("spawn_agent", `{"message":"CHILD-E one"}`),
			call("spawn_agent", `{"message":"CHILD-E two"}`),
			call("spawn_agent", `{"message":"CHILD-E three"}`),
		}},
		fakellm.Reply{Text: "done"},
	)
	e.llm.Route("CHILD-E")
	s, ev := e.open(t, false)

	_, err := s.Submit("spawn three at once")
	require.NoError(t, err)
	ev.finished()

	outputs := lastOutputs(e)
	assert.Equal(t, 1, strings.Count(outputs, "agent limit reached: 2 agents are open"), outputs)
	assert.Len(t, ids(lastParent(e)), 2)
	assert.Contains(t, outputs, `"nickname":"Ada"`)
	assert.Contains(t, outputs, `"nickname":"Babbage"`)
}

// TestAgents_ToolsWhenOff answers a past agent call with an error when
// subagents are off (max_depth 0), so such a session still resumes.
func TestAgents_ToolsWhenOff(t *testing.T) {
	e := newEnv(t, agents.Config{},
		fakellm.Reply{Calls: []fakellm.Call{call("wait_agent", `{"targets":["x"]}`)}},
		fakellm.Reply{Text: "done"},
	)
	e.mgr = agents.New(agents.Config{})
	s, ev := e.open(t, false)

	_, err := s.Submit("wait")
	require.NoError(t, err)
	assert.Equal(t, "done", ev.finished().Answer)

	assert.NotContains(t, e.llm.Requests()[0].Tools, "spawn_agent")
	assert.Contains(t, lastOutputs(e), `tool "wait_agent" is not available in this session`)
}

// TestAgents_ToolsWithoutSubagents offers no agent tools on an engine
// without subagents.
func TestAgents_ToolsWithoutSubagents(t *testing.T) {
	e := newEnv(t, agents.Config{}, fakellm.Reply{Text: "done"})
	s, ev := e.open(t, false, func(c *embedded.Config) { c.Subagents = nil })

	_, err := s.Submit("hello")
	require.NoError(t, err)
	assert.Equal(t, "done", ev.finished().Answer)
	assert.NotContains(t, e.llm.Requests()[0].Tools, "spawn_agent")
}

// TestAgents_ProgressAfterTheParentsRun reports a child that finishes after
// its parent's run ended.
func TestAgents_ProgressAfterTheParentsRun(t *testing.T) {
	gate := make(chan struct{})
	e := newEnv(t, agents.Config{},
		fakellm.Reply{Calls: []fakellm.Call{call("spawn_agent", `{"message":"CHILD-F slow"}`)}},
		fakellm.Reply{Text: "started it"},
	)
	e.llm.Route("CHILD-F", fakellm.Reply{Gate: gate, Text: "finally"})
	s, ev := e.open(t, false)

	_, err := s.Submit("start one")
	require.NoError(t, err)
	assert.Equal(t, "started it", ev.finished().Answer)
	close(gate)
	done := ev.agentState(engine.AgentCompleted)

	assert.Equal(t, "Ada", done.Nickname)
}

// TestAgents_NotifyTheParentWhenAChildEnds sends Codex's
// <subagent_notification> to the parent when a child finishes that the
// parent did not wait for: the parent is idle then, so it goes with the
// parent's next message and starts no run of its own.
func TestAgents_NotifyTheParentWhenAChildEnds(t *testing.T) {
	gate := make(chan struct{})
	e := newEnv(t, agents.Config{},
		fakellm.Reply{Calls: []fakellm.Call{call("spawn_agent", `{"message":"CHILD-N count the files"}`)}},
		fakellm.Reply{Text: "started it"},
		fakellm.Reply{Text: "the agent said forty-two"},
	)
	e.llm.Route("CHILD-N", fakellm.Reply{Gate: gate, Text: "forty-two"})
	s, ev := e.open(t, false)
	_, err := s.Submit("delegate")
	require.NoError(t, err)
	ev.finished()
	close(gate)
	ev.agentState(engine.AgentCompleted)
	parentRequests := func() int {
		n := 0
		for _, r := range e.llm.Requests() {
			if !isChild(r) {
				n++
			}
		}

		return n
	}
	_, err = s.Submit("what did it say?")
	require.NoError(t, err)
	ev.finished()
	assert.Equal(t, 3, parentRequests(), "the notification started no run of its own; it went with the next message")
	var last fakellm.Request
	for _, r := range e.llm.Requests() {
		if !isChild(r) {
			last = r
		}
	}
	require.GreaterOrEqual(t, len(last.UserTexts), 2)
	note := last.UserTexts[len(last.UserTexts)-2]
	assert.Contains(t, note, "<subagent_notification>")
	assert.Contains(t, note, `"status":{"completed":"forty-two"}`)
	assert.Equal(t, "what did it say?", last.UserTexts[len(last.UserTexts)-1])
}

// TestAgents_NotifyTheLiveRun tells a parent that works while its child
// ends in the same run: the notification goes into the run as a developer
// message and rides its next request, which it does not cancel, so the
// parent needs no wait_agent and no message to learn the answer.
func TestAgents_NotifyTheLiveRun(t *testing.T) {
	childGate, parentGate := make(chan struct{}), make(chan struct{})
	e := newEnv(t, agents.Config{},
		fakellm.Reply{Calls: []fakellm.Call{call("spawn_agent", `{"message":"CHILD-L count the files"}`)}},
		fakellm.Reply{Gate: parentGate, Commands: []string{"echo own work"}},
		fakellm.Reply{Text: "the agent said forty-two"},
		fakellm.Reply{Text: "nothing new"},
	)
	e.llm.Route("CHILD-L", fakellm.Reply{Gate: childGate, Text: "forty-two"})
	s, ev := e.open(t, false)
	_, err := s.Submit("delegate")
	require.NoError(t, err)
	awaitRequests(t, e, "CHILD-L", 1)
	close(childGate)
	ev.agentState(engine.AgentCompleted) // while the parent's second request is under way
	close(parentGate)
	assert.Equal(t, "the agent said forty-two", ev.finished().Answer)

	var parent []fakellm.Request
	for _, r := range e.llm.Requests() {
		if !isChild(r) {
			parent = append(parent, r)
		}
	}
	require.Len(t, parent, 3, "the notification canceled no request and started no run")
	last := parent[2]
	note := strings.Join(last.DeveloperTexts, "\n")
	assert.Contains(t, note, "<subagent_notification>")
	assert.Contains(t, note, `"status":{"completed":"forty-two"}`)
	assert.Contains(t, lastOutputs(e), "own work")

	_, err = s.Submit("anything else?")
	require.NoError(t, err)
	ev.finished()
	next := lastParent(e)
	assert.Equal(t, 1, strings.Count(strings.Join(slices.Concat(next.UserTexts, next.DeveloperTexts), "\n"), "<subagent_notification>"), "it is not sent again")
}

// TestAgents_AWaitTakesBackTheHeldNotification reports a child's result
// once: the child ends while the parent's run works without a wait, so its
// <subagent_notification> is held, and when wait_agent then returns the same
// final status, the held notification does not go with the next message.
func TestAgents_AWaitTakesBackTheHeldNotification(t *testing.T) {
	childGate, parentGate := make(chan struct{}), make(chan struct{})
	e := newEnv(t, agents.Config{},
		fakellm.Reply{Calls: []fakellm.Call{call("spawn_agent", `{"message":"CHILD-W count the files"}`)}},
		fakellm.Reply{From: func(req fakellm.Request) fakellm.Reply {
			return fakellm.Reply{Gate: parentGate, Calls: []fakellm.Call{call("wait_agent", `{"targets":["`+ids(req)[0]+`"]}`)}}
		}},
		fakellm.Reply{Text: "the agent said forty-two"},
		fakellm.Reply{Text: "nothing new"},
	)
	e.llm.Route("CHILD-W", fakellm.Reply{Gate: childGate, Text: "forty-two"})
	s, ev := e.open(t, false)
	_, err := s.Submit("delegate")
	require.NoError(t, err)
	close(childGate)
	ev.agentState(engine.AgentCompleted) // its notification is held: no wait is pending
	close(parentGate)
	assert.Equal(t, "the agent said forty-two", ev.finished().Answer)
	assert.Contains(t, lastOutputs(e), `{"completed":"forty-two"}`)

	_, err = s.Submit("anything else?")
	require.NoError(t, err)
	ev.finished()
	last := lastParent(e)
	assert.Equal(t, "anything else?", last.UserTexts[len(last.UserTexts)-1])
	assert.NotContains(t, strings.Join(last.UserTexts, "\n"), "<subagent_notification>", "the wait returned the status, so the note was taken back")
}
