package bubble

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/internal/tui/state"
)

func TestAgentFocus_RootRequestAndDraftDuringOpen(t *testing.T) {
	m := New(context.Background(), Deps{})
	batches := make(chan []core.Event)
	updated, _ := m.Update(eventsMsg{batches: batches, events: []core.Event{
		engine.AgentUpdated{ID: "a", Nickname: "Ada", State: engine.AgentRunning},
		session.AgentFocusRequested{Target: "a"},
	}})
	m = updated.(Model)
	require.True(t, m.watchPending)

	stopped := false
	w := &session.AgentWatch{ID: "a", Stop: func() { stopped = true }}
	m.composer.SetValue("my draft")
	updated, _ = m.onAgentOpened(agentOpenedMsg{w: w, intent: m.watchIntent, focus: true})
	m = updated.(Model)
	assert.True(t, stopped)
	assert.Nil(t, m.st.View)
	assert.Equal(t, "my draft", m.composer.Value())

	m.composer.Reset()
	updated, _ = m.Update(eventsMsg{batches: batches, events: []core.Event{session.AgentFocusRequested{Target: "a"}}})
	m = updated.(Model)
	require.True(t, m.watchPending)
	agentEvents := make(chan core.Event)
	close(agentEvents)
	w = &session.AgentWatch{ID: "a", Nickname: "Ada", Next: agentEvents}
	updated, _ = m.onAgentOpened(agentOpenedMsg{w: w, intent: m.watchIntent, focus: true})
	require.NotNil(t, updated.(Model).st.View)
	assert.Equal(t, "a", updated.(Model).st.View.ID)
}

func TestAgentFocus_OnlyTheViewedChildCanReturn(t *testing.T) {
	m := New(context.Background(), Deps{})
	m.watchGen = 2
	child := state.New(m.deps.Now())
	m.st.View = &state.AgentView{ID: "a", St: &child}
	stopped := false
	m.watch = &session.AgentWatch{ID: "a", Stop: func() { stopped = true }}
	for _, request := range []agentEventsMsg{
		{gen: 1, id: "a", events: []core.Event{session.AgentFocusRequested{}}},
		{gen: 2, id: "b", events: []core.Event{session.AgentFocusRequested{}}},
		{gen: 2, id: "a", events: []core.Event{session.AgentFocusRequested{Target: "b"}}},
	} {
		updated, _ := m.onAgentEvents(request)
		require.NotNil(t, updated.(Model).st.View)
	}
	m.st.Approvals = []state.Approval{{ID: "pending"}}
	back := agentEventsMsg{gen: 2, id: "a", events: []core.Event{session.AgentFocusRequested{}}}
	updated, _ := m.onAgentEvents(back)
	require.NotNil(t, updated.(Model).st.View)

	m.st.Approvals = nil
	updated, _ = m.onAgentEvents(back)
	assert.Nil(t, updated.(Model).st.View)
	assert.True(t, stopped)
}

// TestAgentView_EndedWatchDrains keeps reading an ended watch's batches
// until they close, so its batch goroutine never blocks on a send.
func TestAgentView_EndedWatchDrains(t *testing.T) {
	m := Model{watchGen: 2}
	batches := make(chan []core.Event, 1)
	_, cmd := m.onAgentEvents(agentEventsMsg{gen: 1, id: "a", batches: batches})
	require.NotNil(t, cmd, "a stale batch still reads the next one")

	batches <- []core.Event{core.AssistantMessage{Text: "late"}}
	msg := cmd()
	assert.Equal(t, 1, msg.(agentEventsMsg).gen, "the drain keeps the old generation")

	close(batches)
	_, cmd = m.onAgentEvents(msg.(agentEventsMsg))
	ended := cmd()
	assert.Equal(t, agentWatchEndedMsg{gen: 1, id: "a"}, ended)
	_, cmd = m.onAgentWatchEnded(ended.(agentWatchEndedMsg))
	assert.Nil(t, cmd, "an ended watch that is not shown is not reopened")
}

// TestAgentView_ClosedWatchStays: a shown agent whose session closed is
// reopened once, for its transcript, and then left as it is.
func TestAgentView_ClosedWatchStays(t *testing.T) {
	m := Model{watchGen: 1, watch: &session.AgentWatch{ID: "a"}}
	m.st.View = &state.AgentView{ID: "a"}
	_, cmd := m.onAgentWatchEnded(agentWatchEndedMsg{gen: 1, id: "a"})
	assert.NotNil(t, cmd, "an open agent's ended watch is reopened")

	m.watch.Closed = true
	_, cmd = m.onAgentWatchEnded(agentWatchEndedMsg{gen: 1, id: "a"})
	assert.Nil(t, cmd, "a closed agent's watch is not reopened again")
}

func TestAgentView_FailedSwitchRecoversEndedWatch(t *testing.T) {
	m := New(context.Background(), Deps{})
	child := state.New(m.deps.Now())
	m.st.View = &state.AgentView{ID: "a", St: &child}
	m.watch = &session.AgentWatch{ID: "a", Stop: func() {}}
	openB := m.watchAgent("b", false)
	updated, cmd := m.onAgentWatchEnded(agentWatchEndedMsg{id: "a"})
	require.Nil(t, cmd, "let the pending switch finish first")
	m = updated.(Model)
	updated, _ = m.onAgentOpened(openB().(agentOpenedMsg)) // no session: open fails
	m = updated.(Model)
	assert.Equal(t, "a", m.st.View.ID)
	assert.True(t, m.watchPending, "reopen the visible agent")
}
