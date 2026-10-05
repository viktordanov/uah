package session

import (
	"slices"

	"github.com/google/uuid"

	"github.com/viktordanov/uagent/core"
)

// evDo is work for the loop goroutine from another goroutine.
type evDo func()

// Inject gives the agent a message without a turn of its own, as Codex's
// inject_no_new_turn does for a subagent's notification. During a run it
// goes into the run as a developer message, which asks for no response of
// its own and so cancels no model request: it rides the run's next request,
// so an agent that works while its subagents run learns at once that one
// finished, rather than polling wait_agent or asking the subagent. A run
// that ends before the runner records it holds it again. Otherwise it is
// held and goes out before the next run's messages, and it never starts a
// run; withdraw takes it back while it is held.
// Live says it went into the run. It waits for the session's loop, so it
// is never called from the loop.
func (s *Session) Inject(text string) (withdraw func(), live bool) {
	id := uuid.NewString()
	reply := make(chan bool, 1)
	s.post(evDo(func() { reply <- s.onInject(id, text) }))
	select {
	case live = <-reply:
	case <-s.done:
	}

	return func() {
		s.post(evDo(func() { s.held = slices.DeleteFunc(s.held, func(in core.UserInput) bool { return in.ID == id }) }))
	}, live
}

// onInject sends the message into the live run, or holds it, and reports
// whether it went into the run.
func (s *Session) onInject(id, text string) bool {
	if s.state == StateClosed {
		return false
	}
	if s.state == StateRunning && s.run != nil {
		in := core.UserInput{ID: id, Text: text, Role: core.RoleDeveloper}
		if err := s.run.Send(in); err == nil {
			s.injected = append(s.injected, in)

			return true
		}
	}
	s.held = append(s.held, core.UserInput{ID: id, Text: text})

	return false
}
