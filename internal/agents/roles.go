// Package agents runs subagents: child sessions a session's agent spawns
// with the spawn_agent tool, following Codex's v1 multi-agent tools (see
// docs/design/subagents.md). It implements engine.Subagents.
package agents

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// Role is an agent type from a Codex role file (TOML) or a Markdown agent
// file with front matter, as Claude Code's. uah reads the subset of their
// keys that map to its settings.
type Role struct {
	Name               string   `toml:"name"`
	Description        string   `toml:"description"`
	NicknameCandidates []string `toml:"nickname_candidates"`
	// Model and Effort override the parent's for agents of this role.
	Model  string `toml:"model"`
	Effort string `toml:"model_reasoning_effort"`
	// ServiceTier is Codex's service_tier: "priority" (or its legacy name
	// "fast") runs the agent with priority processing, and "default" without
	// it, whatever the parent uses. Empty follows the parent.
	ServiceTier string `toml:"-"`
	// DeveloperInstructions are added to the agent's system prompt; a
	// Markdown file's body.
	DeveloperInstructions string `toml:"developer_instructions"`
	// Tools are the tools its agents are offered, by uah's names (see
	// mapTools); nil offers every tool.
	Tools []string `toml:"-"`
	// Approve are actions its agents run without asking, within the
	// permission mode: command prefixes and MCP tools (see mapApprove).
	Approve []string `toml:"approve"`
	// Path is the file the role came from.
	Path string `toml:"-"`
}

// LoadRoles reads the agent definitions under each directory: Codex's
// *.toml role files and Markdown *.md files with front matter. A later
// directory replaces an earlier role of the same name, as Codex's config
// layers do; within one directory a Markdown file wins over a TOML file of
// the same name, with a warning. Missing directories are skipped. A
// malformed file is left out with a warning, as Codex does; so are keys
// uah does not support.
func LoadRoles(dirs ...string) (roles []Role, warnings []string) {
	byName := map[string]Role{}
	for _, dir := range dirs {
		files, err := roleFiles(dir)
		if err != nil {
			warnings = append(warnings, err.Error())

			continue
		}
		layer := map[string]Role{}
		for _, path := range files {
			r, warns, err := readDefinition(path)
			warnings = append(warnings, warns...)
			if err != nil {
				warnings = append(warnings, "ignoring malformed agent role: "+err.Error())

				continue
			}
			if old, ok := layer[r.Name]; ok {
				r = clash(old, r)
				warnings = append(warnings, fmt.Sprintf("agent %q is defined twice in %s; using %s", r.Name, dir, r.Path))
			}
			layer[r.Name] = r
		}
		maps.Copy(byName, layer)
	}
	for _, r := range byName {
		roles = append(roles, r)
	}
	slices.SortFunc(roles, func(a, b Role) int { return strings.Compare(a.Name, b.Name) })

	return roles, warnings
}

// clash picks one of two definitions of a name in one directory: a
// Markdown file over a TOML file, else the later file.
func clash(old, r Role) Role {
	if isMarkdown(old.Path) && !isMarkdown(r.Path) {
		return old
	}

	return r
}

func isMarkdown(path string) bool { return filepath.Ext(path) == ".md" }

// readDefinition reads a role file of either format.
func readDefinition(path string) (Role, []string, error) {
	if isMarkdown(path) {
		return readMarkdown(path)
	}

	return readRole(path)
}

// roleFiles lists the .toml and .md files under dir, recursively, sorted.
func roleFiles(dir string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ext := filepath.Ext(path); !d.IsDir() && (ext == ".toml" || ext == ".md") {
			files = append(files, path)
		}

		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to list agent roles in %s: %w", dir, err)
	}
	sort.Strings(files)

	return files, nil
}

// readRole parses and checks one TOML role file, with warnings about what
// it ignored.
func readRole(path string) (Role, []string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Role{}, nil, fmt.Errorf("failed to read %s: %w", path, err)
	}
	// tools and service_tier take the forms a Markdown file's do.
	var f struct {
		Role

		Tools       *nameList `toml:"tools"`
		ServiceTier tierValue `toml:"service_tier"`
	}
	meta, err := toml.Decode(string(data), &f)
	if err != nil {
		return Role{}, nil, fmt.Errorf("failed to parse %s: %w", path, err)
	}
	r := f.Role
	if f.Tools != nil {
		r.Tools = []string(*f.Tools)
	}
	r.ServiceTier = string(f.ServiceTier)
	var ignored []string
	for _, k := range meta.Undecoded() {
		ignored = append(ignored, k[0])
	}
	if tier, ok := serviceTier(r.ServiceTier); ok {
		r.ServiceTier = tier
	} else {
		ignored = append(ignored, fmt.Sprintf("service_tier = %q (want priority, fast, or default)", r.ServiceTier))
		r.ServiceTier = ""
	}
	r.Path = path
	warnings := r.finish(ignored)
	if err := r.check(); err != nil {
		return Role{}, nil, fmt.Errorf("%s: %w", path, err)
	}

	return r, warnings, nil
}

// finish trims the role's names, maps its tools and approvals to uah's,
// and returns its warnings: the ignored keys, then the tools and
// approvals uah cannot use.
func (r *Role) finish(ignored []string) []string {
	r.Name, r.Description = strings.TrimSpace(r.Name), strings.TrimSpace(r.Description)
	var warnings, notes []string
	if len(ignored) > 0 {
		slices.Sort(ignored)
		warnings = append(warnings, fmt.Sprintf("agent role %s: ignoring keys uah does not support: %s", r.Path, strings.Join(slices.Compact(ignored), ", ")))
	}
	if r.Tools != nil {
		var w []string
		r.Tools, w = mapTools(r.Tools)
		notes = append(notes, w...)
	}
	var w []string
	r.Approve, w = mapApprove(r.Approve)
	notes = append(notes, w...)
	for _, n := range notes {
		warnings = append(warnings, fmt.Sprintf("agent role %s: %s", r.Path, n))
	}

	return warnings
}

// check applies Codex's rules for a role file.
func (r Role) check() error {
	switch {
	case r.Name == "":
		return errors.New("the role must define a non-empty name")
	case r.Description == "":
		return errors.New("the role must define a non-empty description")
	case strings.TrimSpace(r.DeveloperInstructions) == "":
		return errors.New("the role must define developer_instructions")
	}
	seen := map[string]bool{}
	for _, n := range r.NicknameCandidates {
		n = strings.TrimSpace(n)
		valid := n != "" && strings.IndexFunc(n, func(c rune) bool { return !nicknameRune(c) }) < 0
		if !valid || seen[n] {
			return fmt.Errorf("invalid nickname candidate %q (ASCII letters, digits, spaces, hyphens, and underscores, no duplicates)", n)
		}
		seen[n] = true
	}
	if r.NicknameCandidates != nil && len(r.NicknameCandidates) == 0 {
		return errors.New("nickname_candidates must contain at least one name")
	}

	return nil
}

// nicknameRune reports whether c may appear in a nickname: ASCII letters,
// digits, spaces, hyphens, and underscores.
func nicknameRune(c rune) bool {
	return c == ' ' || c == '-' || c == '_' || ('0' <= c && c <= '9') || ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z')
}

// defaultRole is the agent type of a child spawned without one.
const defaultRole = "default"

// Role service tiers: Codex's request values that uah's providers serve.
const (
	TierPriority = "priority"
	TierDefault  = "default"
)

// serviceTier normalizes a role's service_tier as Codex reads it: "fast"
// is the legacy name of "priority". Codex also sends "flex", which no
// provider of uah's serves, so it is not ok.
func serviceTier(v string) (string, bool) {
	switch strings.TrimSpace(v) {
	case "":
		return "", true
	case "priority", "fast":
		return TierPriority, true
	case TierDefault:
		return TierDefault, true
	}

	return "", false
}
