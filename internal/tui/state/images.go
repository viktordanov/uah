package state

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/viktordanov/uah/internal/images"
	"github.com/viktordanov/uah/internal/session"
)

// Images pasted into the composer, as Codex and Claude Code have them:
// ctrl+v pastes the clipboard's image, and a pasted or dropped image path
// (or an image chosen after "@") attaches that file. Each image gets a
// placeholder in the text, "[Image #1]"; deleting the placeholder drops the
// image, and on send the images still in the text go with the message.
type (
	// PasteImage is ctrl+v (or alt+v): attach the clipboard's image. The
	// shell pastes the clipboard's text instead when it holds no image.
	PasteImage struct{}
	// AttachFile attaches an image file. Text is what was pasted, put in
	// the composer as it is when the file cannot be attached.
	AttachFile struct{ Path, Text string }
	// ImageAttached is an image read and stored, ready for its placeholder.
	ImageAttached struct{ Image images.Image }
	// ImageFailed is an image that could not be attached; Text, when set,
	// goes into the composer instead, as a text paste would.
	ImageFailed struct {
		Err  error
		Text string
	}
	// DraftRestored puts a message taken back from the queue into the
	// composer with its images.
	DraftRestored struct{ Text string }
)

type (
	// EffPasteImage reads the clipboard's image and stores it.
	EffPasteImage struct{}
	// EffAttachFile reads an image file and stores it.
	EffAttachFile struct{ Path, Text string }
	// EffInsertText inserts text at the composer's cursor.
	EffInsertText struct{ Text string }
)

func (EffPasteImage) effect() {}
func (EffAttachFile) effect() {}
func (EffInsertText) effect() {}

// onImages handles the image intents; ok is false for any other event.
func (s *State) onImages(ev any) (effects []Effect, ok bool) {
	switch e := ev.(type) {
	case PasteImage:
		return []Effect{EffPasteImage{}}, true
	case AttachFile:
		return []Effect{EffAttachFile(e)}, true
	case ImageAttached:
		img := e.Image
		img.Label = images.Label(s.nextImage())
		s.Attached = append(s.Attached, img)

		return []Effect{EffInsertText{Text: img.Label + " "}}, true
	case ImageFailed:
		s.notice(session.LevelWarning, "could not attach the image: "+e.Err.Error())

		return insert(e.Text), true
	case DraftRestored:
		text, imgs := images.Split(e.Text)
		s.Attached = imgs

		return []Effect{EffSetDraft{Text: text}}, true
	}

	return nil, false
}

func insert(text string) []Effect {
	if text == "" {
		return nil
	}

	return []Effect{EffInsertText{Text: text}}
}

// nextImage numbers a new image after the ones in the draft, so a label is
// never reused while its image is attached.
func (s *State) nextImage() int {
	n := 0
	for _, img := range s.Attached {
		var k int
		if _, err := fmt.Sscanf(img.Label, "[Image #%d]", &k); err == nil {
			n = max(n, k)
		}
	}

	return n + 1
}

// pruneImages drops the images whose placeholder the draft lost.
func (s *State) pruneImages(draft string) {
	s.Attached = slices.DeleteFunc(s.Attached, func(img images.Image) bool {
		return !strings.Contains(draft, img.Label)
	})
}

// withImages adds the draft's images that the text still names to a
// message, and empties the draft's images.
func (s *State) withImages(text string) string {
	s.pruneImages(text)
	out := images.Join(text, s.Attached)
	s.Attached = nil

	return out
}

// acceptImage attaches an image file chosen after "@" in place of its path,
// as Codex does; ok is false for any other suggestion.
func (s *State) acceptImage(draft string, picked Suggestion) ([]Effect, bool) {
	at, ok := mentionAt(draft)
	if !ok || picked.Resource || !images.IsImagePath(picked.Label) {
		return nil, false
	}
	path := picked.Label
	if !filepath.IsAbs(path) && s.Settings.Workspace != "" {
		path = filepath.Join(s.Settings.Workspace, path)
	}

	return []Effect{EffSetDraft{Text: draft[:at]}, EffAttachFile{Path: path, Text: picked.Label + " "}}, true
}
