package state

import (
	"fmt"
	"slices"

	"github.com/viktordanov/uah/internal/approval"
	"github.com/viktordanov/uah/internal/session"
)

// ConfigPanel is the /config panel, after Claude Code's: the basic
// settings of the user file with their values and sources, changed in
// place and saved at once.
type ConfigPanel struct {
	// Path is the user file the panel saves to.
	Path string
	// Values are the effective values by key; nil until loaded.
	Values map[string]ConfigValue
	// Index is the selected row.
	Index int
	// Editing means the selected row takes typed text, kept in Input.
	Editing bool
	Input   string
	// saved is the key of the last save, whose source the next load checks.
	saved string
}

// Effects and events of the /config panel.
type (
	// EffLoadConfig loads the effective configuration.
	EffLoadConfig struct{}
	// ConfigLoaded carries it: the user file and each key's value and
	// source for a session opened now.
	ConfigLoaded struct {
		Path   string
		Values map[string]ConfigValue
		Err    error
	}
	// EffSaveConfig writes one key to the user file; a nil Value removes it.
	EffSaveConfig struct {
		Key   string
		Value any
	}
	// ConfigSaved reports the save.
	ConfigSaved struct {
		Key   string
		Value any
		Err   error
	}
)

// EffAgentLimit sets the session's subagent limit now; 0 is the default.
type EffAgentLimit struct{ Max int }

func (EffLoadConfig) effect() {}
func (EffAgentLimit) effect() {}
func (EffSaveConfig) effect() {}

// Panel intents, from keys while the panel is open.
type (
	// ConfigMove moves the selection.
	ConfigMove struct{ Delta int }
	// ConfigChange is enter or space (+1), or ← and → (-1, +1): toggle,
	// cycle, or start typing the selected row's value.
	ConfigChange struct{ Delta int }
	// ConfigType types into the value being edited; "\b" is backspace.
	ConfigType struct{ Text string }
	// ConfigEnter saves the value being typed, or changes the row.
	ConfigEnter struct{}
	// ConfigEsc stops typing, or closes the panel.
	ConfigEsc struct{}
)

// cmdConfig opens the panel and loads the values and the model list.
func cmdConfig(s *State, _ string) []Effect {
	s.Config = &ConfigPanel{}
	effects := []Effect{EffLoadConfig{}}
	if _, ok := s.catalog(); !ok && !s.Menu.modelsLoading {
		s.Menu.modelsLoading = true
		effects = append(effects, EffLoadModels{Provider: s.Settings.Provider})
	}

	return effects
}

// onConfig handles the panel's intents and events; ok is false for others.
func (s *State) onConfig(ev any) (effects []Effect, ok bool) {
	switch e := ev.(type) {
	case ConfigLoaded:
		s.configLoaded(e)

		return nil, true
	case ConfigSaved:
		return s.configSaved(e), true
	}
	p := s.Config
	if p == nil {
		return nil, false
	}
	switch e := ev.(type) {
	case ConfigMove:
		if !p.Editing {
			n := len(configKeys)
			p.Index = ((p.Index+e.Delta)%n + n) % n
		}
	case ConfigChange:
		return s.changeRow(e.Delta), true
	case ConfigType:
		if p.Editing {
			p.Input = typeInto(p.Input, e.Text)
		}
	case ConfigEnter:
		if !p.Editing {
			return s.changeRow(1), true
		}

		return s.commitTyped(), true
	case ConfigEsc:
		if p.Editing {
			p.Editing, p.Input = false, ""
		} else {
			s.Config = nil
		}
	default:
		return nil, false
	}

	return nil, true
}

func (s *State) configLoaded(e ConfigLoaded) {
	if s.Config == nil {
		return
	}
	if e.Err != nil {
		s.notice(session.LevelError, "/config: "+e.Err.Error())
		s.Config = nil

		return
	}
	s.Config.Path, s.Config.Values = e.Path, e.Values
	key := s.Config.saved
	s.Config.saved = ""
	if v, ok := e.Values[key]; ok && v.Source != SourceUser && v.Source != sourceDefault && v.Source != sourceSession {
		s.notice(session.LevelWarning, fmt.Sprintf("%s still comes from %s, which wins over the user file", key, v.Source))
	}
}

// changeRow changes the selected row by delta, or starts typing it.
func (s *State) changeRow(delta int) []Effect {
	p := s.Config
	rows := s.ConfigRows()
	if p.Values == nil || p.Editing || p.Index >= len(rows) {
		return nil
	}
	row := rows[p.Index]
	value, ok := s.next(row, delta)
	if !ok {
		p.Editing, p.Input = true, ""
		v := p.Values[row.Key]
		sessions := row.Key == keyCompactModel && v.Source == sourceDefault // the session's model, not a name to edit
		if v.Value != "0" && !sessions {
			p.Input = v.Value
		}

		return nil
	}

	return s.save(row.Key, value)
}

// commitTyped saves the typed value, or says what is wrong with it.
func (s *State) commitTyped() []Effect {
	p := s.Config
	row := s.ConfigRows()[p.Index]
	value, err := typed(row, p.Input)
	if err != nil {
		s.notice(session.LevelError, err.Error())

		return nil
	}
	p.Editing, p.Input = false, ""

	return s.save(row.Key, value)
}

// save writes the value, shows it at once, and applies it to the running
// session where that works live: the model, effort, fast mode, adaptive
// effort, and permission mode through the session (as /model, /effort,
// /fast, /adaptive, and shift+tab do), the details view, the mouse, and
// file links in the TUI.
func (s *State) save(key string, value any) []Effect {
	p := s.Config
	p.Values[key] = ConfigValue{Value: valueText(value), Source: SourceUser}
	if value == nil {
		p.Values[key] = ConfigValue{Source: sourceDefault}
	}
	effects := []Effect{EffSaveConfig{Key: key, Value: value}}
	next := s.Settings
	switch key {
	case keyModel:
		next.Model, _ = value.(string)
	case keyEffort:
		next.Effort, _ = value.(string)
	case keyFast:
		next.ServiceTier = ""
		if on, _ := value.(bool); on {
			next.ServiceTier = "priority"
		}
	case keyAdaptive:
		next.AdaptiveEffort, _ = value.(string)
	case keyMode:
		mode, _ := value.(string)
		next = next.WithMode(approval.Mode(mode))
	case keyDetails:
		s.Details, _ = value.(bool)
	case keyMouse:
		s.Mouse, _ = value.(bool)
	case keyFileLinks:
		was := s.linksOn()
		s.FileLinks, _ = value.(string)
		if s.FileLinks == "" {
			s.FileLinks = LinksPeek // removed: the default
		}
		if !was {
			s.relinkAll()
		}
	}
	if next != s.Settings && (key != keyFast || s.Priority) {
		effects = append(effects, EffSetSettings{Settings: next})
	}

	return effects
}

// configSaved reports the save and when it applies, then reloads the
// values so their sources are current.
func (s *State) configSaved(e ConfigSaved) []Effect {
	if e.Err != nil {
		s.notice(session.LevelError, fmt.Sprintf("/config: %s was not saved: %v", e.Key, e.Err))
		if s.Config != nil {
			return []Effect{EffLoadConfig{}}
		}

		return nil
	}
	path := ""
	if s.Config != nil {
		path, s.Config.saved = " to "+s.Config.Path, e.Key
	}
	what := e.Key + " = " + valueText(e.Value)
	if e.Value == nil {
		what = e.Key + " removed (the default applies)"
	}
	s.notice(session.LevelInfo, fmt.Sprintf("saved %s%s; %s", what, path, appliesWhen(e.Key, s.Priority)))
	var effects []Effect
	if e.Key == keyAgentLimit {
		n, _ := e.Value.(int64) // nil, the default, is 0
		effects = append(effects, EffAgentLimit{Max: int(n)})
	}
	if s.Config != nil {
		effects = append(effects, EffLoadConfig{})
	}

	return effects
}

// appliesWhen says when a saved key takes effect.
func appliesWhen(key string, fastLive bool) string {
	switch {
	case key == keyDetails || key == keyMouse || key == keyFileLinks || key == keyAgentLimit:
		return "applies now"
	case key == keyFast && !fastLive:
		return "applies to new sessions on a provider with fast mode"
	case slices.Contains([]string{keyModel, keyEffort, keyFast, keyAdaptive, keyMode}, key):
		return "this session changes too"
	}

	return "applies to sessions opened from now on (/new, /resume)"
}

// typeInto edits text with typed input; "\b" deletes the last character.
func typeInto(text, in string) string {
	if in != "\b" {
		return text + in
	}
	if r := []rune(text); len(r) > 0 {
		return string(r[:len(r)-1])
	}

	return text
}
