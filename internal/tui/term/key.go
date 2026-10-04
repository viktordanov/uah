package term

// Key is a key press or release, as the terminal's input decoder reports
// it. String names it as "enter", "ctrl+c", "shift+tab", or the text typed.
type Key struct {
	// Text is the printable text the key typed, such as "a" or "A"; empty
	// for special keys (enter, tab) and for combinations with ctrl or alt.
	Text string
	// Mod is the modifiers held.
	Mod KeyMod
	// Code is the key: a special key such as KeyEnter or KeyUp, or the
	// character, such as 'a'.
	Code rune
	// ShiftedCode is the shifted key and BaseCode the key on a US PC-101
	// layout; the kitty keyboard protocol reports them.
	ShiftedCode rune
	BaseCode    rune
	// IsRepeat is set while the key is held (kitty protocol only).
	IsRepeat bool
}

// KeyPressMsg is a key pressed.
type KeyPressMsg Key

// String names the key, as Key.String does.
func (k KeyPressMsg) String() string { return Key(k).String() }

// Keystroke names the key with its modifiers, as Key.Keystroke does.
func (k KeyPressMsg) Keystroke() string { return Key(k).Keystroke() }

// Key returns the key.
func (k KeyPressMsg) Key() Key { return Key(k) }

// KeyReleaseMsg is a key released (kitty protocol only).
type KeyReleaseMsg Key

// String names the key, as Key.String does.
func (k KeyReleaseMsg) String() string { return Key(k).String() }

// KeyMod is the modifier keys held.
type KeyMod int

// Modifier keys, in the decoder's bit order.
const (
	ModShift KeyMod = 1 << iota
	ModAlt
	ModCtrl
	ModMeta
	ModHyper
	ModSuper
	ModCapsLock
	ModNumLock
	ModScrollLock
)

// Contains reports whether m holds every modifier of mods.
func (m KeyMod) Contains(mods KeyMod) bool { return m&mods == mods }
