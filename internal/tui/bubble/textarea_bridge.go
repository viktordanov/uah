package bubble

import (
	tea "charm.land/bubbletea/v2"

	"github.com/viktordanov/uah/internal/tui/term"
)

// Until the composer is uah's own, the textarea speaks Bubble Tea's types:
// these turn term's messages into its own and its commands back.

// updateComposer gives the composer a message.
func (m *Model) updateComposer(msg term.Msg) term.Cmd {
	switch k := msg.(type) {
	case term.KeyPressMsg:
		msg = tea.KeyPressMsg{Text: k.Text, Mod: tea.KeyMod(k.Mod), Code: k.Code, ShiftedCode: k.ShiftedCode, BaseCode: k.BaseCode, IsRepeat: k.IsRepeat}
	case term.PasteMsg:
		msg = tea.PasteMsg{Content: k.Content}
	}
	var cmd tea.Cmd
	m.composer, cmd = m.composer.Update(msg)

	return fromTea(cmd)
}

func fromTea(cmd tea.Cmd) term.Cmd {
	if cmd == nil {
		return nil
	}

	return func() term.Msg { return cmd() }
}

// composerCursor is the composer's cursor, from its top left.
func (m Model) composerCursor() *term.Cursor {
	c := m.composer.Cursor()
	if c == nil {
		return nil
	}

	return term.NewCursor(c.X, c.Y)
}
