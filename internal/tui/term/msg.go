// Package term is uah's terminal layer: the message and command types the
// TUI model speaks, an event loop that runs the model on one goroutine, and
// a line renderer that writes only the rows of the screen that changed. It
// replaces Bubble Tea; ultraviolet decodes the terminal's input, in uv.go
// alone.
package term

import (
	"image/color"
	"io"
	"time"
)

// Msg is anything the model's Update takes: input from the terminal, a
// command's result, or a message of the model's own.
type Msg = any

// Cmd is work done off the loop, in a goroutine of its own; its result
// comes back to Update. A nil Cmd does nothing.
type Cmd func() Msg

// Model is what Run runs. Init returns the first command, Update takes each
// message, and View draws the screen, once per frame.
type Model interface {
	Init() Cmd
	Update(msg Msg) (Model, Cmd)
	View() View
}

// View is one frame.
type View struct {
	// Content is the screen's lines, joined by "\n"; lines past the
	// screen's height are dropped from the top. Each line must fit the
	// width: with autowrap off, the cells past it overwrite the last
	// column.
	Content string
	// Cursor is where the terminal's cursor shows; nil hides it.
	Cursor *Cursor
	// Mouse reports the mouse's buttons, drags, and wheel (modes 1002 and
	// 1006); off, the terminal keeps its own selection.
	Mouse bool
	// WindowTitle is the terminal's title (OSC 2); "" leaves it alone, or
	// clears the one set before.
	WindowTitle string
}

// NewView returns a view of content.
func NewView(content string) View { return View{Content: content} }

// Position is a cell on the screen, from the top left (0, 0).
type Position struct{ X, Y int }

// Cursor is the terminal's cursor: where it is, its shape, whether it
// blinks, and its colour (nil: the terminal's own).
type Cursor struct {
	Position

	Shape CursorShape
	Blink bool
	Color color.Color
}

// NewCursor returns a steady block cursor at (x, y) in the terminal's
// colour.
func NewCursor(x, y int) *Cursor { return &Cursor{X: x, Y: y} }

// CursorShape is the cursor's shape.
type CursorShape int

// Cursor shapes.
const (
	CursorBlock CursorShape = iota
	CursorUnderline
	CursorBar
)

// style is the cursor's DECSCUSR number: 1 and 2 a blinking and a steady
// block, 3 and 4 an underline, 5 and 6 a bar.
func (c *Cursor) style() int {
	n := 2 + 2*int(c.Shape)
	if c.Blink {
		n--
	}

	return n
}

// BatchMsg runs each of its commands in a goroutine of its own, in no order.
type BatchMsg []Cmd

// Batch runs cmds concurrently; nil commands are dropped, and Batch of none
// is nil.
func Batch(cmds ...Cmd) Cmd {
	var valid []Cmd
	for _, c := range cmds {
		if c != nil {
			valid = append(valid, c)
		}
	}
	switch len(valid) {
	case 0:
		return nil
	case 1:
		return valid[0]
	}

	return func() Msg { return BatchMsg(valid) }
}

// QuitMsg ends Run after one last frame.
type QuitMsg struct{}

// Quit is the command that ends Run.
func Quit() Msg { return QuitMsg{} }

// Tick sends fn's message once d has passed.
func Tick(d time.Duration, fn func(time.Time) Msg) Cmd {
	return func() Msg {
		t := time.NewTimer(d)
		defer t.Stop()

		return fn(<-t.C)
	}
}

// setClipboardMsg asks the loop to copy text with OSC 52.
type setClipboardMsg string

// SetClipboard copies text to the system clipboard through the terminal
// (OSC 52), which works over ssh too.
func SetClipboard(text string) Cmd { return func() Msg { return setClipboardMsg(text) } }

// ExecCommand is a program that runs with the terminal released, such as an
// editor. Run blocks until it ends.
type ExecCommand interface {
	Run() error
	SetStdin(r io.Reader)
	SetStdout(w io.Writer)
	SetStderr(w io.Writer)
}

// ExecCallback turns the program's error, nil when it succeeded, into the
// message Update gets after it.
type ExecCallback func(err error) Msg

// execMsg asks the loop to run a program with the terminal released.
type execMsg struct {
	cmd ExecCommand
	fn  ExecCallback
}

// Exec leaves the alt screen and the input modes, runs c on the terminal,
// enters them again, and redraws the whole screen; fn's message follows.
func Exec(c ExecCommand, fn ExecCallback) Cmd {
	return func() Msg { return execMsg{cmd: c, fn: fn} }
}

// WindowSizeMsg is the terminal's size, sent first and on every resize.
type WindowSizeMsg struct{ Width, Height int }

// PasteMsg is text pasted with bracketed paste.
type PasteMsg struct{ Content string }

// String returns the pasted text.
func (p PasteMsg) String() string { return p.Content }

// FocusMsg and BlurMsg report that the terminal gained or lost focus.
type (
	FocusMsg struct{}
	BlurMsg  struct{}
)

// BackgroundColorMsg is the terminal's background colour (OSC 11), asked
// for once at start.
type BackgroundColorMsg struct{ color.Color }

// IsDark reports whether the colour is dark.
func (e BackgroundColorMsg) IsDark() bool {
	if e.Color == nil {
		return true
	}
	r, g, b, _ := e.RGBA()
	// Relative luminance, as lipgloss's isDarkColor.
	l := (0.2126*float64(r) + 0.7152*float64(g) + 0.0722*float64(b)) / 0xffff

	return l < 0.5
}

// KeyboardEnhancementsMsg is the terminal's answer to the kitty keyboard
// query: Flags is the bitmask of the enhancements on (ansi.Kitty*).
// Terminals without the protocol, and tmux, do not answer.
type KeyboardEnhancementsMsg struct{ Flags int }

// SupportsKeyDisambiguation reports whether the terminal tells keys such as
// shift+enter from enter.
func (k KeyboardEnhancementsMsg) SupportsKeyDisambiguation() bool { return k.Flags > 0 }
