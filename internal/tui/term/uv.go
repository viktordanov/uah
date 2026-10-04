package term

// This file is the only one that names ultraviolet: its decoder turns the
// terminal's bytes into events, the same decoder Bubble Tea used, so keys
// keep their names. Everything past it is term's own types.

import (
	"context"
	"io"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// String names the key: its text when it typed some (but "space" for a
// space), otherwise its keystroke.
func (k Key) String() string { return toUV(k).String() }

// Keystroke names the key with its modifiers, in the order ctrl, alt,
// shift, meta, hyper, super: "ctrl+shift+a", never "shift+ctrl+a".
func (k Key) Keystroke() string { return toUV(k).Keystroke() }

func toUV(k Key) uv.Key {
	return uv.Key{Text: k.Text, Mod: uv.KeyMod(k.Mod), Code: k.Code, ShiftedCode: k.ShiftedCode, BaseCode: k.BaseCode, IsRepeat: k.IsRepeat}
}

func fromUV(k uv.Key) Key {
	return Key{Text: k.Text, Mod: KeyMod(k.Mod), Code: k.Code, ShiftedCode: k.ShiftedCode, BaseCode: k.BaseCode, IsRepeat: k.IsRepeat}
}

func mouseFromUV(m uv.Mouse) Mouse {
	return Mouse{X: m.X, Y: m.Y, Button: m.Button, Mod: KeyMod(m.Mod)}
}

// KeyExtended is the code of a key that typed several runes; Text holds
// them.
const KeyExtended = uv.KeyExtended

// Special keys.
const (
	KeyUp          = uv.KeyUp
	KeyDown        = uv.KeyDown
	KeyRight       = uv.KeyRight
	KeyLeft        = uv.KeyLeft
	KeyBegin       = uv.KeyBegin
	KeyFind        = uv.KeyFind
	KeyInsert      = uv.KeyInsert
	KeyDelete      = uv.KeyDelete
	KeySelect      = uv.KeySelect
	KeyPgUp        = uv.KeyPgUp
	KeyPgDown      = uv.KeyPgDown
	KeyHome        = uv.KeyHome
	KeyEnd         = uv.KeyEnd
	KeyKpEnter     = uv.KeyKpEnter
	KeyKpEqual     = uv.KeyKpEqual
	KeyKpMultiply  = uv.KeyKpMultiply
	KeyKpPlus      = uv.KeyKpPlus
	KeyKpComma     = uv.KeyKpComma
	KeyKpMinus     = uv.KeyKpMinus
	KeyKpDecimal   = uv.KeyKpDecimal
	KeyKpDivide    = uv.KeyKpDivide
	KeyKp0         = uv.KeyKp0
	KeyKp1         = uv.KeyKp1
	KeyKp2         = uv.KeyKp2
	KeyKp3         = uv.KeyKp3
	KeyKp4         = uv.KeyKp4
	KeyKp5         = uv.KeyKp5
	KeyKp6         = uv.KeyKp6
	KeyKp7         = uv.KeyKp7
	KeyKp8         = uv.KeyKp8
	KeyKp9         = uv.KeyKp9
	KeyKpSep       = uv.KeyKpSep
	KeyKpUp        = uv.KeyKpUp
	KeyKpDown      = uv.KeyKpDown
	KeyKpLeft      = uv.KeyKpLeft
	KeyKpRight     = uv.KeyKpRight
	KeyKpPgUp      = uv.KeyKpPgUp
	KeyKpPgDown    = uv.KeyKpPgDown
	KeyKpHome      = uv.KeyKpHome
	KeyKpEnd       = uv.KeyKpEnd
	KeyKpInsert    = uv.KeyKpInsert
	KeyKpDelete    = uv.KeyKpDelete
	KeyKpBegin     = uv.KeyKpBegin
	KeyF1          = uv.KeyF1
	KeyF2          = uv.KeyF2
	KeyF3          = uv.KeyF3
	KeyF4          = uv.KeyF4
	KeyF5          = uv.KeyF5
	KeyF6          = uv.KeyF6
	KeyF7          = uv.KeyF7
	KeyF8          = uv.KeyF8
	KeyF9          = uv.KeyF9
	KeyF10         = uv.KeyF10
	KeyF11         = uv.KeyF11
	KeyF12         = uv.KeyF12
	KeyCapsLock    = uv.KeyCapsLock
	KeyScrollLock  = uv.KeyScrollLock
	KeyNumLock     = uv.KeyNumLock
	KeyPrintScreen = uv.KeyPrintScreen
	KeyPause       = uv.KeyPause
	KeyMenu        = uv.KeyMenu
	KeyLeftShift   = uv.KeyLeftShift
	KeyLeftAlt     = uv.KeyLeftAlt
	KeyLeftCtrl    = uv.KeyLeftCtrl
	KeyLeftSuper   = uv.KeyLeftSuper
	KeyLeftHyper   = uv.KeyLeftHyper
	KeyLeftMeta    = uv.KeyLeftMeta
	KeyRightShift  = uv.KeyRightShift
	KeyRightAlt    = uv.KeyRightAlt
	KeyRightCtrl   = uv.KeyRightCtrl
	KeyRightSuper  = uv.KeyRightSuper
	KeyRightHyper  = uv.KeyRightHyper
	KeyRightMeta   = uv.KeyRightMeta
	KeyBackspace   = uv.KeyBackspace
	KeyTab         = uv.KeyTab
	KeyEnter       = uv.KeyEnter
	KeyReturn      = uv.KeyReturn
	KeyEscape      = uv.KeyEscape
	KeyEsc         = uv.KeyEsc
	KeySpace       = uv.KeySpace
)

// modeReportMsg is the terminal's answer to a DECRQM query.
type modeReportMsg struct {
	mode  ansi.Mode
	value ansi.ModeSetting
}

// readInput decodes the terminal's input from r and sends each event, as a
// Msg, until ctx ends or r fails. term is $TERM, which picks the decoder's
// legacy key table.
func readInput(ctx context.Context, r io.Reader, termType string, out chan<- Msg) error {
	events := make(chan uv.Event, 64)
	errc := make(chan error, 1)
	go func() { errc <- uv.NewTerminalReader(r, termType).StreamEvents(ctx, events) }()
	for {
		select {
		case ev := <-events:
			if msg := translate(ev); msg != nil {
				select {
				case out <- msg:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		case err := <-errc:
			return err //nolint:wrapcheck // the reader's own error, wrapped by the caller
		}
	}
}

// translate turns a decoded event into term's message, or nil for an event
// uah does not read.
func translate(e uv.Event) Msg { //nolint:gocyclo // a type switch over the decoder's events
	switch e := e.(type) {
	case uv.KeyPressEvent:
		return KeyPressMsg(fromUV(uv.Key(e)))
	case uv.KeyReleaseEvent:
		return KeyReleaseMsg(fromUV(uv.Key(e)))
	case uv.MouseClickEvent:
		return MouseClickMsg(mouseFromUV(uv.Mouse(e)))
	case uv.MouseReleaseEvent:
		return MouseReleaseMsg(mouseFromUV(uv.Mouse(e)))
	case uv.MouseMotionEvent:
		return MouseMotionMsg(mouseFromUV(uv.Mouse(e)))
	case uv.MouseWheelEvent:
		return MouseWheelMsg(mouseFromUV(uv.Mouse(e)))
	case uv.PasteEvent:
		return PasteMsg{Content: e.Content}
	case uv.FocusEvent:
		return FocusMsg{}
	case uv.BlurEvent:
		return BlurMsg{}
	case uv.BackgroundColorEvent:
		return BackgroundColorMsg{Color: e.Color}
	case uv.KeyboardEnhancementsEvent:
		return KeyboardEnhancementsMsg{Flags: e.Flags}
	case uv.ModeReportEvent:
		return modeReportMsg{mode: e.Mode, value: e.Value}
	case uv.MultiEvent:
		msgs := make(multiMsg, 0, len(e))
		for _, ev := range e {
			if m := translate(ev); m != nil {
				msgs = append(msgs, m)
			}
		}

		return msgs
	}

	return nil
}

// multiMsg is several messages decoded from one sequence.
type multiMsg []Msg
