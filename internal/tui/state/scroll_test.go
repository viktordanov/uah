package state_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/tui/state"
)

// TestScroll_NewOutputBelow: the shell reports the pinned window; more
// lines below the same anchor in the same layout are new output, which
// shows until the window follows the bottom again. Scrolling back and forth
// keeps it.
func TestScroll_NewOutputBelow(t *testing.T) {
	s, keys := selectable(t)
	bottom := at(keys[1], 1, 0)
	s, _ = apply(s, state.ScrollBy{Lines: 5})
	assert.Empty(t, s.Anchor, "scrolling drops the anchor: the shell reports the new window")
	s, _ = apply(s, state.Anchored{At: bottom, Scroll: 5, Width: 80})
	assert.Equal(t, bottom, s.Anchor)
	assert.False(t, s.NewBelow, "a first report")

	s, _ = apply(s, state.Anchored{At: bottom, Scroll: 9, Width: 80})
	assert.Equal(t, 9, s.Scroll, "the lines below the anchor")
	assert.True(t, s.NewBelow)

	s, _ = apply(s, state.ScrollBy{Lines: -3}, state.Anchored{At: at(keys[2], 0, 0), Scroll: 6, Width: 80}, state.ScrollBy{Lines: 2})
	assert.True(t, s.NewBelow, "scrolling keeps it")

	s, _ = apply(s, state.ScrollToBottom{})
	assert.Zero(t, s.Scroll)
	assert.Empty(t, s.Anchor)
	assert.False(t, s.NewBelow, "the bottom clears it")

	s, _ = apply(s, state.ScrollBy{Lines: 2}, state.Anchored{At: bottom, Scroll: 2, Width: 80}, state.Anchored{At: bottom, Scroll: 4, Width: 80})
	require.True(t, s.NewBelow)
	s, _ = apply(s, state.ScrollBy{Lines: -10})
	assert.False(t, s.NewBelow, "scrolling down to the bottom clears it too")
	s, _ = apply(s, state.Anchored{At: bottom, Scroll: 7, Width: 80})
	assert.Zero(t, s.Scroll, "a following window takes no report")
}

// TestScroll_NotNewOutput: more lines below after a resize, ctrl+t, or
// /reasoning are the same output drawn again, and a new anchor (an item
// that left) has nothing to compare.
func TestScroll_NotNewOutput(t *testing.T) {
	s, keys := selectable(t)
	bottom := at(keys[1], 1, 0)
	report := func(s state.State, at state.TextPos, scroll, width int) state.State {
		s, _ = apply(s, state.Anchored{At: at, Scroll: scroll, Width: width})

		return s
	}
	s, _ = apply(s, state.ScrollBy{Lines: 5})
	s = report(s, bottom, 5, 80)

	s = report(s, bottom, 8, 60)
	assert.False(t, s.NewBelow, "a narrower window")
	s, _ = apply(s, state.ToggleDetails{})
	s = report(s, bottom, 12, 60)
	assert.False(t, s.NewBelow, "the detailed view")
	s, _ = apply(s, state.Submit{Text: "/reasoning"})
	s = report(s, bottom, 14, 60)
	assert.False(t, s.NewBelow, "reasoning shown")

	s = report(s, at(keys[0], 0, 0), 9, 60)
	assert.False(t, s.NewBelow, "another anchor")
	s = report(s, at(keys[0], 0, 0), 9, 60)
	assert.False(t, s.NewBelow, "the same lines")
}

// TestScroll_DragPins: a drag that selects pins the window at the bottom
// too, so streamed output does not move the text under the mouse; a plain
// press does not, and letting go at the bottom follows again.
func TestScroll_DragPins(t *testing.T) {
	s, keys := selectable(t)
	s, _ = apply(s, state.MousePress{At: at(keys[1], 0, 0), When: t0})
	assert.False(t, s.Pinned(), "a press alone")
	s, _ = apply(s, state.MouseDrag{At: at(keys[1], 1, 2)})
	require.True(t, s.Pinned())

	bottom := at(keys[2], 0, 0)
	s, _ = apply(s, state.Anchored{At: bottom, Scroll: 0, Width: 80})
	assert.Equal(t, bottom, s.Anchor)
	s, _ = apply(s,
		core.TurnStarted{At: t0, Turn: 2},
		engine.TextDelta{At: t0, ItemID: "a", Text: "streaming"},
		state.Anchored{At: bottom, Scroll: 3, Width: 80},
	)
	assert.Equal(t, 3, s.Scroll, "the window stayed on its text")
	assert.True(t, s.NewBelow)

	s, _ = apply(s, state.MouseRelease{})
	assert.True(t, s.Pinned(), "still scrolled up after the copy")
	assert.True(t, s.NewBelow)

	s, _ = apply(s, state.ScrollToBottom{}, state.MousePress{At: at(keys[1], 0, 0), When: t0.Add(time.Second)}, state.MouseDrag{At: at(keys[1], 0, 3)})
	s, _ = apply(s, state.Anchored{At: bottom, Scroll: 0, Width: 80}, state.MouseRelease{})
	assert.False(t, s.Pinned())
	assert.Empty(t, s.Anchor, "nothing arrived: the window follows again")
}
