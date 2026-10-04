package bubble

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/viktordanov/uah/internal/images"
	"github.com/viktordanov/uah/internal/images/clipboard"
	"github.com/viktordanov/uah/internal/tui/composer"
	"github.com/viktordanov/uah/internal/tui/state"
	"github.com/viktordanov/uah/internal/tui/term"
)

// clipboardTimeout bounds one read of the clipboard.
const clipboardTimeout = 10 * time.Second

var errNoImages = errors.New("images cannot be attached here")

// runImage runs the image effects; ok is false for any other effect.
func (m Model) runImage(e state.Effect) (term.Cmd, bool) {
	switch e := e.(type) {
	case state.EffPasteImage:
		return m.pasteImage(), true
	case state.EffAttachFile:
		store := m.deps.Images

		return func() term.Msg {
			if store == nil {
				return state.ImageFailed{Err: errNoImages, Text: e.Text}
			}
			img, err := store.PutFile(e.Path)
			if err != nil {
				return state.ImageFailed{Err: err, Text: e.Text}
			}

			return state.ImageAttached{Image: img}
		}, true
	}

	return nil, false
}

// pasteImage reads the clipboard's image, or the image file copied in a
// file manager, off the update loop and stores it.
func (m Model) pasteImage() term.Cmd {
	ctx, clip, store, pasteText := m.ctx, m.deps.Clipboard, m.deps.Images, m.deps.PasteText

	return func() term.Msg {
		if clip == nil || store == nil {
			return state.ImageFailed{Err: errNoImages}
		}
		ctx, cancel := context.WithTimeout(ctx, clipboardTimeout)
		defer cancel()
		content, err := clip.ReadImage(ctx)
		if errors.Is(err, clipboard.ErrNoImage) {
			// ctrl+v pastes text, as it did before images.
			if pasteText == nil {
				return nil
			}
			text, err := pasteText(ctx)
			if err != nil || text == "" {
				return nil
			}

			return term.PasteMsg{Content: text}
		}
		if err != nil {
			return state.ImageFailed{Err: err}
		}
		var img images.Image
		if content.Path != "" {
			img, err = store.PutFile(content.Path)
		} else {
			img, err = store.Put(content.Data)
		}
		if err != nil {
			return state.ImageFailed{Err: err}
		}

		return state.ImageAttached{Image: img}
	}
}

// onPaste attaches a pasted or dropped image path; any other paste goes to
// the composer as text.
func (m Model) onPaste(msg term.PasteMsg) (term.Model, term.Cmd) {
	if m.st.History.Search != nil {
		return m.dispatch(state.SearchType{Text: msg.Content}) // a paste goes into the query
	}
	if m.st.Mode == state.ModeChat && m.st.Config == nil {
		home, _ := os.UserHomeDir()
		if path, ok := images.PastedPath(msg.Content, home); ok {
			return m.dispatch(state.AttachFile{Path: path, Text: msg.Content})
		}
	}
	m.composer.Paste(msg.Content)

	return m, nil
}

// eatPlaceholder makes backspace at the end of an image's placeholder
// delete the whole placeholder, as Codex's atomic elements do: it deletes
// all of it but the last character, which the key then deletes, so the
// draft change drops the image.
func (m *Model) eatPlaceholder() {
	if len(m.st.Attached) == 0 {
		return
	}
	lines := strings.Split(m.composer.Value(), "\n")
	row := m.composer.Line()
	if row >= len(lines) {
		return
	}
	line := []rune(lines[row])
	before := string(line[:min(m.composer.Column(), len(line))])
	for _, img := range m.st.Attached {
		if strings.HasSuffix(before, img.Label) {
			for range len([]rune(img.Label)) - 1 {
				m.composer.Press(composer.Key{Name: "backspace"})
			}

			return
		}
	}
}
