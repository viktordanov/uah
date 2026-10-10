package embedded

import (
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/viktordanov/uah-core/harness/tool"

	"github.com/viktordanov/uah/internal/config"
	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/instructions"
	"github.com/viktordanov/uah/internal/systemskills"
)

// skillRoots are the directories holding <name>/SKILL.md skills, most
// specific first: Codex's .agents/skills from the workspace up to the
// project root, the runner's own .harness/skills, then the user's
// ~/.uah/skills and Codex's $CODEX_HOME/skills, and last uah's system
// skills, so a skill of the same name anywhere else wins.
func skillRoots(workspace string, getenv func(string) string) []string {
	var roots []string
	if dirs, err := instructions.ProjectDirs(workspace, nil); err == nil {
		for _, dir := range slices.Backward(dirs) {
			roots = append(roots, filepath.Join(dir, ".agents", "skills"))
		}
	}
	roots = append(roots, filepath.Join(workspace, ".harness", "skills"), filepath.Join(config.Dir(), "skills"))
	codexHome := strings.TrimSpace(getenv("CODEX_HOME"))
	if codexHome == "" {
		if home, err := os.UserHomeDir(); err == nil {
			codexHome = filepath.Join(home, ".codex")
		}
	}
	if codexHome != "" {
		roots = append(roots, filepath.Join(codexHome, "skills"))
	}

	return append(roots, systemSkillsRoot())
}

// systemSkillsRoot is where uah writes its system skills: ~/.uah/skills/.system.
func systemSkillsRoot() string {
	return filepath.Join(config.Dir(), "skills", systemskills.DirName)
}

// Skills lists the skills a run in workspace may load (engine.SkillLister):
// none with skills off, or when the tool policy refuses SkillUse.
func (e *Engine) Skills(workspace string) []engine.Skill {
	if e.cfg.NoSkills || !e.cfg.Tools.Allows(tool.SkillUseName) {
		return nil
	}
	found, _ := discoverSkills(workspace, e.cfg.Getenv)
	out := make([]engine.Skill, 0, len(found))
	for _, s := range found {
		out = append(out, engine.Skill{Name: s.Name, Description: s.Description})
	}

	return out
}

// discoverSkills writes the system skills, then reads the skills under
// every root with the runner's own SKILL.md parser; a name found in a more
// specific root wins.
func discoverSkills(workspace string, getenv func(string) string) ([]tool.Skill, []error) {
	var skills []tool.Skill
	var errs []error
	if err := systemskills.Install(systemSkillsRoot()); err != nil {
		errs = append(errs, err)
	}
	seen := map[string]bool{}
	for _, root := range skillRoots(workspace, getenv) {
		found, rootErrs := tool.DiscoverSkills(root)
		errs = append(errs, rootErrs...)
		for _, s := range found {
			if !seen[s.Name] {
				seen[s.Name] = true
				skills = append(skills, s)
			}
		}
	}

	return skills, errs
}
