package app

import (
	"maps"
	"slices"
	"strings"

	"github.com/viktordanov/uah/internal/config"
	"github.com/viktordanov/uah/internal/history"
	"github.com/viktordanov/uah/internal/hooks"
	"github.com/viktordanov/uah/internal/instructions"
	"github.com/viktordanov/uah/internal/mcp"
	"github.com/viktordanov/uah/internal/sandbox"
)

// fileSettings are the settings only the configuration files set.
func fileSettings(workspace string, l config.Layers, r Resolved, cfg config.Config) []Setting {
	fallbacks, markers, maxBytes := cfg.InstructionOptions()
	if cfg.ProjectRootMarkers == nil {
		markers = []string{".git"}
	}
	maxBytes = orDefault(maxBytes, instructions.DefaultMaxBytes)
	env := cfg.ShellEnvironmentPolicy
	excludes := true
	if env.IgnoreDefaultExcludes != nil {
		excludes = *env.IgnoreDefaultExcludes
	}
	trusted := pick(overrides(l, func(c config.Config) any {
		_, ok := c.Projects[workspace]

		return ok
	}), FromDefault)
	out := []Setting{
		overridden(l, "approvals_reviewer", r.ApprovalsReviewer, func(c config.Config) any { return c.ApprovalsReviewer }),
		overridden(l, "review.model", r.Review.Model, func(c config.Config) any { return c.Review.Model }),
		overridden(l, "review.effort", string(r.Review.Effort), func(c config.Config) any { return c.Review.Effort }),
		overridden(l, "review.timeout", r.Review.Timeout.String(), func(c config.Config) any { return c.Review.Timeout }),
		overridden(l, "review.policy_file", first(cfg.Review.PolicyFile, "Codex's policy"), func(c config.Config) any { return c.Review.PolicyFile }),
		overridden(l, "web_search", r.WebSearch, func(c config.Config) any { return c.WebSearch }),
		overridden(l, "review_model", first(cfg.ReviewModel, r.Settings.Model), func(c config.Config) any { return c.ReviewModel }),
	}
	out = append(out, compactionSettings(l, r, cfg)...)
	out = append(out, []Setting{
		overridden(l, "model_instructions_file", first(cfg.ModelInstructionsFile, "uah's default prompt"), func(c config.Config) any { return c.ModelInstructionsFile }),
		overridden(l, "instructions.max_bytes", orDefault(cfg.Instructions.MaxBytes, instructions.DefaultMaxBytes), func(c config.Config) any { return c.Instructions.MaxBytes }),
		one("project_doc_max_bytes", maxBytes, pick(
			overrides(l, func(c config.Config) any { return c.ProjectDocMaxBytes }),
			overrides(l, func(c config.Config) any { return c.Instructions.MaxBytes }), FromDefault,
		)),
		overridden(l, "project_doc_fallback_filenames", list(fallbacks), func(c config.Config) any { return c.ProjectDocFallbackFilenames }),
		overridden(l, "project_root_markers", list(markers), func(c config.Config) any { return c.ProjectRootMarkers }),
		added(l, "sandbox_workspace_write.network_access", r.Sandbox.Network, func(c config.Config) any { return c.SandboxWorkspaceWrite.NetworkAccess }),
		added(l, "sandbox_workspace_write.writable_roots", list(r.Sandbox.WritableRoots), func(c config.Config) any { return c.SandboxWorkspaceWrite.WritableRoots }),
		added(l, "approvals.allow", list(cfg.Approvals.Allow), func(c config.Config) any { return c.Approvals.Allow }),
		added(l, "approvals.forbid", list(cfg.Approvals.Forbid), func(c config.Config) any { return c.Approvals.Forbid }),
		overridden(l, "shell_environment_policy.inherit", first(env.Inherit, sandbox.InheritAll), func(c config.Config) any { return c.ShellEnvironmentPolicy.Inherit }),
		overridden(l, "shell_environment_policy.ignore_default_excludes", excludes, func(c config.Config) any { return c.ShellEnvironmentPolicy.IgnoreDefaultExcludes }),
		added(l, "shell_environment_policy.exclude", list(env.Exclude), func(c config.Config) any { return c.ShellEnvironmentPolicy.Exclude }),
		added(l, "shell_environment_policy.include_only", list(env.IncludeOnly), func(c config.Config) any { return c.ShellEnvironmentPolicy.IncludeOnly }),
		added(l, "shell_environment_policy.set", setMap(env.Set), func(c config.Config) any { return c.ShellEnvironmentPolicy.Set }),
		added(l, "tui.details", cfg.TUI.Details, func(c config.Config) any { return c.TUI.Details }),
		overridden(l, "tui.mouse", cfg.TUI.MouseOn(), func(c config.Config) any { return c.TUI.Mouse }),
		overridden(l, "tui.title", cfg.TUI.TitleOn(), func(c config.Config) any { return c.TUI.Title }),
		overridden(l, "tui.file_links", cfg.TUI.FileLinksMode(), func(c config.Config) any { return c.TUI.FileLinks }),
		overridden(l, "history.persistence", first(cfg.History.Persistence, string(history.SaveAll)), func(c config.Config) any { return c.History.Persistence }),
		overridden(l, "history.max_bytes", historyMaxBytes(cfg.History), func(c config.Config) any { return c.History.MaxBytes }),
		overridden(l, "features.goals", !r.Goals.Disabled, func(c config.Config) any { return c.Features.Goals }),
		overridden(l, "goals.max_goal_token_budget", r.Goals.MaxTokenBudget, func(c config.Config) any { return c.Goals.MaxGoalTokenBudget }),
		overridden(l, "goals.max_continuations", r.Goals.MaxContinuations, func(c config.Config) any { return c.Goals.MaxContinuations }),
	}...)
	out = append(out, hookSettings(l, cfg)...)
	out = append(out, mcpSettings(l, cfg)...)

	return append(out, one("projects.<workspace>.trusted", l.Trusted, trusted))
}

// historyMaxBytes is the history file's cap: the key, else the default.
func historyMaxBytes(h config.History) int64 {
	if h.MaxBytes != nil {
		return *h.MaxBytes
	}

	return history.DefaultMaxBytes
}

// hookSettings are the commands per hook event, in the order they run.
func hookSettings(l config.Layers, cfg config.Config) []Setting {
	var out []Setting
	for _, event := range hooks.Events {
		list := cfg.Hooks[string(event)]
		if len(list) == 0 {
			continue
		}
		commands := make([]string, 0, len(list))
		for _, h := range list {
			commands = append(commands, h.Command)
		}
		out = append(out, added(l, "hooks."+string(event), commands, func(c config.Config) any { return c.Hooks[string(event)] }))
	}

	return out
}

// mcpSettings describe each MCP server by name; a server in a later file
// replaces one of the same name.
func mcpSettings(l config.Layers, cfg config.Config) []Setting {
	out := make([]Setting, 0, len(cfg.MCPServers))
	for _, name := range slices.Sorted(maps.Keys(cfg.MCPServers)) {
		out = append(out, overridden(l, "mcp_servers."+name, describeServer(cfg.MCPServers[name]), func(c config.Config) any {
			_, ok := c.MCPServers[name]

			return ok
		}))
	}

	return out
}

// describeServer is a server's command line or URL, and whether it is off.
func describeServer(s mcp.ServerConfig) string {
	text := s.URL
	if s.Command != "" {
		text = strings.Join(append([]string{s.Command}, s.Args...), " ")
	}
	if !s.IsEnabled() {
		text += " (disabled)"
	}

	return text
}

// overridden is a key a later file overrides.
func overridden(l config.Layers, key string, value any, get func(config.Config) any) Setting {
	return one(key, value, pick(overrides(l, get), FromDefault))
}

// added is a key whose files add up (lists and maps) or are OR-ed (booleans).
func added(l config.Layers, key string, value any, get func(config.Config) any) Setting {
	return Setting{Key: key, Value: value, Sources: adds(l, get)}
}

// list is v, or an empty list rather than nil.
func list(v []string) []string {
	if v == nil {
		return []string{}
	}

	return v
}

func setMap(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}

	return m
}

// orDefault is n, or d when n is 0.
func orDefault(n, d int) int {
	if n == 0 {
		return d
	}

	return n
}
