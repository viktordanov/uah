package agents_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/agents"
	"github.com/viktordanov/uah/internal/approval"
	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/testing/fakellm"
)

// TestAgents_StalledChildStops ends a child that runs with no event for
// StallAfter as an error, so the parent's wait returns with why.
func TestAgents_StalledChildStops(t *testing.T) {
	hung := make(chan struct{})
	defer close(hung)
	e := newEnv(t, agents.Config{StallAfter: 300 * time.Millisecond},
		fakellm.Reply{Calls: []fakellm.Call{call("spawn_agent", `{"message":"CHILD-S hang"}`)}},
		callWith("wait_agent", `{"targets":["ID"]}`),
		fakellm.Reply{Text: "it stalled"},
	)
	e.llm.Route("CHILD-S", fakellm.Reply{Gate: hung, Text: "never"})
	s, ev := e.open(t, false)

	_, err := s.Submit("delegate")
	require.NoError(t, err)
	stalled := ev.agentState(engine.AgentErrored)
	assert.Equal(t, "stopped: no activity for 300ms", stalled.Message)
	assert.Equal(t, "it stalled", ev.finished().Answer)
	assert.Contains(t, answers(e), "stopped: no activity for 300ms", "the wait returned the stall")
}

// TestAgents_StalledChildTakesNewWork runs a message sent to a stalled
// child, also one sent before the stall's interrupt is in.
func TestAgents_StalledChildTakesNewWork(t *testing.T) {
	hung := make(chan struct{})
	defer close(hung)
	e := newEnv(t, agents.Config{StallAfter: 300 * time.Millisecond},
		fakellm.Reply{Calls: []fakellm.Call{call("spawn_agent", `{"message":"CHILD-R hang"}`)}},
		callWith("wait_agent", `{"targets":["ID"]}`),
		callWithLast("send_input", `{"target":"ID","message":"CHILD-R again"}`),
		callWithLast("wait_agent", `{"targets":["ID"]}`),
		fakellm.Reply{Text: "it came back"},
	)
	e.llm.Route("CHILD-R", fakellm.Reply{Gate: hung, Text: "never"}, fakellm.Reply{Text: "back"})
	s, ev := e.open(t, false)

	_, err := s.Submit("delegate")
	require.NoError(t, err)
	assert.Equal(t, "it came back", ev.finished().Answer)
	assert.Contains(t, answers(e), `{"completed":"back"}`, "the new work ran and its answer replaced the stall")
}

// TestAgents_ChildWaitingOnAnApprovalIsNotStalled keeps a child whose
// approval is open, however long the user takes.
func TestAgents_ChildWaitingOnAnApprovalIsNotStalled(t *testing.T) {
	e := newEnv(t, agents.Config{StallAfter: time.Second},
		fakellm.Reply{Calls: []fakellm.Call{call("spawn_agent", `{"message":"CHILD-W fetch"}`)}},
		callWith("wait_agent", `{"targets":["ID"]}`),
		fakellm.Reply{Text: "done"},
	)
	e.llm.Route("CHILD-W", fakellm.Reply{Escalated: []string{"echo fetched"}}, fakellm.Reply{Text: "fetched"})
	s, ev := e.open(t, true, e.sandboxed(t))

	_, err := s.Submit("delegate a fetch")
	require.NoError(t, err)
	req := ev.approval()
	time.Sleep(3 * time.Second) // three times StallAfter
	require.NoError(t, s.Resolve(req.ID, approval.Approve))
	assert.Equal(t, "done", ev.finished().Answer)
	assert.Contains(t, answers(e), `{"completed":"fetched"}`)
}
