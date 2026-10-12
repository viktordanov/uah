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
// held and goes out before the next run's messages; a session that is idle
// starts that run for it, so the agent takes up a subagent's or a
// background shell's end (the owner's decision, unlike Codex's
// inject_no_new_turn), unless the user stopped the last run, which keeps
// it for the user's next message (wake). Withdraw takes it back while it
// is held. Live says it went into a run. It waits for the session's loop,
// so it is never called from the loop.
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
	s.wakeFor(id)

	return s.wakeIdle()
}

// wakeFor marks a held message as one that starts a run.
func (s *Session) wakeFor(id string) {
	if s.wake == nil {
		s.wake = map[string]bool{}
	}
	s.wake[id] = true
}

// wakeIdle starts a run for the held messages when an injected one waits
// and the session is idle with nothing else under way, and reports
// whether it did. A run the user stopped leaves them for the user's next
// message.
func (s *Session) wakeIdle() bool {
	waiting := slices.ContainsFunc(s.held, func(in core.UserInput) bool { return s.wake[in.ID] }) // not withdrawn
	if !waiting || s.quiet || s.state != StateIdle || s.closeReply != nil || len(s.queue) > 0 || len(s.hooks.checking) > 0 {
		return false
	}
	s.hooks.stopGen++ // a pending Stop hook no longer decides anything, as for a message
	s.startRun(nil)

	return true
}
