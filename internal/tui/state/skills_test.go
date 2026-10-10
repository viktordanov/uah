package state_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/tui/state"
)

var menuSkills = []engine.Skill{
	{Name: "release", DisplayName: "Release", Description: "Cut a release"},
	{Name: "go-testing", DisplayName: "Go Testing", Description: "Write Go tests"},
	{Name: "testing-web", DisplayName: "Testing Web", Description: "Test a web page"},
	{Name: "imagegen", DisplayName: "Image Gen", Description: "Make images"},
}

// TestMenu_Skills: a "$" word loads the skills and completes their names,
// shown by their display names, matched on either name, the ones that
// start with the text first, then the ones a few typos away, each with its
// description;
// enter puts "$name " in the composer and sends nothing; a word no name
// holds ($HOME) or a complete name has no menu.
func TestMenu_Skills(t *testing.T) {
	s, effects := apply(opened(), state.DraftChanged{Draft: "fix it with $"})
	assert.Contains(t, effects, state.Effect(state.EffLoadSkills{}))
	s, _ = apply(s, state.SkillsLoaded{Skills: menuSkills})

	items := s.Suggestions("fix it with $")
	require.Equal(t, []string{"Release", "Go Testing", "Testing Web", "Image Gen"}, labels(items))
	assert.Equal(t, "Cut a release", items[0].Help)
	assert.Equal(t, []string{"Testing Web", "Go Testing"}, labels(s.Suggestions("fix it with $test")), "a name that starts with it first")
	assert.Equal(t, []string{"Release"}, labels(s.Suggestions("$REL")), "case does not matter")
	assert.Equal(t, []string{"Image Gen"}, labels(s.Suggestions("$imageg")), "the name matches")
	gen := s.Suggestions("$gen")
	require.Equal(t, []string{"Image Gen"}, labels(gen), "and the display name")
	assert.Equal(t, "$imagegen ", gen[0].Draft, "the mention is the name")
	assert.Empty(t, s.Suggestions("echo $HOME"))
	assert.Equal(t, []string{"Image Gen"}, labels(s.Suggestions("$imgaegen")), "a typo or two still matches")
	assert.Equal(t, []string{"Go Testing", "Testing Web"}, labels(s.Suggestions("$gotest")), "one edit, then two")
	assert.Equal(t, []string{"Go Testing", "Testing Web"}, labels(s.Suggestions("$tezt")), "any word of the name")
	assert.Empty(t, s.Suggestions("$rx"), "a short word must match as it is")
	assert.Empty(t, s.Suggestions("use $release"), "a complete name has no menu")
	assert.Empty(t, s.Suggestions("use $release now"), "only the last word")

	_, effects = apply(s, state.MenuEnter{Draft: "fix it with $rel"})
	assert.Equal(t, []state.Effect{state.EffSetDraft{Text: "fix it with $release "}}, effects, "enter completes, it does not send")

	_, effects = apply(s, state.DraftChanged{Draft: "fix it with $r"})
	assert.NotContains(t, effects, state.Effect(state.EffLoadSkills{}), "loaded once per $ word")
	_, effects = apply(s, state.DraftChanged{Draft: "then $"})
	assert.Contains(t, effects, state.Effect(state.EffLoadSkills{}), "a new $ word loads them again")
}

// TestMenu_SkillsOff: [tui] skill_mentions = false shows no menu and loads
// nothing.
func TestMenu_SkillsOff(t *testing.T) {
	s := opened()
	s.NoSkillMentions = true
	s, effects := apply(s, state.DraftChanged{Draft: "$"})
	assert.NotContains(t, effects, state.Effect(state.EffLoadSkills{}))
	s, _ = apply(s, state.SkillsLoaded{Skills: menuSkills})
	assert.Empty(t, s.Suggestions("$rel"))
}
