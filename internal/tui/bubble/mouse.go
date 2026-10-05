package bubble

import (
	"time"

	"github.com/viktordanov/uah/internal/tui/render"
	"github.com/viktordanov/uah/internal/tui/state"
	"github.com/viktordanov/uah/internal/tui/term"
)

// onMouse turns the reported mouse into selection intents: the left
// button's press, drag, and release on the transcript (state/selection.go).
// A press elsewhere clears the selection; the composer keeps its own
// editing. The picker and the agent view take no selection. A drag held at
// the transcript's edge scrolls it on edgeMsg's tick.
func (m Model) onMouse(msg term.Msg) (term.Model, term.Cmd) {
	if _, ok := msg.(edgeMsg); ok {
		return m.edgeScroll()
	}
	if m.st.Mode != state.ModeChat || m.st.View != nil {
		return m, nil
	}
	switch msg := msg.(type) {
	case term.MouseClickMsg:
		if msg.Button != term.MouseLeft {
			return m, nil
		}
		if m.cache.OnPill(msg.X, msg.Y) {
			return m.dispatch(state.ScrollToBottom{})
		}
		at, text, ok := m.cache.At(msg.X, msg.Y, false)
		if !ok {
			if m.st.Selection == nil {
				return m, nil
			}

			return m.dispatch(state.ClearSelection{})
		}

		return m.dispatch(state.MousePress{At: at, Text: text, When: m.deps.Now()})
	case term.MouseMotionMsg:
		return m.drag(msg.X, msg.Y)
	case term.MouseReleaseMsg:
		if m.st.Selection == nil {
			return m, nil
		}

		return m.dispatch(state.MouseRelease{})
	}

	return m, nil
}

// drag moves a selection's head to the mouse. Held on the transcript's
// top row, or below its last, it selects to the edge and starts the edge
// scroll, which goes on while the mouse stays there.
func (m Model) drag(x, y int) (term.Model, term.Cmd) {
	if sel := m.st.Selection; sel == nil || !sel.Dragging {
		return m, nil
	}
	m.pointer.x, m.pointer.y = x, y
	var cmds []term.Cmd
	if m.cache.Edge(y) != 0 && !m.edgeTicking {
		m.edgeTicking = true
		cmds = append(cmds, edgeTick())
	}
	at, _, ok := m.cache.At(x, y, true)
	if !ok {
		return m, term.Batch(cmds...)
	}
	model, cmd := m.dispatch(state.MouseDrag{At: at})

	return model, term.Batch(append(cmds, cmd)...)
}

const (
	// edgeInterval is the edge scroll's tick: a line a tick on the edge
	// row, a line more for each row past it, up to edgeMaxLines.
	edgeInterval = 50 * time.Millisecond
	edgeMaxLines = 8
)

// edgeMsg is the edge scroll's tick.
type edgeMsg struct{}

func edgeTick() term.Cmd {
	return term.Tick(edgeInterval, func(time.Time) term.Msg { return edgeMsg{} })
}

// edgeScroll scrolls a drag held at the transcript's edge and moves the
// selection's head with the text under the mouse. The tick stops when the
// drag ends, the mouse leaves the edge, or the transcript cannot scroll
// further that way, so nothing runs while the mouse rests; a move starts
// it again.
func (m Model) edgeScroll() (term.Model, term.Cmd) {
	m.edgeTicking = false
	edge := m.cache.Edge(m.pointer.y)
	if sel := m.st.Selection; sel == nil || !sel.Dragging || edge == 0 || m.st.Mode != state.ModeChat || m.st.View != nil {
		return m, nil
	}
	lines := -max(min(edge, edgeMaxLines), -edgeMaxLines) // up is positive
	before := m.st.Scroll
	model, cmd := m.scroll(lines)
	m = model.(Model) //nolint:forcetypeassert // scroll returns a Model
	if m.st.Scroll == before {
		return m, cmd // at the top or the bottom
	}
	m.View() // lay out the scrolled window
	m.edgeTicking = true
	cmds := []term.Cmd{cmd, edgeTick()}
	if at, _, ok := m.cache.At(m.pointer.x, m.pointer.y, true); ok {
		model, drag := m.dispatch(state.MouseDrag{At: at})
		m = model.(Model) //nolint:forcetypeassert // dispatch returns a Model
		cmds = append(cmds, drag)
	}

	return m, term.Batch(cmds...)
}

// copySelection writes the selected text to the clipboard twice: as OSC 52,
// which the terminal handles (also over ssh), and with the system's own
// tool (Deps.CopyText), for terminals that ignore OSC 52. Then a toast
// says how many lines.
func (m Model) copySelection() term.Cmd {
	text, lines := render.SelectedText(m.st, m.cache, m.frame())
	if text == "" {
		return nil
	}
	ctx, write, now := m.ctx, m.deps.CopyText, m.deps.Now

	return term.Batch(term.SetClipboard(text), func() term.Msg {
		if write != nil {
			_ = write(ctx, text) // no tool: OSC 52 alone
		}

		return state.Copied{Lines: lines, At: now()}
	})
}
