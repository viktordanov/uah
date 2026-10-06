package bubble

import (
	"time"

	"github.com/viktordanov/uah/internal/tui/state"
	"github.com/viktordanov/uah/internal/tui/term"
)

// findPause lets a quick typist's keys settle before a search starts: a
// key within it makes the search stale before it reads anything.
const findPause = 40 * time.Millisecond

// onFindKey maps a key while the transcript search is open: text edits
// the query until enter, then j and k move; ↑ and ↓ move either way.
func (m Model) onFindKey(msg term.KeyPressMsg) (term.Model, term.Cmd) {
	browsing := m.st.Find.Browsing
	switch key := msg.String(); {
	case key == keyUp || (browsing && key == "k"):
		return m.dispatch(state.FindMove{Delta: -1})
	case key == keyDown || (browsing && key == "j"):
		return m.dispatch(state.FindMove{Delta: 1})
	case key == keyEnter:
		return m.dispatch(state.FindEnter{})
	case key == keyEsc:
		return m.dispatch(state.FindEsc{})
	case key == keyCtrlC:
		return m.dispatch(state.FindCancel{})
	case !browsing && (key == keyBackspace || key == "ctrl+h"):
		return m.dispatch(state.FindType{Text: "\b"})
	}
	if !browsing && msg.Text != "" && msg.Mod&(term.ModCtrl|term.ModAlt) == 0 {
		return m.dispatch(state.FindType{Text: msg.Text})
	}

	return m, nil
}

// findInTranscript searches off the update loop, after a short pause; a
// newer search makes it stop and return nothing.
func (m Model) findInTranscript(e state.EffFindInTranscript) term.Cmd {
	gen := m.findGen
	gen.Store(int64(e.Gen))
	stale := func() bool { return gen.Load() != int64(e.Gen) }

	return func() term.Msg {
		time.Sleep(findPause)
		if stale() {
			return nil
		}
		keys := state.FindIn(e.Docs, e.Query, stale)
		if stale() {
			return nil
		}

		return state.Found{Gen: e.Gen, Keys: keys}
	}
}
