package state_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/tui/state"
)

func TestReduce_Advisor(t *testing.T) {
	s, effects := apply(opened(), state.Submit{Text: "/advisor gpt-6.1-sol xhigh"})
	assert.Equal(t, []state.Effect{state.EffAdvisor{Arg: "gpt-6.1-sol xhigh"}}, effects)

	s, _ = apply(s, state.AdvisorShown{Status: "advisor: gpt-6.1-sol at xhigh", OK: true})
	assert.Equal(t, "advisor: gpt-6.1-sol at xhigh", s.Items[len(s.Items)-1].Text)
	s, _ = apply(s, state.AdvisorShown{Err: errors.New("the tool policy does not allow the advisor")})
	assert.Equal(t, "/advisor: the tool policy does not allow the advisor", s.Items[len(s.Items)-1].Text)
	s, _ = apply(s, state.AdvisorShown{})
	assert.Contains(t, s.Items[len(s.Items)-1].Text, "needs the embedded engine")
}

// TestMenu_AdvisorBare: enter on /advisor in the menu shows the advisor,
// as /advisor alone does; tab still adds the space for an argument.
func TestMenu_AdvisorBare(t *testing.T) {
	for _, draft := range []string{"/advisor", "/advi"} {
		_, effects := apply(opened(), state.MenuEnter{Draft: draft})
		assert.Equal(t, []state.Effect{state.EffSetDraft{Text: ""}, state.EffAdvisor{}}, effects, draft)
	}

	_, effects := apply(opened(), state.MenuAccept{Draft: "/advi"})
	assert.Equal(t, []state.Effect{state.EffSetDraft{Text: "/advisor "}}, effects)
}

// TestToolOutput_Advisor: the advice lands on its call whole.
func TestToolOutput_Advisor(t *testing.T) {
	s := fold(inWorkspace(),
		called("a", engine.AdvisorToolName, `{}`),
		engine.ToolOutput{At: t0, CallID: "a", Result: "1. Check \x1b[31mit\x1b[0m.\r\n2. Ship\x07 it.\n", Size: 24},
	)
	assert.Equal(t, "1. Check it.\n2. Ship it.", item(t, s, "call:a").Result, "no escape sequences or control characters")

	read := "The advisor is unavailable (the command uah failed with exit status 2: unknown key); go on without it.\n" +
		"config: line 3: unknown key \"advisor\"\nunknown key"
	s = fold(inWorkspace(),
		called("b", engine.AdvisorToolName, `{}`),
		engine.ToolOutput{At: t0, CallID: "b", Error: "the command uah failed with exit status 2:\a unknown\b key", Result: read},
	)
	assert.Equal(t, "the command uah failed with exit status 2: unknown key", item(t, s, "call:b").ErrorLine, "a call that got no advice shows why, with no control characters")
	assert.Equal(t, read, item(t, s, "call:b").Result, "the stderr, for the detailed view")
}
