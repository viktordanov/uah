package state

import (
	"fmt"
	"slices"
	"strings"

	"github.com/viktordanov/uah/internal/models"
	"github.com/viktordanov/uah/internal/session"
)

// The /model picker is Codex's model popup and its reasoning popup as one
// panel: /model lists the provider's models, enter on one lists that
// model's efforts, and enter on an effort applies both to the session. esc
// on the efforts goes back to the models, as in Codex; esc on the models
// closes the panel. /model <id> opens it at the model's efforts, and
// /model <id> <effort> applies both without it.

// ModelPicker is the /model panel.
type ModelPicker struct {
	// Model is the model whose efforts are listed; "" while choosing it.
	Model string
	// Index is the selected row.
	Index int
	// Loading waits for the provider's model list.
	Loading bool
}

// EffortRow is one level of the picker's effort step.
type EffortRow struct {
	Level string
	Help  string
	// Default is the model's default level; Current the session's.
	Default, Current bool
}

// Picker intents, from keys while the panel is open.
type (
	// ModelPickMove moves the selection.
	ModelPickMove struct{ Delta int }
	// ModelPickEnter chooses the selected model or effort.
	ModelPickEnter struct{}
	// ModelPickEsc goes back from the efforts to the models, or closes.
	ModelPickEsc struct{}
)

// cmdModel opens the picker, at a named model's efforts, or applies a model
// and an effort given together.
func cmdModel(s *State, args string) []Effect {
	id, effort, _ := strings.Cut(args, " ")
	effort = strings.TrimSpace(effort)
	if id != "" {
		next := s.Settings
		next.Model = id
		if err := next.Validate(); err != nil {
			s.notice(session.LevelError, err.Error())

			return nil
		}
		if err := s.checkModel(id); err != nil {
			s.notice(session.LevelError, err.Error())

			return nil
		}
	}
	if effort != "" {
		return s.applyModel(id, effort)
	}
	s.ModelPicker = &ModelPicker{Model: id}
	if _, ok := s.catalog(); ok {
		return s.showPicker()
	}
	s.ModelPicker.Loading = true
	if s.Menu.modelsLoading {
		return nil
	}
	s.Menu.modelsLoading = true

	return []Effect{EffLoadModels{Provider: s.Settings.Provider}}
}

// modelsArrived shows the picker that waited for the model list.
func (s *State) modelsArrived() []Effect {
	p := s.ModelPicker
	if p == nil || !p.Loading {
		return nil
	}
	if _, ok := s.catalog(); !ok {
		return nil // another provider's list
	}
	p.Loading = false
	if err := s.checkModel(p.Model); p.Model != "" && err != nil {
		s.notice(session.LevelError, err.Error())
		s.ModelPicker = nil

		return nil
	}

	return s.showPicker()
}

// showPicker selects the current model, or the named model's preselected
// effort; with no models to list it closes and says why.
func (s *State) showPicker() []Effect {
	p := s.ModelPicker
	if p.Model != "" {
		return s.pickModel(p.Model)
	}
	list := s.PickerModels()
	if len(list) == 0 {
		s.notice(session.LevelWarning, s.Settings.Provider+" lists no models; use /model <id> [effort]")
		s.ModelPicker = nil

		return nil
	}
	p.Index = max(slices.IndexFunc(list, func(m models.Model) bool { return m.ID == s.Settings.Model }), 0)

	return nil
}

// pickModel moves to the model's efforts, preselected as Codex does: the
// current effort on the current model, else the model's default. A model
// with one level applies at once.
func (s *State) pickModel(id string) []Effect {
	p := s.ModelPicker
	p.Model = id
	rows := s.PickerEfforts()
	if len(rows) == 1 {
		return s.applyModel(id, rows[0].Level)
	}
	p.Index = 0
	if i := slices.IndexFunc(rows, func(r EffortRow) bool { return r.Current }); i >= 0 {
		p.Index = i
	} else if i := slices.IndexFunc(rows, func(r EffortRow) bool { return r.Default }); i >= 0 {
		p.Index = i
	}

	return nil
}

// onModelPicker handles the panel's intents; ok is false for others.
func (s *State) onModelPicker(ev any) (effects []Effect, ok bool) {
	p := s.ModelPicker
	if p == nil {
		return nil, false
	}
	switch e := ev.(type) {
	case ModelPickMove:
		if n := s.pickerRows(); n > 0 && !p.Loading {
			p.Index = ((p.Index+e.Delta)%n + n) % n
		}
	case ModelPickEnter:
		if p.Loading {
			return nil, true
		}
		if p.Model != "" {
			return s.applyModel(p.Model, s.PickerEfforts()[p.Index].Level), true
		}
		if list := s.PickerModels(); p.Index < len(list) {
			return s.pickModel(list[p.Index].ID), true
		}
	case ModelPickEsc:
		if p.Model == "" || p.Loading {
			s.ModelPicker = nil

			return nil, true
		}
		model := p.Model
		p.Model = ""
		p.Index = max(slices.IndexFunc(s.PickerModels(), func(m models.Model) bool { return m.ID == model }), 0)
	default:
		return nil, false
	}

	return nil, true
}

// pickerRows counts the rows of the picker's step.
func (s State) pickerRows() int {
	if s.ModelPicker.Model != "" {
		return len(s.PickerEfforts())
	}

	return len(s.PickerModels())
}

// applyModel closes the picker and sets the model and effort, as /model
// and /effort do: for this session only; /config saves them.
func (s *State) applyModel(id, effort string) []Effect {
	s.ModelPicker = nil
	levels, _, _ := s.modelLevels(id)
	if !slices.Contains(levels, effort) {
		s.notice(session.LevelError, fmt.Sprintf("%s does not accept effort %s (%s)", id, effort, strings.Join(levels, ", ")))

		return nil
	}
	next := s.Settings
	next.Model, next.Effort = id, effort
	s.constrainSpeed(&next)
	if err := next.Validate(); err != nil {
		s.notice(session.LevelError, err.Error())

		return nil
	}
	if next == s.Settings {
		s.notice(session.LevelInfo, fmt.Sprintf("%s/%s · effort %s, unchanged", next.Provider, next.Model, next.Effort))

		return nil
	}

	return []Effect{EffSetSettings{Settings: next}}
}

// PickerModels are the models the picker lists: the provider's visible
// models, in priority order.
func (s State) PickerModels() []models.Model {
	c, ok := s.catalog()
	if !ok {
		return nil
	}

	return c.Visible()
}

// PickerEfforts are the efforts of the model the picker chose, lowest first.
func (s State) PickerEfforts() []EffortRow {
	p := s.ModelPicker
	if p == nil || p.Model == "" {
		return nil
	}
	levels, def, help := s.modelLevels(p.Model)
	rows := make([]EffortRow, 0, len(levels))
	for _, l := range levels {
		rows = append(rows, EffortRow{
			Level: l, Help: help[l], Default: l == def,
			Current: p.Model == s.Settings.Model && l == s.Settings.Effort,
		})
	}

	return rows
}

// modelLevels are a model's levels, default, and descriptions from the
// loaded list; without an entry, every level uah knows.
func (s State) modelLevels(id string) (levels []string, def string, help map[string]string) {
	if c, ok := s.catalog(); ok {
		if m, ok := c.Metadata(id); ok && len(m.ReasoningLevels) > 0 {
			return m.ReasoningLevels, m.DefaultEffort, m.ReasoningHelp
		}
	}

	return session.Efforts, "", nil
}

func (s *State) constrainSpeed(next *session.Settings) {
	if next.ServiceTier == "ultrafast" {
		c, _ := s.catalog()
		if !c.OffersTier(next.Model, next.ServiceTier) {
			next.ServiceTier = ""
			s.notice(session.LevelInfo, "Switched to standard speed: ultrafast is not offered for "+next.Model)
		}
	}
}
