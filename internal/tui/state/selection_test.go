package state_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/internal/tui/state"
)

// selectable is a transcript of a message and two answers; keys are its
// items' keys in order.
func selectable(t *testing.T) (s state.State, keys []string) {
	t.Helper()
	s, _ = apply(opened(),
		core.RunStarted{At: t0, RunID: "r1"},
		core.UserMessage{At: t0, ID: "m1", Text: "hello there"},
		core.AssistantMessage{At: t0, Text: "one\ntwo\nthree"},
		core.AssistantMessage{At: t0, Text: "done", Final: true},
	)
	for _, it := range s.Items {
		if it.Kind == state.KindUser || it.Kind == state.KindAssistant {
			keys = append(keys, it.Key)
		}
	}
	require.Len(t, keys, 3)

	return s, keys
}

func at(key string, line, col int) state.TextPos {
	return state.TextPos{Key: key, Line: line, Col: col}
}

// cols is the selected span of a line as [from, to], or nil.
func cols(s state.State, key string, line int) []int {
	from, to, ok := s.SelectedCols(key, line)
	if !ok {
		return nil
	}

	return []int{from, min(to, 99)}
}

// TestSelection_Drag: a press and a drag select from cell to cell across
// items, in either direction, and the release copies.
func TestSelection_Drag(t *testing.T) {
	s, keys := selectable(t)
	s, effects := apply(s,
		state.MousePress{At: at(keys[0], 2, 4), When: t0},
		state.MouseDrag{At: at(keys[1], 1, 1)},
	)
	assert.Empty(t, effects)
	assert.True(t, s.Selection.Dragging)
	assert.Nil(t, cols(s, keys[0], 1), "above the press")
	assert.Equal(t, []int{4, 99}, cols(s, keys[0], 2), "from the pressed cell to the line's end")
	assert.Equal(t, []int{0, 99}, cols(s, keys[1], 0), "whole lines between")
	assert.Equal(t, []int{0, 2}, cols(s, keys[1], 1), "to the cell under the mouse, included")
	assert.Nil(t, cols(s, keys[1], 2))
	assert.Nil(t, cols(s, keys[2], 0))

	s, effects = apply(s, state.MouseRelease{})
	assert.Equal(t, []state.Effect{state.EffCopySelection{}}, effects)
	assert.False(t, s.Selection.Dragging)
	assert.Equal(t, []int{4, 99}, cols(s, keys[0], 2), "the selection stays after the copy")

	back, _ := apply(s, state.MousePress{At: at(keys[1], 1, 1), When: t0.Add(time.Second)}, state.MouseDrag{At: at(keys[0], 2, 4)})
	assert.Equal(t, []int{4, 99}, cols(back, keys[0], 2), "dragging up selects the same text")
	assert.Equal(t, []int{0, 2}, cols(back, keys[1], 1))
}

// TestSelection_ClickSelectsNothing: a press and release on one cell is a
// click: no selection and nothing copied, and it drops an earlier one.
func TestSelection_ClickSelectsNothing(t *testing.T) {
	s, keys := selectable(t)
	s, _ = apply(s, state.MousePress{At: at(keys[1], 0, 0), When: t0}, state.MouseDrag{At: at(keys[1], 1, 2)}, state.MouseRelease{})
	require.NotNil(t, s.Selection)

	s, effects := apply(s, state.MousePress{At: at(keys[1], 0, 1), When: t0.Add(time.Second)})
	assert.Nil(t, cols(s, keys[1], 0), "a press shows nothing until the mouse moves")
	s, more := apply(s, state.MouseRelease{})
	assert.Nil(t, s.Selection)
	assert.Empty(t, append(effects, more...))
	s, effects = apply(s, state.MouseRelease{})
	assert.Empty(t, effects, "a release without a press")
}

// TestSelection_WordAndLine: a double click selects the word under the
// mouse, a run of non-space counted in cells, a triple click the line, and
// a fourth starts over; a slow second click is a new first one.
func TestSelection_WordAndLine(t *testing.T) {
	s, keys := selectable(t)
	text := "run go test ./..., 界面 ok"
	press := func(s state.State, col int, when time.Time) (state.State, []state.Effect) {
		return apply(s, state.MousePress{At: at(keys[2], 0, col), Text: text, When: when}, state.MouseRelease{})
	}
	s, _ = press(s, 9, t0)
	assert.Nil(t, s.Selection, "one click")
	s, effects := press(s, 9, t0.Add(200*time.Millisecond))
	assert.Equal(t, []int{7, 11}, cols(s, keys[2], 0), "test")
	assert.Equal(t, []state.Effect{state.EffCopySelection{}}, effects)
	s, _ = press(s, 9, t0.Add(400*time.Millisecond))
	assert.Equal(t, []int{0, 99}, cols(s, keys[2], 0), "the line")
	s, _ = press(s, 9, t0.Add(600*time.Millisecond))
	assert.Nil(t, s.Selection, "a fourth click is a first again")

	s, _ = press(s, 12, t0.Add(2*time.Second))
	s, _ = press(s, 12, t0.Add(2100*time.Millisecond))
	assert.Equal(t, []int{12, 18}, cols(s, keys[2], 0), "./..., with its punctuation")
	s, _ = press(s, 21, t0.Add(3*time.Second))
	s, _ = press(s, 21, t0.Add(3100*time.Millisecond))
	assert.Equal(t, []int{19, 23}, cols(s, keys[2], 0), "wide characters by their cells, from the second cell of one")
	s, _ = press(s, 5, t0.Add(4*time.Second))
	s, _ = press(s, 5, t0.Add(5*time.Second))
	assert.Nil(t, s.Selection, "too slow for a double click")
}

// TestSelection_Clears: esc clears it and does nothing else, and so does a
// click outside the transcript; typing, sending, and switching the view
// clear it too.
func TestSelection_Clears(t *testing.T) {
	s, keys := selectable(t)
	s.Busy = true
	selected, _ := apply(s, state.MousePress{At: at(keys[0], 0, 0), When: t0}, state.MouseDrag{At: at(keys[0], 1, 3)}, state.MouseRelease{})
	require.NotNil(t, selected.Selection)

	for name, ev := range map[string]any{
		"esc": state.Esc{Empty: true}, "a click elsewhere": state.ClearSelection{}, "typing": state.DraftChanged{Draft: "x"},
		"sending": state.Submit{Text: "go"}, "details": state.ToggleDetails{},
	} {
		got, _ := apply(selected, ev)
		assert.Nil(t, got.Selection, name)
	}
	got, effects := apply(selected, state.Esc{Empty: true}, state.Esc{Empty: true})
	assert.Empty(t, effects, "the first esc only clears the selection")
	assert.Equal(t, "press esc again to interrupt", got.Status, "the second arms the interrupt as usual")
}

// TestSelection_FollowsTheText: positions name items and their lines, so
// the selection stays on its text while the transcript scrolls and new
// output streams in below, and goes when its items do.
func TestSelection_FollowsTheText(t *testing.T) {
	s, keys := selectable(t)
	s, _ = apply(s, state.MousePress{At: at(keys[1], 0, 0), When: t0}, state.MouseDrag{At: at(keys[1], 2, 2)})
	want := [][]int{cols(s, keys[1], 0), cols(s, keys[1], 1), cols(s, keys[1], 2)}

	s, _ = apply(s,
		state.ScrollBy{Lines: 5},
		core.TurnStarted{At: t0, Turn: 2},
		engine.TextDelta{At: t0, ItemID: "a", Text: "streaming "},
		engine.TextDelta{At: t0, ItemID: "a", Text: "more"},
		session.Notice{At: t0, Level: session.LevelInfo, Message: "note"},
		state.MouseDrag{At: at(keys[1], 2, 2)},
	)
	assert.Equal(t, want, [][]int{cols(s, keys[1], 0), cols(s, keys[1], 1), cols(s, keys[1], 2)})
	assert.True(t, s.Selection.Dragging, "the drag goes on")

	s, _ = apply(s, state.MouseRelease{}, state.HistoryLoaded{SessionID: "sess-1"})
	_, _, ok := s.SelectedRange()
	assert.False(t, ok, "its items are gone")
}

// TestSelection_CopiedToast: a toast says how many lines were copied, for
// two seconds, and leaves the status line and the working line alone.
func TestSelection_CopiedToast(t *testing.T) {
	s, _ := apply(opened(), // a run, and a status line
		session.InputQueued{At: t0, Input: core.UserInput{ID: "m1", Text: "hi"}},
		core.RunStarted{At: t0, RunID: "r1"},
		state.Esc{},
	)
	status, live := s.Status, s.Live
	require.NotEmpty(t, status)

	s, _ = apply(s, state.Copied{Lines: 3, At: t0})
	require.NotNil(t, s.Toast)
	assert.Equal(t, "copied 3 lines", s.Toast.Text)
	assert.Equal(t, status, s.Status, "the status line is not the copy's")
	assert.Same(t, live, s.Live)
	s, _ = apply(s, state.Tick{Now: t0.Add(time.Second)})
	require.NotNil(t, s.Toast)
	s, _ = apply(s, state.Tick{Now: t0.Add(2 * time.Second)})
	assert.Nil(t, s.Toast, "gone after two seconds")
	s, _ = apply(s, state.Copied{Lines: 1, At: t0.Add(2 * time.Second)})
	assert.Equal(t, "copied 1 line", s.Toast.Text)
}
