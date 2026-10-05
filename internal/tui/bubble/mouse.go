package bubble

import (
	"github.com/viktordanov/uah/internal/tui/render"
	"github.com/viktordanov/uah/internal/tui/state"
	"github.com/viktordanov/uah/internal/tui/term"
)

// onMouse turns the reported mouse into selection intents: the left
// button's press, drag, and release on the transcript (state/selection.go).
// A press elsewhere clears the selection; the composer keeps its own
// editing. The picker and the agent view take no selection.
func (m Model) onMouse(msg term.Msg) (term.Model, term.Cmd) {
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

// drag moves a selection's head to the mouse. Past the transcript's top or
// bottom, it scrolls a line that way first and selects to the edge, so
// holding the mouse there and moving it keeps scrolling.
func (m Model) drag(x, y int) (term.Model, term.Cmd) {
	if sel := m.st.Selection; sel == nil || !sel.Dragging {
		return m, nil
	}
	var cmds []term.Cmd
	if edge := m.cache.Edge(y); edge != 0 {
		model, cmd := m.scroll(-edge)
		m = model.(Model) //nolint:forcetypeassert // scroll returns a Model
		m.View()          // lay out the scrolled window
		cmds = append(cmds, cmd)
	}
	at, _, ok := m.cache.At(x, y, true)
	if !ok {
		return m, term.Batch(cmds...)
	}
	model, cmd := m.dispatch(state.MouseDrag{At: at})

	return model, term.Batch(append(cmds, cmd)...)
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
