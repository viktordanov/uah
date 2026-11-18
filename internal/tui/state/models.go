package state

import (
	"fmt"
	"slices"
	"strings"

	"github.com/viktordanov/uah/internal/models"
)

type (
	// EffLoadModels loads the provider's model list for /model.
	EffLoadModels struct{ Provider string }
	// ModelsLoaded carries the provider's model list.
	ModelsLoaded struct{ Catalog models.Catalog }
)

func (EffLoadModels) effect() {}

// loadModels starts loading the model list when the draft is /model and
// the current provider's list is neither loaded nor loading.
func (s *State) loadModels(draft string) Effect {
	if (!strings.HasPrefix(draft, "/model") && !strings.HasPrefix(draft, "/speed")) || s.Menu.modelsLoading {
		return nil
	}
	if s.Menu.Models != nil && s.Menu.Models.Provider == s.Settings.Provider {
		return nil
	}
	s.Menu.modelsLoading = true

	return EffLoadModels{Provider: s.Settings.Provider}
}

// catalog is the loaded list for the current provider, if any.
func (s State) catalog() (models.Catalog, bool) {
	if s.Menu.Models == nil || s.Menu.Models.Provider != s.Settings.Provider {
		return models.Catalog{}, false
	}

	return *s.Menu.Models, true
}

// modelSuggestions are the visible models whose ID starts with arg, then
// those that contain it.
func (s State) modelSuggestions(arg string) []Suggestion {
	c, ok := s.catalog()
	if !ok {
		return nil
	}
	var first, rest []Suggestion
	for _, m := range c.Visible() {
		if m.ID == arg {
			continue
		}
		sug := Suggestion{Label: m.ID, Help: ModelHelp(m), Draft: "/model " + m.ID}
		switch {
		case strings.HasPrefix(m.ID, arg):
			first = append(first, sug)
		case strings.Contains(m.ID, arg):
			rest = append(rest, sug)
		}
	}

	return append(first, rest...)
}

// ModelHelp is a model's help in the menu and the /model picker: its name,
// window, and fast mode.
func ModelHelp(m models.Model) string {
	var parts []string
	if m.DisplayName != "" && m.DisplayName != m.ID {
		parts = append(parts, m.DisplayName)
	}
	if m.ContextWindow > 0 {
		parts = append(parts, fmt.Sprintf("%dk context", m.ContextWindow/1000))
	}
	if m.SupportsPriority() {
		parts = append(parts, "/fast")
	}

	if slices.Contains(m.ServiceTiers, "ultrafast") {
		parts = append(parts, "ultrafast")
	}

	return strings.Join(parts, " · ")
}

// checkModel rejects a model the provider's list lacks, with near misses.
// Without a list from the provider, any model passes.
func (s State) checkModel(id string) error {
	c, ok := s.catalog()
	if !ok {
		return nil
	}

	return c.Check(id)
}

// efforts are the levels the current model accepts: the loaded list's, else
// every level uah knows.
func (s State) efforts() []string {
	levels, _, _ := s.modelLevels(s.Settings.Model)

	return levels
}

// checkEffort rejects a level the model's catalog entry does not list, such
// as ultra on gpt-6-luna.
func (s State) checkEffort(level string) error {
	if levels := s.efforts(); !slices.Contains(levels, level) {
		return fmt.Errorf("%s does not accept effort %s (%s)", s.Settings.Model, level, strings.Join(levels, ", "))
	}

	return nil
}

// stepEffort moves the effort by delta among the model's levels (alt+, and
// alt+.).
func (s *State) stepEffort(delta int) (State, []Effect) {
	levels := s.efforts()
	i := slices.Index(levels, s.Settings.Effort)
	if i < 0 {
		i = max(slices.Index(levels, "high"), 0)
	}
	j := min(max(i+delta, 0), len(levels)-1)
	if j == i && levels[i] == s.Settings.Effort {
		return *s, nil
	}
	next := s.Settings
	next.Effort = levels[j]

	return *s, []Effect{EffSetSettings{Settings: next}}
}
