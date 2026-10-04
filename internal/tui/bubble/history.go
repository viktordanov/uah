package bubble

import (
	"github.com/viktordanov/uah/internal/history"
	"github.com/viktordanov/uah/internal/tui/render"
	"github.com/viktordanov/uah/internal/tui/state"
	"github.com/viktordanov/uah/internal/tui/term"
)

// onSearchKey drives the ctrl+r search, whose query is the footer's: ctrl+r
// and ↑ go to older matches, ctrl+s and ↓ to newer ones, enter keeps the
// match, esc and ctrl+c put the draft back, backspace and ctrl+u edit the
// query, and text goes into it. Other keys do nothing, as in Codex.
func (m Model) onSearchKey(msg term.KeyPressMsg) (term.Model, term.Cmd) {
	switch msg.String() {
	case "ctrl+r", keyUp:
		return m.dispatch(state.SearchMove{Older: true})
	case "ctrl+s", keyDown:
		return m.dispatch(state.SearchMove{})
	case keyEnter:
		return m.dispatch(state.SearchAccept{})
	case keyEsc, keyCtrlC:
		return m.dispatch(state.SearchCancel{})
	case keyBackspace, "ctrl+h":
		return m.dispatch(state.SearchType{Text: "\b"})
	case "ctrl+u":
		return m.dispatch(state.SearchClear{})
	}
	if msg.Text != "" && msg.Mod&(term.ModCtrl|term.ModAlt) == 0 {
		return m.dispatch(state.SearchType{Text: msg.Text})
	}

	return m, nil
}

// runHistory reads and appends the history file off the update loop; ok
// is false for any other effect. A failure shows as an error notice. A prompt joins the recorder's queue here,
// on the loop, so prompts reach the file in the order they were sent.
func (m Model) runHistory(e state.Effect) (term.Cmd, bool) {
	switch e := e.(type) {
	case state.EffLoadPrompts:
		if m.prompts == nil {
			return nil, true
		}
		file := m.prompts.File()

		return func() term.Msg {
			entries, err := file.Load()
			if err != nil {
				return state.Failed{Err: err} // ↑ recalls this process's prompts only
			}
			prompts := make([]state.Prompt, 0, len(entries))
			for _, entry := range entries {
				prompts = append(prompts, state.Prompt{Workspace: entry.Workspace, Text: entry.Text})
			}

			return state.PromptsLoaded{Prompts: prompts}
		}, true
	case state.EffRecordPrompt:
		if m.prompts == nil {
			return nil, true
		}
		m.prompts.Add(history.Entry{SessionID: e.SessionID, TS: m.deps.Now().Unix(), Text: e.Text, Workspace: e.Workspace})
		recorder := m.prompts

		return func() term.Msg {
			if err := recorder.Flush(); err != nil {
				return state.Failed{Err: err} // the prompt is not kept, as in Codex
			}

			return nil
		}, true
	}

	return nil, false
}

// searchCursor puts the terminal's cursor after the search's query in the
// footer, below the composer's band row, as Codex does.
func (m Model) searchCursor(c *term.Cursor, composerRow int) {
	if m.st.History.Search == nil {
		return
	}
	c.Y = composerRow + m.composer.Height() + 1
	c.X = render.SearchCursorX(m.st.History.Search, m.w)
}
