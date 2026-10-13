package bubble

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/viktordanov/uah/internal/tui/state"
	"github.com/viktordanov/uah/internal/tui/term"
)

// The messages that come often while a run streams do not copy the draft
// to see whether it changed; the ones that can edit it do.
func TestMayEditDraft(t *testing.T) {
	for _, msg := range []term.Msg{eventsMsg{}, tickMsg{}, agentEventsMsg{}, term.MouseMotionMsg{}, term.MouseWheelMsg{}, edgeMsg{}, term.WindowSizeMsg{}} {
		assert.False(t, mayEditDraft(msg), "%T", msg)
	}
	for _, msg := range []term.Msg{term.KeyPressMsg{}, term.PasteMsg{}, withdrawnMsg{}, state.DraftEdited{}, state.ImageAttached{}} {
		assert.True(t, mayEditDraft(msg), "%T", msg)
	}
}
