package state

import (
	"strings"

	"github.com/sahilm/fuzzy"

	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/mcp"
	"github.com/viktordanov/uah/internal/models"
	"github.com/viktordanov/uah/internal/session"
)

// menuSize is how many suggestions the menu shows.
const menuSize = 6

// Menu is the suggestion menu under the composer: commands and their
// arguments after "/", workspace files after "@".
type Menu struct {
	// Index is the selected suggestion.
	Index int
	// Closed is the draft the menu was closed for; it opens again when the
	// draft changes.
	Closed string
	// Files are the workspace's files for "@", loaded on first use.
	Files        []string
	filesLoading bool
	// found is the files that best match the last draft's "@" word, kept
	// by DraftChanged and FilesLoaded so the menu's keys and every frame
	// do not search the file list again.
	found fileMatches
	// Models is the provider's model list for /model, loaded on first use.
	Models        *models.Catalog
	modelsLoading bool
	// Review is the branches and commits for /review (reviewmenu.go).
	Review        *ReviewTargetsLoaded
	reviewLoading bool
	// Prompts are the MCP servers' prompts for "/", loaded again each time
	// "/" starts a draft, as servers can change them.
	Prompts        []mcp.Prompt
	promptsLoading bool
	// Resources are the MCP servers' resources for "@", loaded again each
	// time an "@" word starts.
	Resources        []mcp.ResourceRef
	resourcesLoading bool
	// Skills are the skills for "$" (skills.go), loaded again each time a
	// "$" word starts, as a skill can be added meanwhile.
	Skills        []engine.Skill
	skillsLoading bool
}

// fileMatches is the best files for an "@" word's query, at most a menu's
// worth; ok is false until the file list is searched for it.
type fileMatches struct {
	query string
	paths []string
	ok    bool
}

// findFiles searches the file list; a variable so a test can count it.
var findFiles = fuzzy.Find

// Suggestion is one menu entry. Accepting it replaces the draft with Draft.
type Suggestion struct {
	Label string
	Help  string
	Draft string
	// Resource marks an MCP resource after "@", which is never a file
	// to attach.
	Resource bool
}

// Menu intents carry the composer's text, which the shell owns.
type (
	// MenuMove moves the selection.
	MenuMove struct {
		Draft string
		Delta int
	}
	// MenuAccept puts the selected suggestion into the composer.
	MenuAccept struct{ Draft string }
	// MenuEnter runs the selected command, or accepts the selection when it
	// still needs an argument or is a file.
	MenuEnter struct{ Draft string }
	// MenuClose hides the menu until the draft changes.
	MenuClose struct{ Draft string }
	// DraftChanged reports a new composer text, so the selection resets and
	// "@" can load the file list.
	DraftChanged struct{ Draft string }
	// FilesLoaded carries the workspace's files for "@".
	FilesLoaded struct{ Paths []string }
)

// Suggestions returns the menu for a draft, or nothing when it is closed.
func (s State) Suggestions(draft string) []Suggestion {
	if draft == "" || draft == s.Menu.Closed || s.Shell || s.showsRecalled(draft) {
		return nil
	}
	if at, ok := s.skillAt(draft); ok {
		return s.skillSuggestions(draft, at)
	}
	if at, ok := mentionAt(draft); ok {
		out := append(s.resourceSuggestions(draft, at), s.fileSuggestions(draft, at)...)

		return out[:min(menuSize, len(out))]
	}
	if !strings.HasPrefix(draft, "/") || strings.Contains(draft, "\n") {
		return nil
	}
	name, arg, hasArg := strings.Cut(strings.TrimPrefix(draft, "/"), " ")
	if !hasArg {
		return append(commandSuggestions(name), s.promptSuggestions(name)...)
	}

	return s.argSuggestions(name, arg)
}

func commandSuggestions(prefix string) []Suggestion {
	var out []Suggestion
	for _, c := range Complete(prefix) {
		label := "/" + c.Name
		if c.Args != "" {
			label += " " + c.Args
		}
		draft := "/" + c.Name
		if c.Args != "" {
			draft += " "
		}
		out = append(out, Suggestion{Label: label, Help: c.Help, Draft: draft})
	}

	return out
}

// argSuggestions completes a command's argument where the values are known.
func (s State) argSuggestions(name, arg string) []Suggestion {
	var values []string
	switch name {
	case "model":
		return s.modelSuggestions(arg)
	case cmdReviewName:
		return s.reviewSuggestions(arg)
	case "effort":
		values = session.Efforts
	case "adaptive":
		return s.adaptiveSuggestions(arg)
	case "goal":
		return s.goalSuggestions(arg)
	case cmdAgentsName:
		values = s.agentNames()
	case "resume":
		for _, in := range s.Picker.Sessions {
			values = append(values, in.ID[:min(8, len(in.ID))])
		}
	default:
		return nil
	}
	var out []Suggestion
	for _, v := range values {
		if strings.HasPrefix(v, arg) && v != arg {
			out = append(out, Suggestion{Label: v, Draft: "/" + name + " " + v})
		}
	}

	return out
}

// mentionAt finds an "@" word at the end of the draft and returns its start.
func mentionAt(draft string) (int, bool) {
	start := strings.LastIndexAny(draft, " \n\t") + 1
	if strings.HasPrefix(draft[start:], "@") {
		return start, true
	}

	return 0, false
}

func (s State) fileSuggestions(draft string, at int) []Suggestion {
	query := draft[at+1:]
	paths := s.Menu.found.paths
	if !s.Menu.found.ok || s.Menu.found.query != query {
		paths = s.bestFiles(query) // a draft DraftChanged did not report
	}
	out := make([]Suggestion, 0, len(paths))
	for _, f := range paths {
		out = append(out, Suggestion{Label: f, Draft: draft[:at] + f + " "})
	}

	return out
}

// bestFiles returns the files that best match query, the first ones for
// an empty query.
func (s State) bestFiles(query string) []string {
	if query == "" {
		return s.Menu.Files[:min(menuSize, len(s.Menu.Files))]
	}
	var out []string
	for _, m := range findFiles(query, s.Menu.Files) {
		out = append(out, m.Str)
		if len(out) == menuSize {
			break
		}
	}

	return out
}

// matchFiles keeps the files for the draft's "@" word, searched once per
// draft and file list rather than at each key and frame.
func (s *State) matchFiles(draft string) {
	s.Menu.found = fileMatches{}
	at, ok := mentionAt(draft)
	if !ok {
		return
	}
	s.Menu.found.query = draft[at+1:]
	if s.Menu.Files != nil {
		s.Menu.found.paths, s.Menu.found.ok = s.bestFiles(s.Menu.found.query), true
	}
}

// loadMenu loads what the menu offers for the draft: the files once and
// the MCP resources each time an "@" word starts, and the MCP prompts each
// time "/" starts a draft (or first while a command's name is typed).
func (s *State) loadMenu(draft string) []Effect {
	var effects []Effect
	if at, mention := mentionAt(draft); mention {
		if s.Menu.Files == nil && !s.Menu.filesLoading {
			s.Menu.filesLoading = true
			effects = append(effects, EffLoadFiles{})
		}
		if (s.Menu.Resources == nil || at == len(draft)-1) && !s.Menu.resourcesLoading {
			s.Menu.resourcesLoading = true
			effects = append(effects, EffLoadMCPResources{})
		}
	}
	effects = append(effects, s.loadSkills(draft)...)
	naming := strings.HasPrefix(draft, "/") && !strings.ContainsAny(draft, " \n")
	if naming && (s.Menu.Prompts == nil || draft == "/") && !s.Menu.promptsLoading {
		s.Menu.promptsLoading = true
		effects = append(effects, EffLoadMCPPrompts{})
	}

	return effects
}

// onMenu handles the menu intents; ok is false for any other event.
func (s *State) onMenu(ev any) (effects []Effect, ok bool) {
	switch e := ev.(type) {
	case DraftChanged:
		s.Menu.Index = 0
		s.pruneImages(e.Draft)
		s.matchFiles(e.Draft)
		if eff := s.loadModels(e.Draft); eff != nil {
			return []Effect{eff}, true
		}
		if eff := s.loadReviewTargets(e.Draft); eff != nil {
			return []Effect{eff}, true
		}

		return s.loadMenu(e.Draft), true
	case SkillsLoaded:
		s.Menu.Skills, s.Menu.skillsLoading = e.Skills, false
	case MCPPromptsLoaded:
		s.Menu.Prompts, s.Menu.promptsLoading = e.Prompts, false
	case MCPResourcesLoaded:
		s.Menu.Resources, s.Menu.resourcesLoading = e.Resources, false
	case FilesLoaded:
		s.Menu.Files, s.Menu.filesLoading = e.Paths, false
		if s.Menu.Files == nil {
			s.Menu.Files = []string{}
		}
		s.Menu.found.paths, s.Menu.found.ok = s.bestFiles(s.Menu.found.query), true
	case ModelsLoaded:
		s.Menu.Models, s.Menu.modelsLoading = &e.Catalog, false

		return s.modelsArrived(), true
	case MenuMove:
		if n := min(len(s.Suggestions(e.Draft)), menuSize); n > 0 {
			s.Menu.Index = ((s.Menu.Index+e.Delta)%n + n) % n
		}
	case MenuAccept:
		items := s.Suggestions(e.Draft)
		if len(items) == 0 {
			return nil, true
		}
		picked := items[min(s.Menu.Index, len(items)-1)]
		s.Menu.Index = 0
		if effects, ok := s.acceptImage(e.Draft, picked); ok {
			return effects, true
		}

		return s.setDraft(picked.Draft), true
	case MenuEnter:
		return s.menuEnter(e.Draft)
	case MenuClose:
		s.Menu.Closed = e.Draft
	default:
		return nil, false
	}

	return nil, true
}

// menuEnter runs the selected command, or accepts the selection when it
// still needs an argument or is a file; ok is false with no menu.
func (s *State) menuEnter(draft string) (effects []Effect, ok bool) {
	items := s.Suggestions(draft)
	if len(items) == 0 {
		return nil, false // an ordinary enter
	}
	picked := items[min(s.Menu.Index, len(items)-1)]
	s.Menu.Index = 0
	if effects, ok := s.acceptImage(draft, picked); ok {
		return effects, true
	}
	if strings.HasSuffix(picked.Draft, " ") && !bare(picked.Draft) {
		return s.setDraft(picked.Draft), true
	}
	next, effects := s.command(picked.Draft)
	*s = next

	return append([]Effect{EffSetDraft{Text: ""}}, effects...), true
}

// setDraft puts an accepted suggestion in the composer and, for /model or
// /review, starts loading the model list or the review targets.
func (s *State) setDraft(draft string) []Effect {
	effects := []Effect{EffSetDraft{Text: draft}}
	if eff := s.loadModels(draft); eff != nil {
		effects = append(effects, eff)
	}
	if eff := s.loadReviewTargets(draft); eff != nil {
		effects = append(effects, eff)
	}

	return effects
}

// bare reports whether a menu entry is a command that enter runs without
// an argument (Command.Bare).
func bare(draft string) bool {
	name, arg, _ := strings.Cut(strings.TrimPrefix(draft, "/"), " ")
	cmd, ok := FindCommand(name)

	return ok && cmd.Bare && arg == "" && strings.HasPrefix(draft, "/")
}

// MenuOpen reports whether the draft shows a menu, so the shell sends the
// menu keys to it.
func (s State) MenuOpen(draft string) bool { return len(s.Suggestions(draft)) > 0 }

// adaptiveSuggestions lists adaptive effort's values with what each does at
// the session's effort, the current one marked, so the steps need no
// remembering. A digit or a word ("2", "two") finds its value too.
func (s State) adaptiveSuggestions(arg string) []Suggestion {
	current := adaptiveText(s.Settings)
	var out []Suggestion
	for _, v := range session.AdaptiveEfforts {
		if v == arg || arg != "" && !strings.HasPrefix(v, arg) && adaptiveValue(arg) != v {
			continue // a complete value has no menu, as /effort's
		}
		help := "full effort on every turn"
		if session.AdaptiveSteps(v) > 0 {
			help = "follow-ups after tool results at " + session.FollowUpEffort(s.Settings.Effort, v)
		}
		if v == current {
			help += " (now)"
		}
		out = append(out, Suggestion{Label: v, Help: help, Draft: "/adaptive " + v})
	}

	return out
}
