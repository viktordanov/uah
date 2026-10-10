package embedded

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"unicode"

	"github.com/google/uuid"

	"github.com/viktordanov/uagent/core"
	"github.com/viktordanov/uah-core/harness/tool"
)

// Skill mentions: a user message that names a skill as "$name" loads it,
// as Codex's skill injection does. Before the message, the run gets a
// developer message with the skill's SKILL.md, so the model has it before
// it answers, without a SkillUse call; a compaction keeps it whole, as it
// keeps every developer message of the harness's (compaction.Developer).
// A "$" word that names no skill, such as $HOME, stays text.

// withMentions puts the skills the messages mention before each message
// that names them, once per message; messages as they are when the run
// may not load a skill (mentionable).
func (w *wiring) withMentions(req core.Request, messages []core.UserInput) []core.UserInput {
	if !slices.ContainsFunc(messages, func(m core.UserInput) bool { return m.Role != core.RoleDeveloper && strings.Contains(m.Text, "$") }) || !w.mentionable(req) {
		return messages
	}
	skills, _ := discoverSkills(req.Workspace, w.getenv)
	out := make([]core.UserInput, 0, len(messages))
	for _, m := range messages {
		if m.Role != core.RoleDeveloper {
			out = append(out, skillMessages(skills, m.Text)...)
		}
		out = append(out, m)
	}

	return out
}

// mentionable reports whether the run may load a skill: skills and their
// mentions are on, and neither the tool policy nor the subagent's scope
// refuses SkillUse.
func (w *wiring) mentionable(req core.Request) bool {
	if w.e.cfg.NoSkills || w.e.cfg.NoSkillMentions || !w.e.cfg.Tools.Allows(tool.SkillUseName) {
		return false
	}

	return !slices.Contains(w.e.scope(req.SessionID).disallow(req.DisallowedTools), tool.SkillUseName)
}

// skillMessages are developer messages with the skills text mentions, in
// the order it first names them; a skill whose file does not read is left
// out.
func skillMessages(skills []tool.Skill, text string) []core.UserInput {
	var out []core.UserInput
	for _, name := range mentions(text) {
		i := slices.IndexFunc(skills, func(s tool.Skill) bool { return s.Name == name })
		if i < 0 {
			continue
		}
		body, err := os.ReadFile(skills[i].Path)
		if err != nil {
			continue
		}
		out = append(out, core.UserInput{ID: uuid.NewString(), Text: skillText(skills[i], string(body)), Role: core.RoleDeveloper})
	}

	return out
}

// skillText is a loaded skill as Codex gives it to the model.
func skillText(s tool.Skill, body string) string {
	return fmt.Sprintf("<skill>\n<name>%s</name>\n<path>%s</path>\n%s\n</skill>", s.Name, s.Path, strings.ToValidUTF8(strings.TrimRight(body, "\n"), "�"))
}

// mentions are the names text mentions as "$name", once each, in order: a
// "$" that starts a word, then letters, digits, "-", "_", ":" and ".",
// less any trailing punctuation ("$imagegen:" and "$imagegen." name
// imagegen).
func mentions(text string) []string {
	var out []string
	for i := 0; i < len(text); i++ {
		if text[i] != '$' || (i > 0 && (isNameRune(rune(text[i-1])) || text[i-1] == '$')) {
			continue
		}
		end := i + 1
		for end < len(text) && isNameRune(rune(text[end])) {
			end++
		}
		name := strings.TrimRight(text[i+1:end], ":.-")
		if name != "" && !slices.Contains(out, name) {
			out = append(out, name)
		}
		i = end - 1
	}

	return out
}

// isNameRune reports whether r can be part of a mentioned skill's name; a
// byte of a multibyte rune cannot.
func isNameRune(r rune) bool {
	return r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("-_:.", r))
}
