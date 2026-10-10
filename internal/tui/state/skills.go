package state

import (
	"cmp"
	"slices"
	"strings"

	"github.com/viktordanov/uah/internal/engine"
)

// EffLoadSkills asks for the skills a run may load, which the "$" menu
// completes, as Codex's skill popup does.
type EffLoadSkills struct{}

func (EffLoadSkills) effect() {}

// SkillsLoaded carries the skills for "$".
type SkillsLoaded struct{ Skills []engine.Skill }

// skillAt finds a "$" word at the end of the draft and returns its start;
// false with the menu turned off ([tui] skill_mentions = false).
func (s State) skillAt(draft string) (int, bool) {
	if s.NoSkillMentions {
		return 0, false
	}
	start := strings.LastIndexAny(draft, " \n\t") + 1
	if strings.HasPrefix(draft[start:], "$") {
		return start, true
	}

	return 0, false
}

// skillSuggestions lists the skills whose name or display name holds the
// text after "$", the ones that start with it first, each shown by its
// display name with its description, as Codex's skill popup. Accepting one
// puts "$name " in its place. A word no name holds, such as $HOME, has no
// menu.
func (s State) skillSuggestions(draft string, at int) []Suggestion {
	query := strings.ToLower(draft[at+1:])
	var starts, holds []Suggestion
	for _, sk := range s.Menu.Skills {
		name, shown := strings.ToLower(sk.Name), strings.ToLower(sk.DisplayName)
		if name == query {
			return nil // a complete name has no menu
		}
		item := Suggestion{Label: cmp.Or(sk.DisplayName, sk.Name), Help: sk.Description, Draft: draft[:at] + "$" + sk.Name + " "}
		switch {
		case strings.HasPrefix(name, query) || strings.HasPrefix(shown, query):
			starts = append(starts, item)
		case strings.Contains(name, query) || strings.Contains(shown, query):
			holds = append(holds, item)
		}
	}
	out := slices.Concat(starts, holds)

	return out[:min(menuSize, len(out))]
}

// loadSkills loads the skills when a "$" word starts, or the first time
// one is typed.
func (s *State) loadSkills(draft string) []Effect {
	at, ok := s.skillAt(draft)
	if !ok || s.Menu.skillsLoading || (s.Menu.Skills != nil && at != len(draft)-1) {
		return nil
	}
	s.Menu.skillsLoading = true

	return []Effect{EffLoadSkills{}}
}
