package state_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/approval"
	"github.com/viktordanov/uah/internal/models"
	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/internal/tui/state"
)

func configValues() map[string]state.ConfigValue {
	return map[string]state.ConfigValue{
		"auto_compact_percent":           {Value: "90", Source: "default"},
		"model_auto_compact_token_limit": {Value: "0", Source: "default"},
		"compact_model":                  {Value: "gpt-6-sol", Source: "default"},
		"model":                          {Value: "gpt-6-sol", Source: "user file"},
		"effort":                         {Value: "high", Source: "default"},
		"fast":                           {Value: "false", Source: "default"},
		"permission_mode":                {Value: "workspace", Source: "default"},
		"web_search":                     {Value: "live", Source: "default"},
		"adaptive_effort":                {Value: "off", Source: "default"},
		"tui.details":                    {Value: "false", Source: "default"},
		"tui.mouse":                      {Value: "false", Source: "user file"},
		"tui.file_links":                 {Value: "peek", Source: "default"},
		"agents.max_concurrent_threads_per_session": {Value: "6", Source: "default"},
	}
}

// openConfig opens /config with the values and the model list loaded, on
// the row with label.
func openConfig(t *testing.T, s state.State, label string) state.State {
	t.Helper()
	s, _ = apply(s, state.Submit{Text: "/config"},
		state.ConfigLoaded{Path: "/home/me/.config/uagent/config.toml", Values: configValues()},
		state.ModelsLoaded{Catalog: codexCatalog(models.OriginLive)})
	for _, row := range s.ConfigRows() {
		if row.Label == label {
			return s
		}
		s, _ = apply(s, state.ConfigMove{Delta: 1})
	}
	t.Fatalf("no row %q", label)

	return s
}

func TestConfig_OpensAndShowsValuesWithSources(t *testing.T) {
	s, effects := apply(opened(), state.Submit{Text: "/config"})
	assert.Equal(t, []state.Effect{state.EffLoadConfig{}, state.EffLoadModels{Provider: "openai-codex"}}, effects)
	require.NotNil(t, s.Config)

	s, _ = apply(s, state.ConfigLoaded{Path: "/cfg.toml", Values: configValues()})
	rows := s.ConfigRows()
	require.Len(t, rows, 13)
	got := map[string][2]string{}
	for _, r := range rows {
		got[r.Label] = [2]string{r.Value, r.Source}
	}
	assert.Equal(t, [2]string{"on at 90%", "default"}, got["Auto-compact"])
	assert.Equal(t, [2]string{"none", "default"}, got["Auto-compact token limit"])
	assert.Equal(t, [2]string{"session model (gpt-6-sol)", "default"}, got["Compaction model"])
	assert.Equal(t, [2]string{"gpt-6-sol", "user file"}, got["Model"])
	assert.Equal(t, [2]string{"off", "user file"}, got["Mouse"])
	assert.Equal(t, [2]string{"live", "default"}, got["Web search"])
	assert.Equal(t, [2]string{"off", "default"}, got["Adaptive effort"], "off is shown as is")
	assert.Equal(t, [2]string{"peek", "default"}, got["File links"])
	assert.Equal(t, [2]string{"6", "default"}, got["Open subagents"])

	s, _ = apply(s, state.ConfigEsc{})
	assert.Nil(t, s.Config, "esc closes")
}

func TestConfig_TogglesAndAppliesLive(t *testing.T) {
	s := openConfig(t, opened(), "Mouse")
	s, effects := apply(s, state.ConfigChange{Delta: 1})
	assert.Equal(t, []state.Effect{state.EffSaveConfig{Key: "tui.mouse", Value: true}}, effects)
	assert.True(t, s.Mouse, "the TUI reports the mouse at once")
	mouse := configRow(s, "Mouse")
	assert.Equal(t, "on", mouse.Value)
	assert.Equal(t, state.SourceUser, mouse.Source)

	s, effects = apply(s, state.ConfigSaved{Key: "tui.mouse", Value: true})
	assert.Equal(t, []state.Effect{state.EffLoadConfig{}}, effects, "reload the sources")
	assert.Contains(t, s.Items[len(s.Items)-1].Text, "saved tui.mouse = true to /home/me/.config/uagent/config.toml; applies now")

	s = openConfig(t, opened(), "Details view")
	s, _ = apply(s, state.ConfigEnter{})
	assert.True(t, s.Details)
}

// TestConfig_FileLinks: the row cycles peek, editor, open, and off, applies
// at once, and turning links on looks up the words of the messages shown.
func TestConfig_FileLinks(t *testing.T) {
	s := opened()
	s.FileLinks = state.LinksOff
	s, _ = apply(s, core.AssistantMessage{Text: "See `main.go`.", Final: true})
	s = openConfig(t, s, "File links")
	s, effects := apply(s, state.ConfigChange{Delta: 1})
	assert.Equal(t, state.EffSaveConfig{Key: "tui.file_links", Value: "editor"}, effects[0])
	assert.Equal(t, state.LinksEditor, s.FileLinks, "applies at once")
	require.Len(t, effects, 2, "links on: the shown messages are looked up")
	assert.Equal(t, []string{"main.go"}, effects[1].(state.EffResolveLinks).Messages[0].Words)

	for _, want := range []string{"open", "off", "peek"} {
		s, effects = apply(s, state.ConfigChange{Delta: 1})
		assert.Equal(t, state.EffSaveConfig{Key: "tui.file_links", Value: want}, effects[0])
		assert.Equal(t, want, s.FileLinks)
	}
	s, _ = apply(s, state.ConfigSaved{Key: "tui.file_links", Value: "peek"})
	assert.Contains(t, s.Items[len(s.Items)-1].Text, "saved tui.file_links = peek to /home/me/.config/uagent/config.toml; applies now")
}

// TestConfig_WebSearch: the row cycles web_search's values and applies to
// the sessions opened next.
func TestConfig_WebSearch(t *testing.T) {
	s := openConfig(t, opened(), "Web search")
	s, effects := apply(s, state.ConfigChange{Delta: 1})
	assert.Equal(t, []state.Effect{state.EffSaveConfig{Key: "web_search", Value: "disabled"}}, effects, "no session change")
	s, _ = apply(s, state.ConfigSaved{Key: "web_search", Value: "disabled"})
	assert.Contains(t, s.Items[len(s.Items)-1].Text, "applies to sessions opened from now on")
}

// TestConfig_AdaptiveEffort: the row cycles off, 1 step, and 2 steps, says
// what adaptive effort does while it is selected, and changes the current
// session too.
func TestConfig_AdaptiveEffort(t *testing.T) {
	s := openConfig(t, opened(), "Adaptive effort")
	row := func(s state.State) state.ConfigRow {
		for _, r := range s.ConfigRows() {
			if r.Key == "adaptive_effort" {
				return r
			}
		}
		t.Fatal("no adaptive_effort row")

		return state.ConfigRow{}
	}
	with := func(value string) session.Settings {
		next := settings()
		next.AdaptiveEffort = value

		return next
	}
	assert.Equal(t, "off", row(s).Value)
	assert.Contains(t, row(s).Help, "Adaptive effort: think one or two effort levels less on follow-up turns")
	s, effects := apply(s, state.ConfigChange{Delta: 1})
	assert.Equal(t, []state.Effect{state.EffSaveConfig{Key: "adaptive_effort", Value: "1-step"}, state.EffSetSettings{Settings: with("1-step")}}, effects)
	assert.Equal(t, "1 step", row(s).Value)
	s, _ = apply(s, state.ConfigSaved{Key: "adaptive_effort", Value: "1-step"})
	assert.Contains(t, s.Items[len(s.Items)-1].Text, "saved adaptive_effort = 1-step")
	assert.Contains(t, s.Items[len(s.Items)-1].Text, "this session changes too")
	s, _ = apply(s, session.SettingsChanged{At: t0, Settings: with("1-step"), Applied: session.AppliedLive})
	assert.Contains(t, s.Items[len(s.Items)-1].Text, "adaptive effort: 1 step (follow-ups at medium), applies now")
	s, effects = apply(s, state.ConfigChange{Delta: 1})
	assert.Equal(t, []state.Effect{state.EffSaveConfig{Key: "adaptive_effort", Value: "2-steps"}, state.EffSetSettings{Settings: with("2-steps")}}, effects)
	assert.Equal(t, "2 steps", row(s).Value)
	s, _ = apply(s, session.SettingsChanged{At: t0, Settings: with("2-steps")})
	_, effects = apply(s, state.ConfigChange{Delta: 1})
	assert.Equal(t, []state.Effect{state.EffSaveConfig{Key: "adaptive_effort", Value: "off"}, state.EffSetSettings{Settings: with("off")}}, effects, "and off again")
}

// TestCycleAdaptive: alt+e steps adaptive effort for this session only,
// through the session, which saves it in the sidecar; the user file is
// not written.
func TestCycleAdaptive(t *testing.T) {
	next := settings()
	next.AdaptiveEffort = "1-step"
	_, effects := apply(opened(), state.CycleAdaptive{})
	assert.Equal(t, []state.Effect{state.EffSetSettings{Settings: next}}, effects)
	_, effects = apply(state.New(t0), state.CycleAdaptive{})
	assert.Empty(t, effects, "no session yet")
}

// TestAdaptiveCommand: /adaptive steps to the next value, takes one by
// name, and shows the current one for anything else; /status shows it.
func TestAdaptiveCommand(t *testing.T) {
	with := func(value string) session.Settings {
		next := settings()
		next.AdaptiveEffort = value

		return next
	}
	s, effects := apply(opened(), state.Submit{Text: "/adaptive"})
	assert.Equal(t, []state.Effect{state.EffSetSettings{Settings: with("1-step")}}, effects)
	_, effects = apply(s, state.Submit{Text: "/adaptive 2-steps"})
	assert.Equal(t, []state.Effect{state.EffSetSettings{Settings: with("2-steps")}}, effects)
	s, effects = apply(s, state.Submit{Text: "/adaptive max"})
	assert.Empty(t, effects)
	assert.Contains(t, s.Items[len(s.Items)-1].Text, "adaptive effort: off (set it with /adaptive off|1-step|2-steps)")
	s, _ = apply(s, session.SettingsChanged{At: t0, Settings: with("2-steps")})
	s, _ = apply(s, state.Submit{Text: "/status"})
	assert.Contains(t, s.Items[len(s.Items)-3].Text, "effort high · adaptive effort 2-steps ·")
}

func TestConfig_ModelEffortAndFastChangeTheSession(t *testing.T) {
	s := openConfig(t, opened(), "Effort")
	_, effects := apply(s, state.ConfigChange{Delta: 1})
	next := settings()
	next.Effort = "xhigh"
	assert.Equal(t, []state.Effect{state.EffSaveConfig{Key: "effort", Value: "xhigh"}, state.EffSetSettings{Settings: next}}, effects)

	s = openConfig(t, opened(), "Model")
	_, effects = apply(s, state.ConfigChange{Delta: 1})
	next = settings()
	next.Model = "gpt-6-luna"
	assert.Equal(t, []state.Effect{state.EffSaveConfig{Key: "model", Value: "gpt-6-luna"}, state.EffSetSettings{Settings: next}}, effects, "the next model in the provider's list")

	s = openConfig(t, opened(), "Fast mode")
	_, effects = apply(s, state.ConfigChange{Delta: 1})
	assert.Equal(t, []state.Effect{state.EffSaveConfig{Key: "fast", Value: true}}, effects, "a provider without priority processing: saved only")
	s = opened()
	s.Priority = true
	s = openConfig(t, s, "Fast mode")
	_, effects = apply(s, state.ConfigChange{Delta: 1})
	next = settings()
	next.ServiceTier = "priority"
	assert.Equal(t, []state.Effect{state.EffSaveConfig{Key: "fast", Value: true}, state.EffSetSettings{Settings: next}}, effects)
}

func TestConfig_CyclesThePermissionModeAsShiftTab(t *testing.T) {
	s := openConfig(t, opened(), "Permission mode")
	s, effects := apply(s, state.ConfigChange{Delta: 1})
	assert.Equal(t, []state.Effect{
		state.EffSaveConfig{Key: "permission_mode", Value: "auto"},
		state.EffSetSettings{Settings: settings().WithMode(approval.ModeAuto)},
	}, effects)
	_, effects = apply(s, state.ConfigChange{Delta: 1})
	assert.Equal(t, state.EffSaveConfig{Key: "permission_mode", Value: "read-only"}, effects[0], "yolo is not in the cycle")
}

func TestConfig_CyclesAutoCompactAndTheCompactionModel(t *testing.T) {
	s := openConfig(t, opened(), "Auto-compact")
	s, effects := apply(s, state.ConfigChange{Delta: 1})
	assert.Equal(t, []state.Effect{state.EffSaveConfig{Key: "auto_compact_percent", Value: 95}}, effects)
	assert.Equal(t, "on at 95%", s.ConfigRows()[0].Value, "shown before the save returns")
	s, effects = apply(s, state.ConfigChange{Delta: 1})
	assert.Equal(t, []state.Effect{state.EffSaveConfig{Key: "auto_compact_percent", Value: 0}}, effects, "after 95%, off")
	s, effects = apply(s, state.ConfigChange{Delta: -1})
	assert.Equal(t, []state.Effect{state.EffSaveConfig{Key: "auto_compact_percent", Value: 95}}, effects, "← goes back")

	s = openConfig(t, opened(), "Compaction model")
	s, effects = apply(s, state.ConfigChange{Delta: 1})
	assert.Equal(t, []state.Effect{state.EffSaveConfig{Key: "compact_model", Value: "gpt-6-sol"}}, effects, "from the session's model to the first listed")
	s.Config.Values["compact_model"] = state.ConfigValue{Value: "gpt-5.6-luna", Source: "user file"}
	_, effects = apply(s, state.ConfigChange{Delta: 1})
	assert.Equal(t, []state.Effect{state.EffSaveConfig{Key: "compact_model", Value: nil}}, effects, "after the last model, the session's again: the key is removed")
}

func TestConfig_TypesTheTokenLimit(t *testing.T) {
	s := openConfig(t, opened(), "Auto-compact token limit")
	s, effects := apply(s, state.ConfigEnter{})
	assert.Empty(t, effects)
	require.True(t, s.Config.Editing)
	assert.Empty(t, s.Config.Input, "0 is shown as none, so typing starts empty")

	s, effects = apply(s, state.ConfigType{Text: "5"}, state.ConfigType{Text: "x"}, state.ConfigEnter{})
	assert.Empty(t, effects)
	assert.Contains(t, s.Items[len(s.Items)-1].Text, `wants a number of tokens, not "5x"`)
	s, effects = apply(s, state.ConfigType{Text: "\b"}, state.ConfigType{Text: "0000"}, state.ConfigEnter{})
	assert.Equal(t, []state.Effect{state.EffSaveConfig{Key: "model_auto_compact_token_limit", Value: int64(50000)}}, effects)
	assert.False(t, s.Config.Editing)

	s, _ = apply(s, state.ConfigEnter{}, state.ConfigType{Text: "1"}, state.ConfigEsc{})
	assert.False(t, s.Config.Editing, "esc stops typing")
	assert.NotNil(t, s.Config, "and keeps the panel")
}

func TestConfig_TypesAModelWithoutAList(t *testing.T) {
	s, _ := apply(opened(), state.Submit{Text: "/config"}, state.ConfigLoaded{Path: "/cfg.toml", Values: configValues()})
	for range 3 {
		s, _ = apply(s, state.ConfigMove{Delta: 1})
	}
	s, _ = apply(s, state.ConfigChange{Delta: 1})
	require.True(t, s.Config.Editing)
	assert.Equal(t, "gpt-6-sol", s.Config.Input)
	_, effects := apply(s, state.ConfigType{Text: "\b"}, state.ConfigType{Text: "\b"}, state.ConfigType{Text: "\b"}, state.ConfigType{Text: "luna"}, state.ConfigEnter{})
	next := settings()
	next.Model = "gpt-6-luna"
	assert.Equal(t, []state.Effect{state.EffSaveConfig{Key: "model", Value: "gpt-6-luna"}, state.EffSetSettings{Settings: next}}, effects)
}

func TestConfig_WarnsWhenAnotherSourceWins(t *testing.T) {
	s := openConfig(t, opened(), "Auto-compact")
	s, _ = apply(s, state.ConfigChange{Delta: 1}, state.ConfigSaved{Key: "auto_compact_percent", Value: 95})
	values := configValues()
	values["auto_compact_percent"] = state.ConfigValue{Value: "80", Source: "config.d/x.toml"}
	s, _ = apply(s, state.ConfigLoaded{Path: "/cfg.toml", Values: values})
	assert.Equal(t, session.LevelWarning, s.Items[len(s.Items)-1].Level)
	assert.Contains(t, s.Items[len(s.Items)-1].Text, "auto_compact_percent still comes from config.d/x.toml, which wins over the user file")
	assert.Contains(t, s.Items[len(s.Items)-2].Text, "applies to sessions opened from now on")

	s, _ = apply(s, state.ConfigSaved{Key: "fast", Value: true, Err: assert.AnError})
	assert.Equal(t, session.LevelError, s.Items[len(s.Items)-1].Level)
}

// configRow is the /config row with the label.
func configRow(s state.State, label string) state.ConfigRow {
	for _, r := range s.ConfigRows() {
		if r.Label == label {
			return r
		}
	}

	return state.ConfigRow{}
}

// TestConfig_AgentLimit: the subagent limit is a typed number that
// applies to the session at once; empty removes it for the default.
func TestConfig_AgentLimit(t *testing.T) {
	s := openConfig(t, opened(), "Open subagents")
	s, _ = apply(s, state.ConfigChange{Delta: 1}, state.ConfigType{Text: "\b"}, state.ConfigType{Text: "8"})
	s, effects := apply(s, state.ConfigEnter{})
	assert.Equal(t, []state.Effect{state.EffSaveConfig{Key: "agents.max_concurrent_threads_per_session", Value: int64(8)}}, effects)

	s, effects = apply(s, state.ConfigSaved{Key: "agents.max_concurrent_threads_per_session", Value: int64(8)})
	assert.Contains(t, effects, state.Effect(state.EffAgentLimit{Max: 8}))
	assert.Contains(t, s.Items[len(s.Items)-1].Text, "applies now")

	_, effects = apply(s, state.ConfigSaved{Key: "agents.max_concurrent_threads_per_session"})
	assert.Contains(t, effects, state.Effect(state.EffAgentLimit{}), "removed: the default")
}
