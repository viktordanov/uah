package state

import (
	"cmp"
	"slices"
	"strings"
	"unicode/utf8"

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
// text after "$", the ones that start with it first, then the ones a few
// typos away (typos), the nearest first, each shown by its display name
// with its description, as Codex's skill popup. Accepting one puts
// "$name " in its place. A word no name is near, such as $HOME, has no
// menu.
func (s State) skillSuggestions(draft string, at int) []Suggestion {
	query := strings.ToLower(draft[at+1:])
	type near struct {
		item  Suggestion
		edits int
	}
	var starts, holds []Suggestion
	var typoed []near
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
		default:
			if edits := min(wordEdits(query, name), wordEdits(query, shown)); edits <= typos(query) {
				typoed = append(typoed, near{item, edits})
			}
		}
	}
	slices.SortStableFunc(typoed, func(a, b near) int { return cmp.Compare(a.edits, b.edits) })
	out := slices.Concat(starts, holds)
	for _, n := range typoed {
		out = append(out, n.item)
	}

	return out[:min(menuSize, len(out))]
}

// typos is how many edits the text after "$" may be from a name and still
// match: none under three letters, then one per three letters, at most
// three.
func typos(query string) int { return min(utf8.RuneCountInString(query)/3, 3) }

// wordEdits is the fewest edits (Levenshtein distance) from query to the
// start of name or of one of its words, so "tezt" is one edit from
// "testing" and "gotest" one from "go-testing".
func wordEdits(query, name string) int {
	best := prefixEdits([]rune(query), []rune(name))
	for i, r := range name {
		if i > 0 && strings.ContainsRune(" -_:", r) {
			best = min(best, prefixEdits([]rune(query), []rune(name[i+1:])))
		}
	}

	return best
}

// prefixEdits is the Levenshtein distance from query to the nearest
// prefix of name.
func prefixEdits(query, name []rune) int {
	row := make([]int, len(name)+1) // edits from query[:i] to name[:j]
	for j := range row {
		row[j] = j
	}
	for i := range query {
		diag := row[0]
		row[0] = i + 1
		for j := range name {
			cost := 1
			if query[i] == name[j] {
				cost = 0
			}
			diag, row[j+1] = row[j+1], min(row[j+1]+1, row[j]+1, diag+cost)
		}
	}

	return slices.Min(row)
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
