package bubble

import (
	"strings"
	"unicode/utf8"

	"charm.land/lipgloss/v2"

	"github.com/viktordanov/uah/internal/tui/composer"
	"github.com/viktordanov/uah/internal/tui/render"
	"github.com/viktordanov/uah/internal/tui/term"
)

// minComposerRows is the composer's height limit on a short terminal.
const minComposerRows = 8

// composerRows is how tall the composer grows on a terminal h rows high:
// half the screen, and at least 8 rows, so a long prompt shows more of
// itself and the transcript keeps the other half. Codex caps it only at
// the screen's height, but its transcript lives in the terminal's
// scrollback, where uah's shares the screen.
func composerRows(h int) int { return max(minComposerRows, h/2) }

// resizeComposer fits the composer to the window: its width, and its
// height limit, which SetWidth applies at once.
func (m *Model) resizeComposer() {
	m.composer.SetMaxHeight(composerRows(m.h))
	m.composer.SetWidth(m.w)
}

// newComposer makes the composer: its key map is textarea's, with
// shift+enter and ctrl+j for a new line, as enter sends.
func newComposer(theme *render.Styles) composer.Composer {
	c := composer.New()
	c.Placeholder = "Ask uah to do anything · / for commands"
	// The λ marks the composer's first row only; the rows below it line up
	// under the text, as Codex's composer does.
	c.SetPrompt(2, firstRowPrompt("λ "))
	// The composer grows to composerRows and then scrolls to keep the
	// cursor in view; the draft's only limit is composer.MaxLines.
	c.SetMaxHeight(minComposerRows)
	c.SetStyles(composerStyles(theme))

	return c
}

// firstRowPrompt marks the composer's first row only: λ, or ! in shell
// mode (render.ShellPrompt).
func firstRowPrompt(mark string) func(int) string {
	return func(row int) string {
		if row == 0 {
			return mark
		}

		return "  "
	}
}

// syncShell draws the composer for shell mode after it changed, and for
// the agent's questions: its mark and placeholder come from the state,
// through render.
func (m *Model) syncShell(was bool) {
	m.composer.Placeholder = render.ShellPlaceholder(m.st)
	if m.st.Shell == was {
		return
	}
	m.composer.SetPrompt(2, firstRowPrompt(render.ShellPrompt(m.st)))
}

// composerStyles draw the composer in the theme: the λ in the accent, the
// placeholder dim, and a selection (shift+←/→) on textarea's gray. The
// text has no colors or backgrounds of its own, since the screen puts it
// on the band.
func composerStyles(theme *render.Styles) composer.Styles {
	return composer.Styles{
		Prompt:      renderWith(theme.Accent()),
		Placeholder: renderWith(theme.Dim()),
		Selection:   renderWith(lipgloss.NewStyle().Background(lipgloss.Color("8"))),
	}
}

func renderWith(st lipgloss.Style) func(string) string {
	return func(s string) string { return st.Render(s) }
}

// composerCursor is the terminal's cursor at the composer's: textarea's
// default, a blinking block in color 7.
func (m Model) composerCursor() *term.Cursor {
	x, y, ok := m.composer.Cursor()
	if !ok {
		return nil
	}
	c := term.NewCursor(x, y)
	c.Blink = true
	c.Color = lipgloss.Color("7")
	c.Shape = term.CursorBlock

	return c
}

// composerAction does the I/O a key asked of the composer: ctrl+v pastes
// the clipboard's text (Deps.PasteText), and ctrl+shift+c copies the
// selection, as OSC 52 and with the system's tool (Deps.CopyText), as a
// transcript selection is copied.
func (m Model) composerAction(act composer.Action) term.Cmd {
	switch act {
	case composer.ActionPaste:
		ctx, read := m.ctx, m.deps.PasteText
		if read == nil {
			return nil
		}

		return func() term.Msg {
			text, err := read(ctx)
			if err != nil || text == "" {
				return nil
			}

			return term.PasteMsg{Content: text}
		}
	case composer.ActionCopy:
		text := m.composer.SelectedText()
		if text == "" {
			return nil
		}
		ctx, write := m.ctx, m.deps.CopyText

		return term.Batch(term.SetClipboard(text), func() term.Msg {
			if write != nil {
				_ = write(ctx, text) // as textarea, a failed copy says nothing
			}

			return nil
		})
	case composer.ActionNone:
	}

	return nil
}

// atEdge reports whether the composer's cursor is at the draft's start or
// end, where ↑ and ↓ recall a prompt left as it was (state.Recalls).
func (m Model) atEdge() bool {
	line, col := m.composer.Line(), m.composer.Column()
	if line == 0 && col == 0 {
		return true
	}
	lines := strings.Split(m.composer.Value(), "\n")

	return line == len(lines)-1 && col == utf8.RuneCountInString(lines[line])
}
