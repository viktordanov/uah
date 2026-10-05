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
// that ends first leaves it in the history for the next request. Otherwise
// it is held and goes out before the next run's messages, and it never
// starts a run; withdraw takes it back while it is held.
func (s *Session) Inject(text string) (withdraw func()) {
	id := uuid.NewString()
	s.post(evDo(func() { s.onInject(id, text) }))

	return func() {
		s.post(evDo(func() { s.held = slices.DeleteFunc(s.held, func(in core.UserInput) bool { return in.ID == id }) }))
	}
}

func (s *Session) onInject(id, text string) {
	if s.state == StateClosed {
		return
	}
	if s.state == StateRunning && s.run != nil {
		if err := s.run.Send(core.UserInput{ID: id, Text: text, Role: core.RoleDeveloper}); err == nil {
			return
		}
	}
	s.held = append(s.held, core.UserInput{ID: id, Text: text})
}
