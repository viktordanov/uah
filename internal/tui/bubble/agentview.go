package bubble

import (
	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/internal/tui/state"
	"github.com/viktordanov/uah/internal/tui/term"
)

// Messages of the agent view: a watch that opened, and a batch of the
// watched agent's events. gen drops batches of a watch that ended.
type (
	agentOpenedMsg struct{ w *session.AgentWatch }
	agentEventsMsg struct {
		gen     int
		id      string
		events  []core.Event
		batches <-chan []core.Event
	}
	// agentWatchEndedMsg says a watch's events ended.
	agentWatchEndedMsg struct {
		gen int
		id  string
	}
)

// onAgentMsg routes the agent view's messages.
func (m Model) onAgentMsg(msg term.Msg) (term.Model, term.Cmd) {
	switch msg := msg.(type) {
	case agentOpenedMsg:
		return m.onAgentOpened(msg)
	case agentEventsMsg:
		return m.onAgentEvents(msg)
	case agentWatchEndedMsg:
		return m.onAgentWatchEnded(msg)
	}

	return m, nil
}

// watchAgent starts following an agent of the session.
func (m Model) watchAgent(id string) term.Cmd {
	sess := m.sess

	return func() term.Msg {
		if sess == nil {
			return state.Failed{Err: errNoSession}
		}
		w, err := sess.WatchAgent(id)
		if err != nil {
			return state.Failed{Err: err}
		}

		return agentOpenedMsg{w: w}
	}
}

// onAgentOpened shows the agent's transcript and follows its events, in
// batches as the session's are.
func (m Model) onAgentOpened(msg agentOpenedMsg) (term.Model, term.Cmd) {
	m.stopWatch()
	m.watch = msg.w
	m.watchGen++
	batches := make(chan []core.Event)
	go batch(msg.w.Next, batches)
	w := msg.w
	updated, cmd := m.dispatch(state.AgentViewOpened{ID: w.ID, Nickname: w.Nickname, History: w.History, Events: w.Events})

	return updated, term.Batch(cmd, nextAgent(m.watchGen, w.ID, batches))
}

func (m Model) onAgentEvents(msg agentEventsMsg) (term.Model, term.Cmd) {
	if msg.gen != m.watchGen {
		// A watch that ended: keep reading until its batches close, so its
		// batch goroutine is not left blocked on a send.
		return m, nextAgent(msg.gen, msg.id, msg.batches)
	}
	m.st, _ = state.Reduce(m.st, state.AgentEvents{ID: msg.id, Events: msg.events})

	return m, term.Batch(m.afterChange(), nextAgent(msg.gen, msg.id, msg.batches))
}

// onAgentWatchEnded reopens the view when the watch closed while it was
// still shown: the manager closes a view that fell a whole queue behind,
// and opening it again catches up. When the agent's session closed, the
// reopened watch is closed too and brings its whole transcript from disk;
// the view then stays as it is.
func (m Model) onAgentWatchEnded(msg agentWatchEndedMsg) (term.Model, term.Cmd) {
	if msg.gen != m.watchGen || m.watch == nil || m.watch.Closed || m.st.View == nil || m.st.View.ID != msg.id {
		return m, nil
	}

	return m, m.watchAgent(msg.id)
}

// nextAgent waits for the watched agent's next batch, and reports when its
// events end.
func nextAgent(gen int, id string, batches <-chan []core.Event) term.Cmd {
	return func() term.Msg {
		events, ok := <-batches
		if !ok {
			return agentWatchEndedMsg{gen: gen, id: id}
		}

		return agentEventsMsg{gen: gen, id: id, events: events, batches: batches}
	}
}

// stopWatch ends the current watch, if any.
func (m *Model) stopWatch() {
	if m.watch != nil {
		m.watch.Stop()
		m.watch = nil
		m.watchGen++
	}
}

// sendToAgent gives the watched agent a message.
func (m Model) sendToAgent(text string, when session.When) term.Cmd {
	w := m.watch

	return m.calls.next(func() term.Msg {
		if w == nil {
			return nil
		}
		if err := w.Send(text, when); err != nil {
			return state.Failed{Err: err}
		}

		return nil
	})
}

// steerAgentQueue sends the watched agent's queued messages now.
func (m Model) steerAgentQueue() term.Cmd {
	w := m.watch

	return m.calls.next(func() term.Msg {
		if w == nil || w.SteerQueued == nil {
			return nil
		}
		if err := w.SteerQueued(); err != nil {
			return state.Failed{Err: err}
		}

		return nil
	})
}
