package state_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/internal/tui/state"
)

// found runs the search an effect asks for, as the shell does off the
// update loop.
func found(t *testing.T, effects []state.Effect) state.Found {
	t.Helper()
	for _, e := range effects {
		if f, ok := e.(state.EffFindInTranscript); ok {
			return state.Found{Gen: f.Gen, Keys: state.FindIn(f.Docs, f.Query, nil)}
		}
	}
	t.Fatal("no search started")

	return state.Found{}
}

// TestFind_TypeMoveAndGoBack: / while going back opens the search; the
// query finds your messages and the agent's answers, ignoring case, and
// shows the newest; ↑ and ↓ move; esc goes back to your message at or
// above the match.
func TestFind_TypeMoveAndGoBack(t *testing.T) {
	s := talked(t)
	s, _ = apply(s, state.Esc{Empty: true}, state.Esc{Empty: true}, state.FindOpen{})
	require.NotNil(t, s.Find)
	assert.Nil(t, s.Backtrack, "the search takes over from going back")

	s, effects := apply(s, state.FindType{Text: "ANSWER"})
	assert.Contains(t, s.Status, "/ANSWER · searching…")
	s, _ = apply(s, found(t, effects))
	require.Len(t, s.Find.Keys, 3, "the three answers")
	assert.Equal(t, s.Find.Keys[2], s.Find.Key(), "the newest first")
	assert.Contains(t, s.Status, "3 of 3")

	s, _ = apply(s, state.FindMove{Delta: -1})
	assert.Contains(t, s.Status, "2 of 3")
	s, _ = apply(s, state.FindMove{Delta: -5})
	assert.Contains(t, s.Status, "1 of 3", "the first stays")

	s, _ = apply(s, state.FindEnter{})
	assert.True(t, s.Find.Browsing)
	assert.Contains(t, s.Status, "↑/k earlier · ↓/j later")
	s, _ = apply(s, state.FindMove{Delta: 1}, state.FindEsc{})
	assert.Nil(t, s.Find)
	require.NotNil(t, s.Backtrack, "back to choosing a message")
	it, _ := s.Item(s.Backtrack.Key)
	assert.Contains(t, it.Text, "look", "your message above the second answer")
}

// TestFind_StaleAndEmpty: a search for an older query is dropped, a query
// without matches says so, and ctrl+c leaves everything.
func TestFind_StaleAndEmpty(t *testing.T) {
	s := talked(t)
	s, first := apply(s, state.FindOpen{Query: "thi"})
	s, second := apply(s, state.FindType{Text: "rd"})
	s, _ = apply(s, found(t, first))
	assert.True(t, s.Find.Searching, "the older query's result is dropped")
	s, _ = apply(s, found(t, second))
	assert.Equal(t, []string{s.Find.Key()}, s.Find.Keys, "only your third message")

	s, effects := apply(s, state.FindType{Text: "zzz"})
	s, _ = apply(s, found(t, effects))
	assert.Contains(t, s.Status, "no match")
	assert.Empty(t, s.Find.Key())

	s, _ = apply(s, state.FindCancel{})
	assert.Nil(t, s.Find)
	assert.Nil(t, s.Backtrack)
	assert.Empty(t, s.Status)
}

func TestFindIn(t *testing.T) {
	docs := []state.SearchDoc{{Key: "a", Text: "Straße"}, {Key: "b", Text: "nothing"}, {Key: "c", Text: "STRASSE straße"}}
	assert.Equal(t, []string{"a", "c"}, state.FindIn(docs, "STRAßE", nil))
	assert.Nil(t, state.FindIn(docs, "a", func() bool { return true }), "a stale search stops")
}

// TestFind_Command: /search opens the search with its words as the query,
// also while a run is live; esc then leaves for the bottom, since going
// back needs an idle session.
func TestFind_Command(t *testing.T) {
	s, effects := apply(talked(t), state.Submit{Text: "/search third"})
	require.NotNil(t, s.Find)
	s, _ = apply(s, found(t, effects))
	assert.Contains(t, s.Status, "/third · 1 of 1")

	busy, _ := apply(talked(t), session.InputQueued{At: t0, Input: core.UserInput{ID: "q1", Text: "work"}})
	busy, effects = apply(busy, state.Submit{Text: "/search answer"})
	require.NotNil(t, busy.Find, "allowed while busy")
	busy, _ = apply(busy, found(t, effects), state.FindEsc{})
	assert.Nil(t, busy.Find)
	assert.Nil(t, busy.Backtrack)
}

// TestFind_Stay: FindStay leaves the search and going back with the
// window pinned where it was drawn; a window at the bottom just follows.
func TestFind_Stay(t *testing.T) {
	s := talked(t)
	s, _ = apply(s, state.Esc{Empty: true}, state.Esc{Empty: true}, state.FindOpen{Query: "first"})
	at := state.TextPos{Key: s.Items[1].Key, Line: 2}
	s, _ = apply(s, state.FindStay{At: at, Scroll: 9, Width: 100})
	assert.Nil(t, s.Find)
	assert.Nil(t, s.Backtrack)
	assert.Empty(t, s.Status)
	assert.True(t, s.Pinned())
	assert.Equal(t, at, s.Anchor)
	assert.Equal(t, 9, s.Scroll)

	bottom, _ := apply(talked(t), state.FindOpen{Query: "first"}, state.FindStay{At: at, Width: 100})
	assert.False(t, bottom.Pinned())
	assert.Empty(t, bottom.Anchor.Key)
}
