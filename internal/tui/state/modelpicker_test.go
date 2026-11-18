package state_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/models"
	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/internal/tui/state"
)

// pickerCatalog is a list with per-model efforts: gpt-6-sol from low to
// ultra (default low), gpt-6-luna without ultra (default medium), and
// gpt-6-mini with a single level.
func pickerCatalog() models.Catalog {
	return models.Catalog{Provider: "openai-codex", Origin: models.OriginLive, Models: []models.Model{
		{
			ID: "gpt-6-sol", ReasoningLevels: []string{"low", "medium", "high", "xhigh", "max", "ultra"}, DefaultEffort: "low",
			ReasoningHelp: map[string]string{"low": "Fast responses with lighter reasoning"},
		},
		{ID: "gpt-6-luna", ReasoningLevels: []string{"low", "medium", "high"}, DefaultEffort: "medium"},
		{ID: "gpt-6-mini", ReasoningLevels: []string{"medium"}, DefaultEffort: "medium"},
		{ID: "gpt-secret", Hidden: true},
	}}
}

func levels(rows []state.EffortRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Level)
	}

	return out
}

func TestSpeedChecksCatalogAndResetsOnModelChange(t *testing.T) {
	c := pickerCatalog()
	c.Models[0].ServiceTiers = []string{"priority", "ultrafast"}
	s, effects := apply(opened(), state.Submit{Text: "/speed ultrafast"})
	require.Equal(t, []state.Effect{state.EffLoadModels{Provider: c.Provider}}, effects)
	s, effects = apply(s, state.ModelsLoaded{Catalog: c})
	require.Len(t, effects, 1)
	selected := effects[0].(state.EffSetSettings).Settings
	require.Equal(t, "ultrafast", selected.ServiceTier)
	require.Equal(t, s.Settings.Effort, selected.Effort)
	s.Settings = selected
	_, effects = apply(s, state.Submit{Text: "/model gpt-6-luna medium"})
	require.Len(t, effects, 1)
	assert.Empty(t, effects[0].(state.EffSetSettings).Settings.ServiceTier)
}

func modelIDs(list []models.Model) []string {
	out := make([]string, 0, len(list))
	for _, m := range list {
		out = append(out, m.ID)
	}

	return out
}

func withModelEffort(model, effort string) session.Settings {
	s := settings()
	s.Model, s.Effort = model, effort

	return s
}

// TestModelPicker_LoadsThenListsTheModels: /model with no list loads it and
// shows the models once it arrives, the current one selected.
func TestModelPicker_LoadsThenListsTheModels(t *testing.T) {
	s, effects := apply(opened(), state.Submit{Text: "/model"})
	assert.Equal(t, []state.Effect{state.EffLoadModels{Provider: "openai-codex"}}, effects)
	require.NotNil(t, s.ModelPicker)
	assert.True(t, s.ModelPicker.Loading)
	s, _ = apply(s, state.ModelPickEnter{}, state.ModelPickMove{Delta: 1})
	assert.True(t, s.ModelPicker.Loading, "keys wait for the list")

	s, _ = apply(s, state.ModelsLoaded{Catalog: pickerCatalog()})
	assert.False(t, s.ModelPicker.Loading)
	assert.Equal(t, []string{"gpt-6-sol", "gpt-6-luna", "gpt-6-mini"}, modelIDs(s.PickerModels()), "hidden models are not listed")
	assert.Equal(t, 0, s.ModelPicker.Index, "the current model")
}

// TestModelPicker_PickModelThenEffort: enter on a model lists its efforts
// with its default selected; enter on one applies both.
func TestModelPicker_PickModelThenEffort(t *testing.T) {
	s, _ := apply(opened(), state.ModelsLoaded{Catalog: pickerCatalog()}, state.Submit{Text: "/model"})
	s, effects := apply(s, state.ModelPickMove{Delta: 1}, state.ModelPickEnter{})
	assert.Empty(t, effects)
	assert.Equal(t, "gpt-6-luna", s.ModelPicker.Model)
	rows := s.PickerEfforts()
	assert.Equal(t, []string{"low", "medium", "high"}, levels(rows), "the model's levels, not every level")
	assert.True(t, rows[1].Default)
	assert.Equal(t, 1, s.ModelPicker.Index, "the default is preselected on another model")

	s, effects = apply(s, state.ModelPickMove{Delta: 1}, state.ModelPickEnter{})
	assert.Equal(t, []state.Effect{state.EffSetSettings{Settings: withModelEffort("gpt-6-luna", "high")}}, effects)
	assert.Nil(t, s.ModelPicker, "the panel closes")
}

// TestModelPicker_CurrentModelSelectsTheCurrentEffort follows Codex: on the
// model in use, the session's effort is selected, not the default.
func TestModelPicker_CurrentModelSelectsTheCurrentEffort(t *testing.T) {
	s, _ := apply(opened(), state.ModelsLoaded{Catalog: pickerCatalog()}, state.Submit{Text: "/model"}, state.ModelPickEnter{})
	rows := s.PickerEfforts()
	require.Len(t, rows, 6)
	assert.Equal(t, "high", rows[s.ModelPicker.Index].Level)
	assert.True(t, rows[2].Current)
	assert.True(t, rows[0].Default)
	assert.Equal(t, "Fast responses with lighter reasoning", rows[0].Help)

	s, effects := apply(s, state.ModelPickEnter{})
	assert.Empty(t, effects, "nothing changed")
	assert.Contains(t, s.Items[len(s.Items)-1].Text, "unchanged")
}

// TestModelPicker_EscGoesBackThenCloses: esc on the efforts goes back to the
// models with the chosen one selected; esc there closes the panel.
func TestModelPicker_EscGoesBackThenCloses(t *testing.T) {
	s, _ := apply(opened(), state.ModelsLoaded{Catalog: pickerCatalog()}, state.Submit{Text: "/model"},
		state.ModelPickMove{Delta: 1}, state.ModelPickEnter{})
	s, effects := apply(s, state.ModelPickEsc{})
	assert.Empty(t, effects)
	require.NotNil(t, s.ModelPicker)
	assert.Empty(t, s.ModelPicker.Model)
	assert.Equal(t, 1, s.ModelPicker.Index, "gpt-6-luna stays selected")

	s, effects = apply(s, state.ModelPickEsc{})
	assert.Empty(t, effects)
	assert.Nil(t, s.ModelPicker)
	assert.Equal(t, settings(), s.Settings, "nothing applied")
}

// TestModelPicker_MoveWraps moves through the rows and around.
func TestModelPicker_MoveWraps(t *testing.T) {
	s, _ := apply(opened(), state.ModelsLoaded{Catalog: pickerCatalog()}, state.Submit{Text: "/model"}, state.ModelPickMove{Delta: -1})
	assert.Equal(t, 2, s.ModelPicker.Index)
}

// TestModelPicker_SingleLevelAppliesAtOnce: a model with one level needs
// no second step, as in Codex.
func TestModelPicker_SingleLevelAppliesAtOnce(t *testing.T) {
	s, _ := apply(opened(), state.ModelsLoaded{Catalog: pickerCatalog()}, state.Submit{Text: "/model"}, state.ModelPickMove{Delta: 2})
	s, effects := apply(s, state.ModelPickEnter{})
	assert.Equal(t, []state.Effect{state.EffSetSettings{Settings: withModelEffort("gpt-6-mini", "medium")}}, effects)
	assert.Nil(t, s.ModelPicker)
}

// TestModelPicker_TypedModel: /model <id> opens at the model's efforts,
// /model <id> <effort> applies both, and an unknown model or effort is
// refused without the panel.
func TestModelPicker_TypedModel(t *testing.T) {
	loaded := func() state.State {
		s, _ := apply(opened(), state.ModelsLoaded{Catalog: pickerCatalog()})

		return s
	}

	s, effects := apply(loaded(), state.Submit{Text: "/model gpt-6-luna"})
	assert.Empty(t, effects)
	require.NotNil(t, s.ModelPicker)
	assert.Equal(t, "gpt-6-luna", s.ModelPicker.Model)
	assert.Equal(t, "medium", s.PickerEfforts()[s.ModelPicker.Index].Level)

	s, effects = apply(loaded(), state.Submit{Text: "/model gpt-6-luna low"})
	assert.Equal(t, []state.Effect{state.EffSetSettings{Settings: withModelEffort("gpt-6-luna", "low")}}, effects)
	assert.Nil(t, s.ModelPicker)

	s, effects = apply(loaded(), state.Submit{Text: "/model gpt-6-luna ultra"})
	assert.Empty(t, effects)
	assert.Equal(t, "gpt-6-luna does not accept effort ultra (low, medium, high)", s.Items[len(s.Items)-1].Text)

	s, effects = apply(loaded(), state.Submit{Text: "/model gpt-6-lune"})
	assert.Empty(t, effects)
	assert.Nil(t, s.ModelPicker)
	assert.Contains(t, s.Items[len(s.Items)-1].Text, "gpt-6-lune is not available on openai-codex; did you mean gpt-6-luna")
}

// TestModelPicker_NoModels: a provider that lists nothing says so and
// points at /model <id>.
func TestModelPicker_NoModels(t *testing.T) {
	s, _ := apply(opened(), state.Submit{Text: "/model"}, state.ModelsLoaded{Catalog: models.Catalog{Provider: "openai-codex", Origin: models.OriginNone}})
	assert.Nil(t, s.ModelPicker)
	assert.Equal(t, "openai-codex lists no models; use /model <id> [effort]", s.Items[len(s.Items)-1].Text)
}

// TestModelPicker_EnterOnTheMenuOpensIt: enter on /model in the command
// menu opens the picker, while tab still leaves room for an argument.
func TestModelPicker_EnterOnTheMenuOpensIt(t *testing.T) {
	loaded := func() state.State {
		s, _ := apply(opened(), state.ModelsLoaded{Catalog: pickerCatalog()})

		return s
	}
	s, effects := apply(loaded(), state.MenuEnter{Draft: "/mod"})
	assert.Equal(t, []state.Effect{state.EffSetDraft{Text: ""}}, effects)
	require.NotNil(t, s.ModelPicker)

	_, effects = apply(loaded(), state.MenuAccept{Draft: "/mod"})
	assert.Equal(t, []state.Effect{state.EffSetDraft{Text: "/model "}}, effects)
	_, effects = apply(loaded(), state.MenuEnter{Draft: "/rev"})
	assert.Equal(t, state.EffSetDraft{Text: "/review "}, effects[0], "other commands still wait for their argument")
}
