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
	agentOpenedMsg struct {
		w           *session.AgentWatch
		err         error
		gen, intent int
		focus       bool
		id          string
	}
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
func (m *Model) watchAgent(id string, focus bool) term.Cmd {
	sess := m.sess
	m.watchIntent++
	m.watchPending = true
	gen, intent := m.gen, m.watchIntent

	return func() term.Msg {
		if sess == nil {
			return agentOpenedMsg{err: errNoSession, id: id, gen: gen, intent: intent, focus: focus}
		}
		w, err := sess.WatchAgent(id)

		return agentOpenedMsg{w: w, err: err, id: id, gen: gen, intent: intent, focus: focus}
	}
}

// onAgentOpened shows the agent's transcript and follows its events, in
// batches as the session's are.
func (m Model) onAgentOpened(msg agentOpenedMsg) (term.Model, term.Cmd) {
	current := msg.gen == m.gen && msg.intent == m.watchIntent
	if current {
		m.watchPending = false
	}
	if !current || (msg.focus && (m.st.View != nil || !m.canChangeAgentFocus() ||
		msg.w == nil || msg.w.Closed || !m.st.WorkingAgent(msg.w.ID))) {
		if msg.w != nil {
			msg.w.Stop()
		}

		return m, nil
	}
	if msg.err != nil {
		var retry term.Cmd
		if m.st.View != nil && m.watch == nil && m.st.View.ID != msg.id {
			retry = m.watchAgent(m.st.View.ID, false)
		}
		updated, cmd := m.dispatch(state.Failed{Err: msg.err})
		return updated, term.Batch(cmd, retry)
	}
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
	for _, e := range msg.events {
		if req, ok := e.(session.AgentFocusRequested); ok && req.Target == "" && !m.watchPending &&
			m.st.View != nil && m.st.View.ID == msg.id && m.canChangeAgentFocus() {
			nextModel, cmd := m.dispatch(state.CloseAgentView{})

			return nextModel, term.Batch(cmd, nextAgent(msg.gen, msg.id, msg.batches))
		}
	}

	return m, term.Batch(m.afterChange(), nextAgent(msg.gen, msg.id, msg.batches))
}

func (m Model) canChangeAgentFocus() bool {
	return m.composer.Value() == "" && len(m.st.Attached) == 0 &&
		m.st.Mode == state.ModeChat && !m.st.Shell && !m.st.Quitting &&
		len(m.st.Approvals) == 0 && len(m.st.Questions) == 0 &&
		m.st.Config == nil && m.st.ModelPicker == nil && m.st.Peek == nil &&
		m.st.Find == nil && m.st.Backtrack == nil && m.st.History.Search == nil
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

	if m.watchPending {
		// If the switch fails, the visible view will need a fresh watch.
		m.stopWatch()
		return m, nil
	}
	cmd := m.watchAgent(msg.id, false)

	return m, cmd
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
