package bubble

import (
	"github.com/viktordanov/uah/internal/approval"
	"github.com/viktordanov/uah/internal/tui/composer"
	"github.com/viktordanov/uah/internal/tui/state"
	"github.com/viktordanov/uah/internal/tui/term"
)

// Keys named in more than one mode.
const (
	keyEnter     = "enter"
	keyEsc       = "esc"
	keyCtrlN     = "ctrl+n"
	keyCtrlC     = "ctrl+c"
	keyDown      = "down"
	keyUp        = "up"
	keyCtrlP     = "ctrl+p"
	keyBackspace = "backspace"
)

// onKey maps keys to intents. The send keys (state.SendIntent): while the
// agent works, ctrl+enter and alt+enter give it the message now, enter after
// its next tool call, and tab at the end of the run. Shift+Enter and ctrl+j
// add a line.
func (m Model) onKey(msg term.KeyPressMsg) (term.Model, term.Cmd) { //nolint:gocyclo // a dispatch switch over a closed set; see docs/documentation/architecture.md
	if m.st.Mode == state.ModePicker {
		return m.onPickerKey(msg)
	}
	if _, ok := m.st.PendingApproval(); ok {
		return m.onApprovalKey(msg)
	}
	if q, ok := m.st.PendingQuestions(); ok {
		if intent := questionIntent(msg.String(), m.composer.Value(), q.Typing()); intent != nil {
			return m.dispatch(intent)
		}
		if !q.Typing() {
			return m, nil // the options take no text, so no key starts an answer by accident
		}
		switch msg.String() {
		case "!", "/", "@":
			return m.typeKey(msg) // text, not shell mode or the menu
		}
	}
	if m.st.Config != nil {
		return m.onConfigKey(msg)
	}
	if m.st.ModelPicker != nil {
		return m.onModelPickerKey(msg)
	}
	if m.st.Backtrack != nil {
		if intent := backtrackIntent(msg.String()); intent != nil {
			return m.dispatch(intent)
		}
		// Any other key cancels, and then does what it does.
		model, cancel := m.dispatch(state.BacktrackCancel{})
		next, cmd := model.(Model).onKey(msg) //nolint:forcetypeassert // dispatch returns a Model

		return next, term.Batch(cancel, cmd)
	}
	if m.st.History.Search != nil {
		return m.onSearchKey(msg)
	}
	draft := m.composer.Value()
	if intent := menuIntent(m.st, msg.String(), draft); intent != nil {
		return m.dispatch(intent)
	}
	switch msg.String() {
	case keyEnter, state.KeyCtrlEnter, state.KeyAltEnter, state.KeyTab:
		intent := m.st.SendIntent(msg.String(), draft)
		if intent == nil && msg.String() != state.KeyTab {
			return m, nil
		}
		if intent != nil {
			if trimmed(draft) != "" {
				m.composer.Reset()
			}

			return m.dispatch(intent)
		}
	case keyEsc:
		return m.dispatch(state.Esc{Empty: draft == ""})
	case keyCtrlC:
		if draft != "" {
			m.composer.Reset()

			return m.dispatch(state.DraftCleared{Draft: draft}) // ↑ brings it back; drops its images
		}

		return m.dispatch(state.Quit{})
	case "up":
		if draft == "" && len(m.st.Queue) > 0 {
			return m.dispatch(state.EditLastQueued{})
		}
		if m.st.Recalls(draft, m.atEdge(), false) {
			return m.dispatch(state.RecallOlder{})
		}
		// The terminal's wheel arrives as ↑ and ↓ when the mouse is not
		// reported: ↑ on the composer's first row scrolls the transcript,
		// as there is nowhere above to move the cursor.
		if m.onFirstRow() {
			return m.scroll(1)
		}
	case keyDown:
		if m.st.Recalls(draft, m.atEdge(), true) {
			return m.dispatch(state.RecallNewer{})
		}
		if m.onLastRow() {
			return m.scroll(-1)
		}
	case "pgup":
		return m.scroll(max(m.h/2, 1))
	case "pgdown":
		return m.scroll(-max(m.h/2, 1))
	case "shift+up":
		return m.scroll(1)
	case "shift+down":
		return m.scroll(-1)
	case "end":
		if draft == "" {
			return m.dispatch(state.ScrollToBottom{})
		}
	case "alt+left":
		return m.dispatch(state.SwitchAgent{Delta: -1})
	case "alt+right":
		return m.dispatch(state.SwitchAgent{Delta: 1})
	case "alt+b", "alt+f":
		// Many macOS terminals send alt+← and alt+→ as these word motions;
		// on an empty composer they switch agents, as in Codex.
		if draft == "" {
			delta := 1
			if msg.String() == "alt+b" {
				delta = -1
			}

			return m.dispatch(state.SwitchAgent{Delta: delta})
		}
	case "ctrl+v", "alt+v":
		// Paste the clipboard's image, as Codex and Claude Code do on
		// macOS and Linux; text pastes arrive as a bracketed paste.
		return m.dispatch(state.PasteImage{})
	case keyBackspace:
		if draft == "" {
			return m.dispatch(state.LeaveShell{}) // shell mode ends; nothing to delete
		}
		m.eatPlaceholder()
	case "shift+tab":
		return m.dispatch(state.CycleMode{})
	case "alt+,":
		return m.dispatch(state.StepEffort{Delta: -1})
	case "alt+.":
		return m.dispatch(state.StepEffort{Delta: 1})
	case "alt+e":
		return m.dispatch(state.CycleAdaptive{})
	case "ctrl+s":
		return m.dispatch(state.OpenPicker{})
	case keyCtrlN:
		return m.dispatch(state.NewSession{})
	case "!":
		if draft == "" {
			return m.dispatch(state.EnterShell{})
		}
	case "ctrl+g":
		return m.dispatch(state.EditDraft{Draft: draft})
	case "ctrl+r":
		return m.dispatch(state.SearchOpen{Draft: draft})
	case "ctrl+t":
		return m.dispatch(state.ToggleDetails{})
	}

	return m.typeKey(msg)
}

// typeKey gives the key to the composer.
func (m Model) typeKey(msg term.KeyPressMsg) (term.Model, term.Cmd) {
	draft := m.composer.Value()
	cmd := m.composerAction(m.composer.Press(composer.Key{Name: msg.String(), Text: msg.Text}))
	if next := m.composer.Value(); next != draft {
		model, effects := m.dispatch(state.DraftChanged{Draft: next})

		return model, term.Batch(cmd, effects)
	}

	return m, cmd
}

// questionIntent maps a key while the agent's questions show. On the
// options: ↑↓ choose, a number picks, n notes the chosen option, enter
// answers, tab and shift+tab go between the questions, and esc (or ctrl+c)
// interrupts the run. While the composer holds an answer of the user's own
// or a note (typing), letters and numbers type, ↑↓ leave the own answer
// (not a note), enter answers or keeps the note, and esc drops the note or
// interrupts. nil leaves the key to the composer.
func questionIntent(key, draft string, typing bool) any {
	switch key {
	case keyUp, keyCtrlP:
		return state.QuestionMove{Delta: -1, Draft: draft}
	case keyDown, keyCtrlN:
		return state.QuestionMove{Delta: 1, Draft: draft}
	case keyEnter, state.KeyCtrlEnter, state.KeyAltEnter:
		return state.QuestionAnswer{Draft: draft}
	case state.KeyTab:
		return state.QuestionSwitch{Delta: 1, Draft: draft}
	case "shift+tab":
		return state.QuestionSwitch{Delta: -1, Draft: draft}
	case keyEsc:
		return state.QuestionDismiss{}
	case keyCtrlC:
		if draft == "" {
			return state.QuestionDismiss{}
		}
	}
	if typing {
		return nil
	}
	if key == "n" {
		return state.QuestionNote{}
	}
	if len(key) == 1 && key[0] >= '1' && key[0] <= '9' {
		return state.QuestionPick{Number: int(key[0] - '0')}
	}

	return nil
}

// backtrackIntent maps a key while an earlier message is selected, as
// Codex's backtrack: esc, ↑, and ← select an earlier message, ↓ and → a
// later one, enter goes back to it, and ctrl+c cancels. nil means the key
// cancels and does what it does.
func backtrackIntent(key string) any {
	switch key {
	case keyEsc:
		return state.Esc{Empty: true}
	case keyUp, "left":
		return state.BacktrackMove{Delta: -1}
	case keyDown, "right":
		return state.BacktrackMove{Delta: 1}
	case keyEnter:
		return state.BacktrackSelect{}
	case keyCtrlC:
		return state.BacktrackCancel{}
	}

	return nil
}

// onApprovalKey answers the approval overlay: y approves, s approves and
// allows the proposed prefix, a approves and always allows the MCP tool,
// n, esc, and ctrl+c decline. Other keys wait.
func (m Model) onApprovalKey(msg term.KeyPressMsg) (term.Model, term.Cmd) {
	switch msg.String() {
	case "y":
		return m.dispatch(state.Answer{Answer: approval.Approve})
	case "s", "p":
		return m.dispatch(state.Answer{Answer: approval.ApprovePrefix})
	case "a":
		return m.dispatch(state.Answer{Answer: approval.ApproveTool})
	case "n", keyEsc, keyCtrlC:
		return m.dispatch(state.Answer{Answer: approval.Decline})
	}

	return m, nil
}

func (m Model) onPickerKey(msg term.KeyPressMsg) (term.Model, term.Cmd) {
	switch msg.String() {
	case keyUp, keyCtrlP:
		return m.dispatch(state.PickerMove{Delta: -1})
	case keyDown, keyCtrlN:
		return m.dispatch(state.PickerMove{Delta: 1})
	case keyEnter:
		return m.dispatch(state.PickerChoose{})
	case keyCtrlC:
		if m.st.SessionID == "" {
			return m.dispatch(state.Quit{}) // the startup picker: quit, as Codex does
		}

		return m.dispatch(state.PickerCancel{})
	case keyEsc:
		return m.dispatch(state.PickerCancel{})
	case keyBackspace:
		return m.dispatch(state.PickerType{Text: "\b"})
	case "tab":
		return m.dispatch(state.PickerToggleAll{})
	}
	if msg.Text != "" {
		return m.dispatch(state.PickerType{Text: msg.Text})
	}

	return m, nil
}

// wheelLines is how far one mouse wheel step scrolls.
const wheelLines = 3

func (m Model) onWheel(msg term.MouseWheelMsg) (term.Model, term.Cmd) {
	if m.st.Mode == state.ModePicker {
		return m, nil
	}
	lines := wheelLines
	switch msg.Button {
	case term.MouseWheelUp:
	case term.MouseWheelDown:
		lines = -wheelLines
	default:
		return m, nil
	}
	model, cmd := m.scroll(lines)
	if sel := m.st.Selection; sel != nil && sel.Dragging {
		// The text under the mouse moved: the selection follows it.
		m = model.(Model) //nolint:forcetypeassert // scroll returns a Model
		m.View()
		next, drag := m.drag(msg.X, msg.Y)

		return next, term.Batch(cmd, drag)
	}

	return model, cmd
}

// scroll moves the transcript, stopping at its first line.
func (m Model) scroll(lines int) (term.Model, term.Cmd) {
	if lines > 0 {
		m.View() // refresh the limit: frames can lag behind a burst of wheel events
		scrolled := m.st.Scroll
		if m.st.View != nil {
			scrolled = m.st.View.St.Scroll
		}
		if limit := m.cache.MaxScroll(); limit >= 0 {
			lines = max(min(lines, limit-scrolled), 0)
		}
	}
	if lines == 0 {
		return m, nil
	}

	return m.dispatch(state.ScrollBy{Lines: lines})
}

// menuIntent maps a key to a menu intent while the menu is open, or nil.
func menuIntent(st state.State, key, draft string) any {
	if !st.MenuOpen(draft) {
		return nil
	}
	switch key {
	case "tab":
		return state.MenuAccept{Draft: draft}
	case keyEnter:
		return state.MenuEnter{Draft: draft}
	case keyUp, keyCtrlP:
		return state.MenuMove{Draft: draft, Delta: -1}
	case keyDown, keyCtrlN:
		return state.MenuMove{Draft: draft, Delta: 1}
	case keyEsc:
		return state.MenuClose{Draft: draft}
	}

	return nil
}

// onConfigKey drives the /config panel: ↑↓ choose, enter or space change,
// ← → cycle back and forth, esc closes (or stops typing a value).
func (m Model) onConfigKey(msg term.KeyPressMsg) (term.Model, term.Cmd) {
	editing := m.st.Config.Editing
	switch msg.String() {
	case keyUp, keyCtrlP:
		return m.dispatch(state.ConfigMove{Delta: -1})
	case keyDown, keyCtrlN:
		return m.dispatch(state.ConfigMove{Delta: 1})
	case keyEnter:
		return m.dispatch(state.ConfigEnter{})
	case keyEsc, keyCtrlC:
		return m.dispatch(state.ConfigEsc{})
	case "left":
		if !editing {
			return m.dispatch(state.ConfigChange{Delta: -1})
		}
	case "right":
		if !editing {
			return m.dispatch(state.ConfigChange{Delta: 1})
		}
	case "space":
		if !editing {
			return m.dispatch(state.ConfigChange{Delta: 1})
		}
	case keyBackspace:
		return m.dispatch(state.ConfigType{Text: "\b"})
	}
	if editing && msg.Text != "" {
		return m.dispatch(state.ConfigType{Text: msg.Text})
	}

	return m, nil
}

// onModelPickerKey drives the /model picker: ↑↓ choose, enter picks the
// model or applies the effort, esc goes back to the models or closes.
func (m Model) onModelPickerKey(msg term.KeyPressMsg) (term.Model, term.Cmd) {
	switch msg.String() {
	case keyUp, keyCtrlP:
		return m.dispatch(state.ModelPickMove{Delta: -1})
	case keyDown, keyCtrlN:
		return m.dispatch(state.ModelPickMove{Delta: 1})
	case keyEnter:
		return m.dispatch(state.ModelPickEnter{})
	case keyEsc, keyCtrlC:
		return m.dispatch(state.ModelPickEsc{})
	}

	return m, nil
}

// onFirstRow reports whether the composer's cursor is on its first visual
// row, so ↑ has no line to move to.
func (m Model) onFirstRow() bool {
	return m.composer.Line() == 0 && m.composer.LineInfo().RowOffset == 0
}

// onLastRow reports whether the cursor is on the composer's last visual
// row, so ↓ has no line to move to.
func (m Model) onLastRow() bool {
	info := m.composer.LineInfo()

	return m.composer.Line() == m.composer.LineCount()-1 && info.RowOffset >= info.Height-1
}
