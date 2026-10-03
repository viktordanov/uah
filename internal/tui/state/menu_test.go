package state_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/tui/state"
)

func labels(items []state.Suggestion) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.Label)
	}

	return out
}

func TestMenu_Commands(t *testing.T) {
	s := opened()
	assert.Equal(t, []string{"/resume [id]", "/rewind", "/review [target]", "/reasoning"}, labels(s.Suggestions("/re")))
	assert.Equal(t, []string{"low", "medium"}, labels(s.Suggestions("/effort ")[:2]))
	assert.Equal(t, []string{"medium", "max"}, labels(s.Suggestions("/effort m")))
	assert.Empty(t, s.Suggestions("hello"), "plain text has no menu")
	assert.Empty(t, s.Suggestions("/effort low"), "a complete value has no menu")
}

func TestMenu_MoveAcceptClose(t *testing.T) {
	s := opened()
	s, _ = apply(s, state.MenuMove{Draft: "/re", Delta: 1})
	assert.Equal(t, 1, s.Menu.Index)
	s, effects := apply(s, state.MenuAccept{Draft: "/re"})
	assert.Equal(t, []state.Effect{state.EffSetDraft{Text: "/rewind"}}, effects)
	assert.Equal(t, 0, s.Menu.Index)

	s, _ = apply(s, state.MenuMove{Draft: "/re", Delta: -1})
	assert.Equal(t, 3, s.Menu.Index, "moving up from the top wraps")
	s, _ = apply(s, state.MenuClose{Draft: "/re"})
	assert.False(t, s.MenuOpen("/re"))
	assert.True(t, s.MenuOpen("/res"), "a new draft opens it again")
}

func TestMenu_Mentions(t *testing.T) {
	s := opened()
	s, effects := apply(s, state.DraftChanged{Draft: "look at @"})
	require.Equal(t, []state.Effect{state.EffLoadFiles{}, state.EffLoadMCPResources{}}, effects, "the first @ loads the file list and the MCP resources")
	_, effects = apply(s, state.DraftChanged{Draft: "look at @x"})
	assert.Empty(t, effects, "only once")

	s, _ = apply(s, state.FilesLoaded{Paths: []string{"README.md", "internal/session/session.go", "cmd/uah/main.go"}})
	items := s.Suggestions("look at @sesgo")
	require.NotEmpty(t, items)
	assert.Equal(t, "internal/session/session.go", items[0].Label)
	assert.Equal(t, "look at internal/session/session.go ", items[0].Draft)
}

func TestMenu_EnterRunsOrFills(t *testing.T) {
	s, effects := apply(opened(), state.MenuEnter{Draft: "/deta"})
	assert.Equal(t, []state.Effect{state.EffSetDraft{Text: ""}}, effects)
	assert.True(t, s.Details, "a command without arguments runs")

	_, effects = apply(opened(), state.MenuEnter{Draft: "/eff"})
	assert.Equal(t, []state.Effect{state.EffSetDraft{Text: "/effort "}}, effects, "one with arguments is filled in")
}

// TestMenu_Adaptive: /adaptive lists its values with what each does at the
// session's effort (high), marks the current one, and finds a value from a
// digit, so the steps need no remembering.
func TestMenu_Adaptive(t *testing.T) {
	s := opened()
	items := s.Suggestions("/adaptive ")
	require.Equal(t, []string{"off", "1-step", "2-steps"}, labels(items))
	assert.Equal(t, "full effort on every turn (now)", items[0].Help)
	assert.Equal(t, "follow-ups after tool results at medium", items[1].Help)
	assert.Equal(t, "follow-ups after tool results at low", items[2].Help)
	assert.Equal(t, "/adaptive 2-steps", items[2].Draft)
	assert.Equal(t, []string{"2-steps"}, labels(s.Suggestions("/adaptive 2")))
	assert.Equal(t, []string{"1-step"}, labels(s.Suggestions("/adaptive one")))
	assert.Empty(t, s.Suggestions("/adaptive 1-step"), "a complete value has no menu")
}

// TestCommand_AdaptiveShortForms: "/adaptive two" and "/adaptive 1" set the
// steps; an unknown value says how to set it.
func TestCommand_AdaptiveShortForms(t *testing.T) {
	for in, want := range map[string]string{"/adaptive two": "2-steps", "/adaptive 1": "1-step", "/adaptive 0": "off", "/adaptive 2-steps": "2-steps"} {
		_, effects := state.Reduce(opened(), state.Submit{Text: in})
		require.Len(t, effects, 1, in)
		set, ok := effects[0].(state.EffSetSettings)
		require.True(t, ok, in)
		assert.Equal(t, want, set.Settings.AdaptiveEffort, in)
	}
	next, effects := state.Reduce(opened(), state.Submit{Text: "/adaptive lots"})
	assert.Empty(t, effects)
	assert.Contains(t, next.Items[len(next.Items)-1].Text, "off|1-step|2-steps")
}
