package state

import (
	"github.com/viktordanov/uah/internal/session"
)

type (
	// EffAdvisor asks the session to apply /advisor's argument ("" only
	// shows the advisor).
	EffAdvisor struct{ Arg string }
	// AdvisorShown is what the session says the advisor is then; OK is
	// false when the engine has none.
	AdvisorShown struct {
		Status string
		OK     bool
		Err    error
	}
)

func (EffAdvisor) effect() {}

func cmdAdvisor(_ *State, args string) []Effect { return []Effect{EffAdvisor{Arg: args}} }

// onAdvisor shows the advisor's state, or why /advisor could not change
// it; ok is false for any other event.
func (s *State) onAdvisor(ev any) ([]Effect, bool) {
	e, ok := ev.(AdvisorShown)
	if !ok {
		return nil, false
	}
	switch {
	case e.Err != nil:
		s.notice(session.LevelError, "/advisor: "+e.Err.Error())
	case !e.OK:
		s.notice(session.LevelInfo, "/advisor needs the embedded engine")
	default:
		s.notice(session.LevelInfo, e.Status)
	}

	return nil, true
}
