package state_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/internal/tui/state"
)

// press maps a send key to its intent and reduces it, as the shell does;
// a false ok means the key sent nothing.
func press(s state.State, key, draft string) (state.State, []state.Effect, bool) {
	intent := s.SendIntent(key, draft)
	if intent == nil {
		return s, nil, false
	}
	s, effects := apply(s, intent)

	return s, effects, true
}

// working is a session whose agent works on "start", with one message
// queued behind it.
func working(t *testing.T) state.State {
	t.Helper()
	s, _ := apply(opened(),
		session.InputQueued{Input: core.UserInput{ID: "a", Text: "start"}},
		session.InputSent{IDs: []string{"a"}},
		core.RunStarted{RunID: "r"},
		session.InputQueued{Input: core.UserInput{ID: "b", Text: "queued"}},
	)
	require.True(t, s.Working())

	return s
}

var sendKeys = []string{state.KeyEnter, state.KeyCtrlEnter}

// TestSendKeys_WhileWorking: the three ways to send while the agent works.
// ctrl+enter gives the agent the message now, enter once no model response or tool call is
// under way, and tab at the end of the run.
func TestSendKeys_WhileWorking(t *testing.T) {
	busy := working(t)
	for _, tc := range []struct {
		key  string
		want state.Effect
	}{
		{state.KeyCtrlEnter, state.EffSteer{Text: "msg", When: session.SendNow}},
		{state.KeyEnter, state.EffSteer{Text: "msg", When: session.SendAfterTool}},
		{state.KeyTab, state.EffSubmit{Text: "msg"}},
	} {
		_, effects, _ := press(busy, tc.key, "msg")
		assert.Equal(t, []state.Effect{tc.want}, effects, tc.key)
	}
	for _, key := range sendKeys {
		_, effects, _ := press(busy, key, "  ")
		assert.Equal(t, []state.Effect{state.EffSteerQueued{}}, effects, "%s on an empty composer sends the queue now", key)
	}
	_, _, sent := press(busy, state.KeyTab, " ")
	assert.False(t, sent, "tab on an empty composer is the composer's")

	kept, _ := apply(busy, session.Idle{})
	_, effects, _ := press(kept, state.KeyEnter, "")
	assert.Equal(t, []state.Effect{state.EffSteerQueued{}}, effects, "a queue an interrupt kept goes too")
}

func TestSendKeys_Idle(t *testing.T) {
	idle := opened()
	for _, key := range append(sendKeys, state.KeyTab) {
		_, effects, _ := press(idle, key, "hi")
		assert.Equal(t, []state.Effect{state.EffSubmit{Text: "hi"}}, effects, "idle, %s sends", key)
	}
	_, effects, _ := press(idle, state.KeyEnter, "")
	assert.Empty(t, effects, "nothing queued, nothing sent")
	_, byTab, _ := press(idle, state.KeyTab, "/diff")
	_, byEnter, _ := press(idle, state.KeyEnter, "/diff")
	assert.Equal(t, byEnter, byTab, "a command runs, as with enter")
}

func TestSendKeys_ShellMode(t *testing.T) {
	for _, s := range []state.State{opened(), working(t)} {
		s, _ = apply(s, state.EnterShell{})
		_, _, sent := press(s, state.KeyTab, "ls")
		assert.False(t, sent, "tab does not run a command, as in Codex")
		_, effects, _ := press(s, state.KeyEnter, "ls")
		require.Len(t, effects, 1)
		assert.IsType(t, state.EffShell{}, effects[0])
	}
}

// TestSendKeys_TheTerminalPicksTheNewlineHint: the keyboard enhancement
// answer only names the new-line key, as in Codex.
func TestSendKeys_TheTerminalPicksTheNewlineHint(t *testing.T) {
	s := state.New(t0)
	assert.Equal(t, "ctrl+j", s.Keys.NewlineKey(), "until the terminal answers: ctrl+j works everywhere")
	s, _ = apply(s, state.KeyboardReported{Disambiguates: true})
	assert.Equal(t, "shift+enter", s.Keys.NewlineKey())

	assert.Equal(t, "enter after tool · ^enter now · tab later", s.Keys.SendHint(true))
	assert.Equal(t, "enter after tool · tab later", state.Keys{}.SendHint(true), "ctrl+enter may arrive as enter")
	assert.Equal(t, "enter send · shift+enter new line", s.Keys.SendHint(false))
	assert.Equal(t, "enter send · ctrl+j new line", state.Keys{}.SendHint(false))

	plain, _ := apply(opened(), state.Submit{Text: "/help"})
	assert.Contains(t, plain.Items[len(plain.Items)-1].Text, "tab after the run")
	assert.Contains(t, plain.Items[len(plain.Items)-1].Text, "shift+enter arrives as enter in this terminal")
	enhanced, _ := apply(opened(), state.KeyboardReported{Disambiguates: true}, state.Submit{Text: "/help"})
	assert.Contains(t, enhanced.Items[len(enhanced.Items)-1].Text, "shift+enter, alt+enter, or ctrl+j new line")
}

// TestSendKeys_AgentView: the view's composer talks to the viewed agent, so
// enter sends into that agent's live run, and the view keeps the terminal's
// answer, also one that arrives while it is open.
func TestSendKeys_AgentView(t *testing.T) {
	s, _ := apply(opened(), state.AgentViewOpened{ID: "subagent-1", Nickname: "Ada"})
	assert.False(t, s.Working(), "the viewed agent is idle")
	_, effects, _ := press(s, state.KeyEnter, "hi")
	assert.Equal(t, []state.Effect{state.EffAgentSend{ID: "subagent-1", Text: "hi", When: session.SendAfterRun}}, effects)

	s, _ = apply(s, state.AgentEvents{ID: "subagent-1", Events: []core.Event{core.RunStarted{RunID: "r"}}})
	require.True(t, s.Working())
	_, effects, _ = press(s, state.KeyCtrlEnter, "faster")
	assert.Equal(t, []state.Effect{state.EffAgentSend{ID: "subagent-1", Text: "faster", When: session.SendNow}}, effects, "ctrl+enter steers the working agent now")
	_, effects, _ = press(s, state.KeyEnter, "next")
	assert.Equal(t, []state.Effect{state.EffAgentSend{ID: "subagent-1", Text: "next", When: session.SendAfterTool}}, effects, "enter after its tool call")
	_, effects, _ = press(s, state.KeyTab, "later")
	assert.Equal(t, []state.Effect{state.EffAgentSend{ID: "subagent-1", Text: "later", When: session.SendAfterRun}}, effects, "tab gives it the message for after the run")

	s, _ = apply(s, state.KeyboardReported{Disambiguates: true})
	assert.Equal(t, s.Keys, s.View.St.Keys)
}
