package term

import "github.com/charmbracelet/x/ansi"

// Mouse is a mouse event at cell (X, Y), from the top left (0, 0).
type Mouse struct {
	X, Y   int
	Button MouseButton
	Mod    KeyMod
}

// MouseButton is a mouse button, in X11's numbering.
type MouseButton = ansi.MouseButton

// Mouse buttons.
const (
	MouseNone       = ansi.MouseNone
	MouseLeft       = ansi.MouseLeft
	MouseMiddle     = ansi.MouseMiddle
	MouseRight      = ansi.MouseRight
	MouseWheelUp    = ansi.MouseWheelUp
	MouseWheelDown  = ansi.MouseWheelDown
	MouseWheelLeft  = ansi.MouseWheelLeft
	MouseWheelRight = ansi.MouseWheelRight
)

// Mouse events: a button pressed, released, moved while held, and the wheel.
type (
	MouseClickMsg   Mouse
	MouseReleaseMsg Mouse
	MouseMotionMsg  Mouse
	MouseWheelMsg   Mouse
)
