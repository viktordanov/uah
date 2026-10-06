package state

import (
	"cmp"
	"slices"
	"strings"
	"time"

	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/session"
)

// Going back to an earlier message, as Codex's backtrack
// (codex-rs/tui/src/app_backtrack.rs, rust-v0.156.1): esc on an empty
// composer while idle primes it, a second esc selects your latest message,
// ↑ or k steps to earlier ones and ↓ or j to later ones, enter goes back
// to before the selected message and puts it in the composer, and esc or
// any other key cancels. /rewind selects at once. The transcript is cut when the
// session reports engine.Rewound.

// Backtrack is the selection while going back: Key is the selected user
// message's item.
type Backtrack struct {
	Key string
}

type (
	// BacktrackMove selects an earlier (-1) or later (+1) message.
	BacktrackMove struct{ Delta int }
	// BacktrackSelect goes back to before the selected message.
	BacktrackSelect struct{}
	// BacktrackCancel leaves the selection; nothing changes.
	BacktrackCancel struct{}
	// EffRewind goes back to before the message ID.
	EffRewind struct{ ID string }
)

func (EffRewind) effect() {}

const (
	backtrackPrimed = "esc again to edit a previous message"
	backtrackHint   = "editing a previous message · ↑/k earlier · ↓/j later · enter edit from here · esc or any other key cancels"
	noBacktrack     = "No previous message to edit."
)

// onBacktrack handles the selection's intents, and esc on an empty
// composer while idle; ok is false for any other event.
func (s *State) onBacktrack(ev any) (effects []Effect, ok bool) {
	if s.Backtrack != nil {
		return s.onSelecting(ev)
	}
	esc, isEsc := ev.(Esc)
	if !isEsc || !esc.Empty || !s.canBacktrack() {
		return nil, false
	}
	if s.escArmed.IsZero() || s.Now.Sub(s.escArmed) >= confirmWindow {
		s.escArmed, s.Status = s.Now, ""
		if len(s.rewindTargets()) > 0 {
			s.Status = backtrackPrimed
		}

		return nil, true
	}
	s.escArmed, s.Status = time.Time{}, ""
	s.startBacktrack()

	return nil, true
}

// cmdRewind selects your latest message at once, as a second esc does.
func cmdRewind(s *State, _ string) []Effect {
	if len(s.Queue) > 0 {
		s.notice(session.LevelWarning, "/rewind waits until nothing is queued (↑ takes a queued message back)")

		return nil
	}
	s.startBacktrack()

	return nil
}

func (s *State) onSelecting(ev any) ([]Effect, bool) {
	switch e := ev.(type) {
	case BacktrackMove:
		s.moveBacktrack(e.Delta)
	case BacktrackSelect:
		return s.selectBacktrack(), true
	case BacktrackCancel, Esc:
		s.Backtrack, s.Status = nil, ""
	default:
		return nil, false
	}

	return nil, true
}

// canBacktrack reports whether esc may start going back: a session is
// open and idle with nothing queued. A running /review is not idle: esc
// stops it instead.
func (s *State) canBacktrack() bool {
	return !s.Busy && !s.ShellRunning() && !s.ReviewRunning() && len(s.Queue) == 0 && s.SessionID != ""
}

func (s *State) startBacktrack() {
	targets := s.rewindTargets()
	if len(targets) == 0 {
		s.notice(session.LevelInfo, noBacktrack)

		return
	}
	s.Backtrack, s.Status = &Backtrack{Key: s.Items[targets[len(targets)-1]].Key}, backtrackHint
}

// rewindTargets are the indexes of the messages you can go back to: yours,
// delivered to the agent.
func (s *State) rewindTargets() []int {
	var out []int
	for i, it := range s.Items {
		if it.Kind == KindUser && it.Input == InputDelivered {
			out = append(out, i)
		}
	}

	return out
}

func (s *State) moveBacktrack(delta int) {
	targets := s.rewindTargets()
	at := slices.IndexFunc(targets, func(i int) bool { return s.Items[i].Key == s.Backtrack.Key })
	if at < 0 {
		s.Backtrack, s.Status = nil, ""

		return
	}
	at = min(max(at+delta, 0), len(targets)-1)
	s.Backtrack.Key = s.Items[targets[at]].Key
}

// selectBacktrack asks the session to go back and puts the message, with
// its images, in the composer, as Codex reopens the prompt for editing.
func (s *State) selectBacktrack() []Effect {
	it, ok := s.Item(s.Backtrack.Key)
	s.Backtrack, s.Status = nil, ""
	if !ok {
		return nil
	}
	s.Scroll = 0
	draft, _ := s.onImages(DraftRestored{Text: cmp.Or(it.Raw, it.Text)})

	return append([]Effect{EffRewind{ID: strings.TrimPrefix(it.Key, "msg:")}}, draft...)
}

// onRewound cuts the transcript at the message the session went back to:
// it and everything after it leave, as Codex's transcript ends before the
// reverted turn.
func (s *State) onRewound(e engine.Rewound) {
	s.ContextUsed = e.Tokens
	i, ok := s.index["msg:"+e.MessageID]
	if !ok {
		return
	}
	for _, it := range s.Items[i:] {
		delete(s.index, it.Key)
	}
	s.Items = s.Items[:i]
	s.streaming = nil // a rewind needs an idle session, so none is left; none may outlive its item
	s.agentIDs = slices.DeleteFunc(s.agentIDs, func(id string) bool {
		_, kept := s.index["agent:"+id]

		return !kept
	})
	s.Scroll = 0
	s.notice(LevelDebug, "went back to before a message; what followed left the context and stays in the session file")
}
