// Package config reads uah's configuration: a user file, the layers in
// config.d and UAH_EXTRA_CONFIG, and, for workspaces the user trusts, a
// project file. Unknown keys are errors, so typos surface.
package config

import (
	"fmt"
	"path/filepath"
	"slices"
	"time"

	"github.com/viktordanov/uah/internal/home"
	"github.com/viktordanov/uah/internal/hooks"
	"github.com/viktordanov/uah/internal/mcp"
	"github.com/viktordanov/uah/internal/toolpolicy"
)

// Config holds defaults below flags, the environment, and a resumed session.
type Config struct {
	Provider string `toml:"provider"`
	Model    string `toml:"model"`
	Effort   string `toml:"effort"`
	MaxDisk  string `toml:"max_disk"`
	// RequestMaxAttempts is how many times a model request is sent before
	// the run fails (0: uah's default, engine.DefaultMaxAttempts).
	RequestMaxAttempts int `toml:"request_max_attempts"`
	// Fast asks for priority processing.
	Fast bool `toml:"fast"`
	// SandboxMode is read-only or workspace-write (the default); the names
	// match Codex's. Codex's danger-full-access is yolo mode, which only
	// --yolo gives.
	SandboxMode string `toml:"sandbox_mode"`
	// PermissionMode is read-only, workspace, or auto: a sandbox mode and
	// who decides what needs approval. It overrides sandbox_mode when set.
	PermissionMode        string                `toml:"permission_mode"`
	SandboxWorkspaceWrite SandboxWorkspaceWrite `toml:"sandbox_workspace_write"`
	// ShellEnvironmentPolicy is which environment variables commands get.
	ShellEnvironmentPolicy ShellEnvironmentPolicy `toml:"shell_environment_policy"`
	// ApprovalPolicy is on-request (the default) or never, as Codex's.
	ApprovalPolicy string `toml:"approval_policy"`
	// Approvals are command prefixes allowed or forbidden besides the
	// rules files.
	Approvals Approvals `toml:"approvals"`
	// ApprovalsReviewer is who approves an action that needs approval:
	// auto_review (the default) or user, as Codex's key.
	ApprovalsReviewer string `toml:"approvals_reviewer"`
	// Review configures the auto-reviewer's model call.
	Review Review `toml:"review"`
	// UserShellSandbox runs the commands the user types in the TUI's shell
	// mode (`!`) like the agent's: in the permission mode's sandbox and
	// refused by forbid rules. Off (the default), they run as the user's
	// own, as in Codex and Claude Code.
	UserShellSandbox bool `toml:"user_shell_sandbox"`
	// WebSearch offers the provider's hosted web search tool: live (the
	// default where the provider has it) or disabled, as Codex's key.
	WebSearch string `toml:"web_search"`
	// AdaptiveEffort is adaptive effort for new sessions: off (the
	// default), 1-step, or 2-steps. On, the model thinks one or two effort
	// levels less on follow-up turns (requests that only follow tool
	// results). A session keeps its own, as it keeps its effort.
	AdaptiveEffort string `toml:"adaptive_effort"`
	// ContextPreparation starts each new session, subagents' included,
	// with one message of prepared context: the environment, the
	// sandbox, the workspace, the agent files, and the harness (true by
	// default).
	ContextPreparation *bool `toml:"context_preparation"`
	// Context configures context preparation's modules.
	Context Context `toml:"context"`
	// Tools turns tools on or off, in Codex's [tools] table.
	Tools Tools `toml:"tools"`
	// Skills turns skills on or off: uah's enabled key in Codex's [skills]
	// table.
	Skills Skills `toml:"skills"`
	// Features turns features on or off, in Codex's [features] table.
	Features Features `toml:"features"`
	// Goals configures /goal, in Codex's [goals] table.
	Goals Goals `toml:"goals"`
	// ModelVerbosity is low, medium, or high: the Responses API's
	// text.verbosity in place of the model's default, for a model that
	// supports verbosity, as Codex's key.
	ModelVerbosity string `toml:"model_verbosity"`

	// AutoCompactPercent compacts the context once a response used this
	// share of the model's window (default 90; 0 turns it off).
	AutoCompactPercent *int `toml:"auto_compact_percent"`
	// ModelContextWindow overrides the model's context window in tokens, as
	// Codex's key does.
	ModelContextWindow int64 `toml:"model_context_window"`
	// Compaction keys with Codex's names: the automatic limit in tokens
	// (the lower of it and auto_compact_percent applies) and the summary
	// prompt, inline or from a file (compact_prompt wins).
	ModelAutoCompactTokenLimit    int64  `toml:"model_auto_compact_token_limit"`
	CompactPrompt                 string `toml:"compact_prompt"`
	ExperimentalCompactPromptFile string `toml:"experimental_compact_prompt_file"`
	// The summary call's model and effort (the session's by default), and
	// the cap on user messages a compaction keeps (Codex's 20,000 tokens).
	CompactModel                string `toml:"compact_model"`
	CompactEffort               string `toml:"compact_effort"`
	CompactUserMessageMaxTokens int    `toml:"compact_user_message_max_tokens"`
	// CompactElideAfterCalls elides tool outputs this many calls old before
	// an automatic summary (10 by default; 0 turns it off).
	CompactElideAfterCalls *int `toml:"compact_elide_after_calls"`
	// CompactKeepRecentCalls leaves the last tool calls verbatim after a
	// summary (5 by default; 0 summarizes everything, as Codex).
	CompactKeepRecentCalls *int `toml:"compact_keep_recent_calls"`
	// RemoteCompaction compacts on openai and openai-codex into the
	// provider's encrypted item, as Codex does (true by default).
	RemoteCompaction *bool `toml:"remote_compaction"`
	// ReviewModel is /review's model, as Codex's key (the session's by
	// default).
	ReviewModel string `toml:"review_model"`

	// ModelInstructionsFile is a file whose text replaces the base
	// instructions, uah's default prompt, as Codex's key does. A
	// relative path is relative to the file that sets it (Load resolves it).
	ModelInstructionsFile string `toml:"model_instructions_file"`

	Instructions Instructions `toml:"instructions"`
	// Codex's AGENTS.md keys: fallback file names after AGENTS.md (none by
	// default), project root markers (nil: .git; empty: no walking up), and
	// the size cap (the same as [instructions] max_bytes).
	ProjectDocFallbackFilenames []string  `toml:"project_doc_fallback_filenames"`
	ProjectRootMarkers          *[]string `toml:"project_root_markers"`
	ProjectDocMaxBytes          int       `toml:"project_doc_max_bytes"`
	TUI                         TUI       `toml:"tui"`
	// History is the prompt history file, with Codex's [history] keys.
	History History `toml:"history"`
	// Hooks are keyed by event name: [[hooks.PreToolUse]].
	Hooks map[string][]Hook `toml:"hooks"`
	// MCPServers are keyed by server name, in Codex's format.
	MCPServers map[string]mcp.ServerConfig `toml:"mcp_servers"`
	// Codex's MCP OAuth keys: where logins are kept (auto, file, or
	// keyring) and the callback `uah mcp login` listens on.
	MCPOAuthCredentialsStore string `toml:"mcp_oauth_credentials_store"`
	MCPOAuthCallbackPort     int    `toml:"mcp_oauth_callback_port"`
	MCPOAuthCallbackURL      string `toml:"mcp_oauth_callback_url"`
	// Agents configures subagents, as Codex's [agents].
	Agents Agents `toml:"agents"`

	// Projects are keyed by absolute workspace path.
	Projects map[string]Project `toml:"projects"`
}

// Tools is Codex's [tools] table.
type Tools struct {
	// ExperimentalRequestUserInput is the agent's question tool
	// (request_user_input), as Codex's key: on by default, where a user can
	// answer it.
	ExperimentalRequestUserInput ToolToggle `toml:"experimental_request_user_input"`
	// Allow are the only tools the model may use (internal/toolpolicy):
	// unset allows every tool, and an empty list none. Each file's list
	// narrows the others', so no layer widens another's.
	Allow *[]string `toml:"allow"`
	// Deny are tools the model may never use; every file's add up.
	Deny []string `toml:"deny"`
	// within are the files' allowlists Allow was narrowed from
	// (toolpolicy.Policy.Within), set by merge.
	within [][]string
}

// ToolPolicy is the [tools] allow and deny lists as a policy.
func (c Config) ToolPolicy() toolpolicy.Policy { return c.Tools.policy() }

func (t Tools) policy() toolpolicy.Policy {
	return toolpolicy.Policy{Allow: listOf(t.Allow), Deny: t.Deny, Within: t.within}
}

// ToolToggle is Codex's { enabled = … } for one tool.
type ToolToggle struct {
	Enabled *bool `toml:"enabled"`
}

// RequestUserInputEnabled reports whether the agent may ask the user
// questions with request_user_input (true by default).
func (c Config) RequestUserInputEnabled() bool {
	e := c.Tools.ExperimentalRequestUserInput.Enabled

	return e == nil || *e
}

// Skills is the [skills] table.
type Skills struct {
	// Enabled discovers skills and offers them through SkillUse (true by
	// default); off, the prompt lists none.
	Enabled *bool `toml:"enabled"`
}

// SkillsEnabled reports whether skills are on (true by default).
func (c Config) SkillsEnabled() bool { return c.Skills.Enabled == nil || *c.Skills.Enabled }

// Features is Codex's [features] table, with the one feature uah reads.
type Features struct {
	// Goals is /goal and the goal tools (true by default, as Codex's).
	Goals *bool `toml:"goals"`
}

// Goals is Codex's [goals] table, with uah's cap on continuations.
type Goals struct {
	// MaxGoalTokenBudget caps a goal's token budget and is the budget of a
	// goal that names none, as Codex's key (0: none).
	MaxGoalTokenBudget int64 `toml:"max_goal_token_budget"`
	// MaxContinuations caps the runs uah starts on its own for one goal
	// (goal.DefaultMaxContinuations when unset; 0: no limit). uah's.
	MaxContinuations *int `toml:"max_continuations"`
}

// GoalsEnabled reports whether /goal and the goal tools are on (true by
// default).
func (c Config) GoalsEnabled() bool { return c.Features.Goals == nil || *c.Features.Goals }

// Context configures context preparation's modules ([context]).
type Context struct {
	// Modules are module ids to turn on that ship turned off, such as the
	// library's "go" (internal/contextprep).
	Modules []string `toml:"modules"`
}

// Agents configures subagents with Codex's [agents] keys. Unset values
// take the defaults: enabled, 4 open agents per session, depth 1, and the
// parent's model and effort.
type Agents struct {
	Enabled                        *bool `toml:"enabled"`
	MaxConcurrentThreadsPerSession *int  `toml:"max_concurrent_threads_per_session"`
	// MaxThreads is Codex's older name for the same limit.
	MaxThreads                     *int   `toml:"max_threads"`
	MaxDepth                       *int   `toml:"max_depth"`
	DefaultSubagentModel           string `toml:"default_subagent_model"`
	DefaultSubagentReasoningEffort string `toml:"default_subagent_reasoning_effort"`
}

// MaxThreadsValue is the concurrency limit under either name, or nil.
func (a Agents) MaxThreadsValue() *int {
	if a.MaxConcurrentThreadsPerSession != nil {
		return a.MaxConcurrentThreadsPerSession
	}

	return a.MaxThreads
}

// AgentsDir holds the user's agent role files (*.toml), as Codex's.
func AgentsDir() string { return filepath.Join(Dir(), "agents") }

// ProjectAgentsDir holds a trusted workspace's agent role files.
func ProjectAgentsDir(workspace string) string {
	return filepath.Join(ProjectDir(workspace), "agents")
}

// Instructions configure instruction files.
type Instructions struct {
	// Enabled defaults to true.
	Enabled  *bool `toml:"enabled"`
	MaxBytes int   `toml:"max_bytes"`
}

// Hook is one [[hooks.<Event>]] entry.
type Hook struct {
	Matcher string `toml:"matcher"`
	Command string `toml:"command"`
	Timeout string `toml:"timeout"`
	// Source is the file kind it came from, set by Load.
	Source hooks.Source `toml:"-"`
}

// HookList converts the configured hooks, checking events and timeouts.
func (c Config) HookList() ([]hooks.Hook, error) {
	var out []hooks.Hook
	for _, event := range hooks.Events {
		for _, h := range c.Hooks[string(event)] {
			var timeout time.Duration
			if h.Timeout != "" {
				d, err := time.ParseDuration(h.Timeout)
				if err != nil || d <= 0 {
					return nil, fmt.Errorf("invalid %s hook timeout %q", event, h.Timeout)
				}
				timeout = d
			}
			out = append(out, hooks.Hook{Event: event, Matcher: h.Matcher, Command: h.Command, Timeout: timeout, Source: h.Source})
		}
	}
	for name := range c.Hooks {
		if !slices.Contains(hooks.Events, hooks.Event(name)) {
			return nil, fmt.Errorf("unknown hook event %q (want one of %v)", name, hooks.Events)
		}
	}

	return out, nil
}

// SandboxWorkspaceWrite configures the workspace-write sandbox, as Codex's
// [sandbox_workspace_write] does.
type SandboxWorkspaceWrite struct {
	// NetworkAccess lets sandboxed commands use the network.
	NetworkAccess bool `toml:"network_access"`
	// WritableRoots are extra writable directories; ~ is the home directory,
	// and relative paths are relative to the workspace.
	WritableRoots []string `toml:"writable_roots"`
}

// ShellEnvironmentPolicy is Codex's [shell_environment_policy]. Empty, it
// passes the whole environment to commands, as Codex does.
type ShellEnvironmentPolicy struct {
	Inherit               string            `toml:"inherit"` // all, core, or none
	IgnoreDefaultExcludes *bool             `toml:"ignore_default_excludes"`
	Exclude               []string          `toml:"exclude"`
	IncludeOnly           []string          `toml:"include_only"`
	Set                   map[string]string `toml:"set"`
}

// Approvals are simple command rules: each entry is a command prefix, such
// as "git status". Allowed commands run outside the sandbox without asking;
// forbidden ones never run.
type Approvals struct {
	Allow  []string `toml:"allow"`
	Forbid []string `toml:"forbid"`
}

// Review configures the auto-reviewer. Empty fields take the defaults:
// codex-auto-review on openai-codex (else the session model), low effort,
// a 90s timeout, and Codex's review policy.
type Review struct {
	Model   string `toml:"model"`
	Effort  string `toml:"effort"`
	Timeout string `toml:"timeout"`
	// PolicyFile is a file whose text replaces the review policy, as
	// Codex's [auto_review] policy does inline.
	PolicyFile string `toml:"policy_file"`
}

// TUI configures the terminal UI.
type TUI struct {
	// Details starts in the detailed view (ctrl+t toggles it).
	Details bool `toml:"details"`
	// Mouse reports the mouse to the TUI (on when unset, MouseOn), so the
	// wheel scrolls the transcript and a drag selects and copies its text.
	// Off, the terminal selects text and turns the wheel into ↑ and ↓.
	Mouse *bool `toml:"mouse"`
	// Title shows the session's state in the terminal's title (on when
	// unset, TitleOn).
	Title *bool `toml:"title"`
	// FileLinks is what a click on a file path in the transcript does
	// (FileLinksMode): "peek" (the default) shows the file in an overlay,
	// "editor" opens it in $VISUAL or $EDITOR at its line, "open" with the
	// system's default app, and "off" draws paths as plain text.
	FileLinks string `toml:"file_links"`
}

// History configures <home>/history.jsonl, the prompts ↑ and ctrl+r
// recall, with Codex's [history] keys: persistence ("save-all", the
// default, or "none", which stops writing but still reads the file) and
// max_bytes (the cap; the oldest prompts go first; 0 is no cap).
type History struct {
	Persistence string `toml:"persistence"`
	MaxBytes    *int64 `toml:"max_bytes"`
}

// MouseOn reports whether the TUI reports the mouse: on unless set false.
func (t TUI) MouseOn() bool { return t.Mouse == nil || *t.Mouse }

// The values of [tui] file_links.
const (
	FileLinksPeek   = "peek"
	FileLinksEditor = "editor"
	FileLinksOpen   = "open"
	FileLinksOff    = "off"
)

// FileLinksModes are file_links' values, the default first.
var FileLinksModes = []string{FileLinksPeek, FileLinksEditor, FileLinksOpen, FileLinksOff}

// FileLinksMode is what a click on a file path does: file_links, or peek
// when unset.
func (t TUI) FileLinksMode() string {
	if t.FileLinks == "" {
		return FileLinksPeek
	}

	return t.FileLinks
}

// TitleOn reports whether the TUI sets the terminal's title and progress:
// on unless set false.
func (t TUI) TitleOn() bool { return t.Title == nil || *t.Title }

// Project is per-workspace configuration from the user file.
type Project struct {
	// Trusted allows <workspace>/.uah/config.toml to apply.
	Trusted bool `toml:"trusted"`
}

// InstructionOptions are the AGENTS.md discovery settings and the size cap
// (project_doc_max_bytes wins over [instructions] max_bytes).
func (c Config) InstructionOptions() (fallbacks []string, markers []string, maxBytes int) {
	if c.ProjectRootMarkers != nil {
		markers = *c.ProjectRootMarkers
		if markers == nil {
			markers = []string{}
		}
	}
	maxBytes = c.Instructions.MaxBytes
	if c.ProjectDocMaxBytes != 0 {
		maxBytes = c.ProjectDocMaxBytes
	}

	return c.ProjectDocFallbackFilenames, markers, maxBytes
}

// ContextPreparationEnabled reports whether new sessions start with
// prepared context.
func (c Config) ContextPreparationEnabled() bool {
	return c.ContextPreparation == nil || *c.ContextPreparation
}

// InstructionsEnabled reports whether instruction files should be loaded.
func (c Config) InstructionsEnabled() bool {
	return c.Instructions.Enabled == nil || *c.Instructions.Enabled
}

// Dir holds the user's files: uah's home, ~/.uah or $UAH_HOME.
func Dir() string { return home.Dir() }

// UserFile is the user configuration file in Dir.
func UserFile() string { return filepath.Join(Dir(), "config.toml") }

// RulesDir holds the user's command rules files (*.rules).
func RulesDir() string { return filepath.Join(Dir(), "rules") }

// ProjectRulesDir holds a trusted workspace's command rules files.
func ProjectRulesDir(workspace string) string {
	return filepath.Join(ProjectDir(workspace), "rules")
}

// ProjectDir holds a workspace's project files: <workspace>/.uah.
func ProjectDir(workspace string) string { return filepath.Join(workspace, home.Name) }

// ProjectFile is a workspace's project configuration file.
func ProjectFile(workspace string) string {
	return filepath.Join(ProjectDir(workspace), "config.toml")
}
