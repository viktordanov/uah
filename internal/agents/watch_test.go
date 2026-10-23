package agents_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/agents"
	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/testing/fakellm"
)

// TestAgents_Watch follows a child from the parent session, as the TUI's
// agent view does: what it did so far, what it does next, and a message
// from the user that the child answers.
func TestAgents_Watch(t *testing.T) {
	gate := make(chan struct{})
	e := newEnv(t, agents.Config{},
		fakellm.Reply{Calls: []fakellm.Call{call("spawn_agent", `{"message":"CHILD-W look"}`)}},
		fakellm.Reply{Text: "spawned"},
	)
	e.llm.Route("CHILD-W", fakellm.Reply{Gate: gate, Text: "first answer"}, fakellm.Reply{Text: "second answer"})
	s, ev := e.open(t, false)
	_, err := s.Submit("start one")
	require.NoError(t, err)
	ev.finished()
	ev.agentState(engine.AgentRunning)

	_, err = s.WatchAgent("nobody")
	require.Error(t, err)
	w, err := s.WatchAgent("ada")
	require.NoError(t, err, "by nickname, in any case")
	defer w.Stop()
	assert.Equal(t, "Ada", w.Nickname)
	require.NotEmpty(t, w.Events)
	opened, ok := w.Events[0].(session.SessionOpened)
	require.True(t, ok, "the view starts where the child's session started")
	assert.Equal(t, w.ID, opened.ID)

	close(gate)
	answers := func(want string) {
		t.Helper()
		deadline := time.After(waitTimeout)
		for {
			select {
			case x, ok := <-w.Next:
				require.True(t, ok, "the watch ended before %q", want)
				if m, ok := x.(core.AssistantMessage); ok && m.Text == want {
					return
				}
			case <-deadline:
				t.Fatalf("no %q", want)
			}
		}
	}
	answers("first answer")
	require.NoError(t, w.Send("CHILD-W and now?", session.SendAfterRun))
	answers("second answer")
	assert.True(t, slices.ContainsFunc(e.llm.Requests(), func(r fakellm.Request) bool {
		return slices.Contains(r.UserTexts, "CHILD-W and now?")
	}), "the user's message reached the child")
	assert.Equal(t, engine.AgentCompleted, ev.agentState(engine.AgentCompleted).State)
}

// TestAgents_WatchClosed: a closed child's watch is closed from the start
// and brings all of its runs from disk, since its log is gone.
func TestAgents_WatchClosed(t *testing.T) {
	e := newEnv(t, agents.Config{},
		fakellm.Reply{Calls: []fakellm.Call{call("spawn_agent", `{"message":"CHILD-Z look"}`)}},
		callWith("wait_agent", `{"targets":["ID"]}`),
		callWith("close_agent", `{"target":"ID"}`),
		fakellm.Reply{Text: "done"},
	)
	e.llm.Route("CHILD-Z", fakellm.Reply{Text: "the answer"})
	s, ev := e.open(t, false)
	_, err := s.Submit("start one")
	require.NoError(t, err)
	ev.finished()

	var w *session.AgentWatch
	require.Eventually(t, func() bool {
		w, err = s.WatchAgent("ada")
		require.NoError(t, err)

		return w.Closed
	}, waitTimeout, time.Millisecond)
	_, open := <-w.Next
	assert.False(t, open, "nothing follows")
	require.Len(t, w.History, 1, "the run from this process, from disk")
	assert.Equal(t, "the answer", w.History[0].Record.Result.Answer)
}

// TestAgents_SendToAClosedChild refuses a message to a child that closed,
// as one that closes while send_input hands it over: the child stays shut
// down, so a wait on it returns at once.
func TestAgents_SendToAClosedChild(t *testing.T) {
	e := newEnv(t, agents.Config{},
		fakellm.Reply{Calls: []fakellm.Call{call("spawn_agent", `{"message":"CHILD-X look"}`)}},
		callWith("wait_agent", `{"targets":["ID"]}`),
		callWith("close_agent", `{"target":"ID"}`),
		fakellm.Reply{Text: "done"},
	)
	e.llm.Route("CHILD-X", fakellm.Reply{Text: "the answer"})
	s, ev := e.open(t, false)
	_, err := s.Submit("start one")
	require.NoError(t, err)
	ev.finished()
	closed := ev.agentState(engine.AgentShutdown)
	w, err := s.WatchAgent("ada")
	require.NoError(t, err)

	require.ErrorContains(t, w.Send("CHILD-X again", session.SendNow), "is closed")
	statuses, timedOut, err := e.mgr.Wait(t.Context(), s.ID(), []string{closed.ID}, time.Second)
	require.NoError(t, err)
	assert.False(t, timedOut, "the child is not running again")
	assert.Equal(t, engine.AgentShutdown, statuses[closed.ID].State)
}

// TestAgents_SteeredRunThatEndedAtOnce steers an interrupted child's queue
// into a run that ends before the steer returns: the child keeps the
// run's final status rather than running again with nothing to end it.
func TestAgents_SteeredRunThatEndedAtOnce(t *testing.T) {
	hold := make(chan struct{})
	t.Cleanup(func() { close(hold) })
	e := newEnv(t, agents.Config{},
		fakellm.Reply{Calls: []fakellm.Call{call("spawn_agent", `{"message":"CHILD-Q look"}`)}},
		fakellm.Reply{Text: "spawned"},
		fakellm.Reply{Text: "noted"},
		fakellm.Reply{Text: "noted"},
	)
	e.llm.Route("CHILD-Q", fakellm.Reply{Gate: hold, Text: "never"}, fakellm.Reply{Text: "steered"})
	s, ev := e.open(t, false)
	_, err := s.Submit("start one")
	require.NoError(t, err)
	running := ev.agentState(engine.AgentRunning)
	w, err := s.WatchAgent("ada")
	require.NoError(t, err)
	defer w.Stop()
	require.NoError(t, w.Send("CHILD-Q queued", session.SendAfterRun))
	require.Eventually(t, func() bool { // the held reply is taken, so the steered run gets the next
		return slices.ContainsFunc(e.llm.Requests(), func(r fakellm.Request) bool {
			return slices.ContainsFunc(r.UserTexts, func(text string) bool { return strings.HasPrefix(text, "CHILD-Q look") })
		})
	}, waitTimeout, time.Millisecond)
	w.Interrupt()
	ev.agentState(engine.AgentInterrupted)

	e.mgr.AfterSteer(func() { ev.agentState(engine.AgentCompleted) })
	require.NoError(t, w.SteerQueued())
	statuses, timedOut, err := e.mgr.Wait(t.Context(), s.ID(), []string{running.ID}, time.Second)
	require.NoError(t, err)
	assert.False(t, timedOut, "the child is not running again")
	assert.Equal(t, engine.AgentCompleted, statuses[running.ID].State)
}
