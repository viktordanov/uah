package term

import (
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/assert"
)

// The modifiers keep the decoder's bits, so a decoded key converts as is
// and keeps its name.
func TestKeyModMatchesTheDecoder(t *testing.T) {
	assert.Equal(t, []KeyMod{1, 2, 4, 8, 16, 32, 64, 128, 256}, []KeyMod{
		KeyMod(uv.ModShift), KeyMod(uv.ModAlt), KeyMod(uv.ModCtrl), KeyMod(uv.ModMeta), KeyMod(uv.ModHyper),
		KeyMod(uv.ModSuper), KeyMod(uv.ModCapsLock), KeyMod(uv.ModNumLock), KeyMod(uv.ModScrollLock),
	})
	assert.Equal(t, []KeyMod{1, 2, 4, 8, 16, 32, 64, 128, 256},
		[]KeyMod{ModShift, ModAlt, ModCtrl, ModMeta, ModHyper, ModSuper, ModCapsLock, ModNumLock, ModScrollLock})
}

func TestKeyNames(t *testing.T) {
	for _, tc := range []struct {
		key  KeyPressMsg
		want string
	}{
		{KeyPressMsg{Code: KeyEnter}, "enter"},
		{KeyPressMsg{Code: KeyEnter, Mod: ModShift}, "shift+enter"},
		{KeyPressMsg{Code: KeyEnter, Mod: ModCtrl}, "ctrl+enter"},
		{KeyPressMsg{Code: KeyEscape}, "esc"},
		{KeyPressMsg{Code: KeyTab, Mod: ModShift}, "shift+tab"},
		{KeyPressMsg{Code: 'c', Mod: ModCtrl}, "ctrl+c"},
		{KeyPressMsg{Code: 'a', Text: "a"}, "a"},
		{KeyPressMsg{Code: KeySpace, Text: " "}, "space"},
		{KeyPressMsg{Code: ',', Mod: ModAlt}, "alt+,"},
		{KeyPressMsg{Code: KeyPgUp}, "pgup"},
		{KeyPressMsg{Code: KeyUp, Mod: ModShift}, "shift+up"},
		{KeyPressMsg{Code: KeyBackspace}, "backspace"},
	} {
		assert.Equal(t, tc.want, tc.key.String())
	}
}

// A decoded event becomes term's message with the same fields.
func TestTranslate(t *testing.T) {
	k := uv.KeyPressEvent{Code: 'x', Mod: uv.ModCtrl | uv.ModShift}
	assert.Equal(t, KeyPressMsg{Code: 'x', Mod: ModCtrl | ModShift}, translate(k))
	assert.Equal(t, "ctrl+shift+x", translate(k).(KeyPressMsg).String()) //nolint:forcetypeassert // checked above
	assert.Equal(t, MouseWheelMsg{X: 3, Y: 4, Button: MouseWheelDown}, translate(uv.MouseWheelEvent{X: 3, Y: 4, Button: uv.MouseWheelDown}))
	assert.Equal(t, PasteMsg{Content: "hi"}, translate(uv.PasteEvent{Content: "hi"}))
	assert.Equal(t, KeyboardEnhancementsMsg{Flags: 1}, translate(uv.KeyboardEnhancementsEvent{Flags: 1}))
	assert.Nil(t, translate(uv.UnknownEvent("x")))
}
