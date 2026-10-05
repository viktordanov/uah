// Package app turns what the CLI collected into a session ready to open:
// Resolve decides the settings without I/O, and Setup loads the files and
// builds the engine around them.
package app

import (
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"

	"github.com/viktordanov/uah/internal/approval"
	"github.com/viktordanov/uah/internal/compaction"
	"github.com/viktordanov/uah/internal/config"
	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/engine/embedded"
	"github.com/viktordanov/uah/internal/goal"
	"github.com/viktordanov/uah/internal/review"
	"github.com/viktordanov/uah/internal/rules"
	"github.com/viktordanov/uah/internal/sandbox"
	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/internal/toolpolicy"
)

// Defaults when neither a flag, the resumed session, nor the configuration
// sets a value.
const (
	CodexProvider = "openai-codex"
	DefaultEffort = "high"
)

// LogLevels are the names --log-level accepts.
var LogLevels = map[string]slog.Level{"debug": slog.LevelDebug, "info": slog.LevelInfo, "warn": slog.LevelWarn, "error": slog.LevelError}

// Inputs are the values the CLI collected, flags already folded with the
// environment. An empty string means not set; the Set fields say whether a
// flag with a default value was given.
type Inputs struct {
	ConfigPath string
	StateDir   string
	SessionRef string // a session to resume, by ID or unique prefix
	// NewSessionID is --session-id: the ID of a new session (a UUID).
	NewSessionID string
	LogLevel     string

	Provider  string
	Model     string
	Effort    string
	Workspace string
	BaseURL   string

	MaxDisk    string
	MaxDiskSet bool
	// MaxAttempts is --max-attempts or its environment variable (0: unset).
	MaxAttempts int
	Fast        bool
	FastSet     bool
	// AdaptiveEffort is --adaptive-effort or its environment variable
	// ("": unset).
	AdaptiveEffort string
	// ContextPreparation is on or off: off from --no-context-preparation,
	// else its environment variable ("": unset).
	ContextPreparation string
	// EffortUpdates is UAH_EFFORT_UPDATES: off turns effort updates off, a
	// switch for tests and A/B runs ("": unset, on).
	EffortUpdates string
	// ModelVerbosity is --model-verbosity or its environment variable
	// ("": unset).
	ModelVerbosity string
	// Sandbox is the --sandbox mode.
	Sandbox string
	// Ask is the --ask approval policy.
	Ask string
	// Yolo is --yolo: yolo mode, and yolo in the shift+tab cycle.
	Yolo bool

	AllowDotenv    bool
	NoInstructions bool

	// RunStateDir, when set, takes the session's files and run records in
	// place of StateDir: `uah exec --ephemeral` passes a temporary directory.
	RunStateDir string
	// Interactive is a session a user drives, the TUI's: the main agent is
	// offered request_user_input, whose questions the user answers.
	Interactive bool
	// RequestUserInput is UAH_REQUEST_USER_INPUT, on or off ("": unset).
	RequestUserInput string
	// Tools is --tools, the only tools the model may use (nil: unset; an
	// empty list allows none), and DenyTools is --deny-tools. They narrow
	// [tools] allow and deny, never widen them.
	Tools     []string
	DenyTools []string
	// NoSkills is --no-skills: no skill is discovered or offered.
	NoSkills bool
}

// Resolved is what Resolve decides.
type Resolved struct {
	// Settings has no SystemPrompt yet; Setup adds it from the instructions.
	Settings session.Settings
	MaxDisk  int64
	// Instructions reports whether to load AGENTS.md and CLAUDE.md files.
	Instructions bool
	// ContextPreparation reports whether new sessions start with prepared
	// context.
	ContextPreparation bool
	// RequestUserInput reports whether the agent may ask the user with
	// request_user_input where a user can answer
	// ([tools.experimental_request_user_input] enabled, or
	// UAH_REQUEST_USER_INPUT). Off, the default prompt does not name it.
	RequestUserInput bool
	// EffortUpdates reports whether effort changes go as configuration
	// updates where the model takes them (UAH_EFFORT_UPDATES).
	EffortUpdates bool
	// Sandbox is the policy commands run under. Its Workspace and
	// WritableRoots are as given; Setup makes them absolute.
	Sandbox sandbox.Policy
	// Env is which environment variables commands get.
	Env sandbox.EnvPolicy
	// Compaction is when the embedded engine compacts and how it
	// summarizes. Its Prompt is compact_prompt; Setup reads
	// CompactPromptFile into it when that is empty.
	Compaction        compaction.Settings
	CompactPromptFile string
	// Approval is when the user is asked to approve a command.
	Approval approval.Policy
	// Rules are the configured [approvals] prefixes; Setup adds the rules
	// files.
	Rules []rules.Rule
	// ApprovalsReviewer is auto_review or user.
	ApprovalsReviewer string
	// Review is the auto-reviewer's model, effort, and timeout.
	Review review.Config
	// Agents are the subagent settings.
	Agents Agents
	// WebSearch is live or disabled; the engine offers live search only
	// on a provider that has it.
	WebSearch string
	// Verbosity is model_verbosity ("": each model's default).
	Verbosity string
	// DefaultModel reports that no flag, resumed session, or file named the
	// model on openai-codex or openai: Settings.Model is then the provider's
	// fallback until SettleModel sees the provider's list.
	DefaultModel bool
	// Goals are the [goals] settings, for /goal and the goal tools.
	Goals goal.Settings
	// Tools is the tool policy: [tools] allow and deny of every
	// configuration file, the resumed session's, and --tools and
	// --deny-tools, each narrowing the others (pickToolPolicy).
	Tools toolpolicy.Policy
	// NoSkills turns skills off: none is discovered or offered
	// (--no-skills, or [skills] enabled = false).
	NoSkills bool
}

// UsageError is an error in what the user asked for, such as an invalid
// flag or configuration value.
type UsageError struct{ Err error }

func (e *UsageError) Error() string { return e.Err.Error() }

func (e *UsageError) Unwrap() error { return e.Err }

func usage(err error) error { return &UsageError{Err: err} }

// Resolve combines the inputs, the resumed session (the zero Info for a new
// one), and the configuration, in that order, then the defaults. It does no
// I/O.
func Resolve(in Inputs, resumed session.Info, cfg config.Config) (Resolved, error) {
	s := session.Settings{
		Provider:    first(in.Provider, resumed.Provider, cfg.Provider, CodexProvider),
		Effort:      first(in.Effort, resumed.Effort, cfg.Effort, DefaultEffort),
		Workspace:   workspaceFor(in, resumed),
		BaseURL:     in.BaseURL,
		AllowDotenv: in.AllowDotenv,
		// The flag, the resumed session's, or the configured default.
		AdaptiveEffort: first(in.AdaptiveEffort, resumed.AdaptiveEffort, cfg.AdaptiveEffort, session.AdaptiveOff),
	}
	s.Model = pickModel(in, resumed, cfg)
	defaulted := s.Model == "" && fallbackModels[s.Provider] != ""
	if defaulted {
		s.Model = fallbackModels[s.Provider]
	}
	var err error
	if s.MaxAttempts, err = pickMaxAttempts(in, cfg); err != nil {
		return Resolved{}, err
	}
	if pickFast(in, resumed, cfg) {
		if !embedded.Priority(s.Provider) {
			return Resolved{}, usage(errors.New("--fast needs the openai or openai-codex provider"))
		}
		s.ServiceTier = "priority"
	}
	if err := s.Validate(); err != nil {
		return Resolved{}, usage(err)
	}
	maxDisk, err := pickMaxDisk(in, cfg)
	if err != nil {
		return Resolved{}, err
	}
	mode, policy, err := pickMode(in, resumed, cfg, s.Workspace)
	if err != nil {
		return Resolved{}, err
	}
	s = s.WithMode(mode)

	envPolicy, err := pickEnv(cfg.ShellEnvironmentPolicy)
	if err != nil {
		return Resolved{}, err
	}
	compact, promptFile, err := pickCompaction(cfg, &s)
	if err != nil {
		return Resolved{}, err
	}
	approvalPolicy, configured, err := pickApprovals(in, cfg)
	if err != nil {
		return Resolved{}, err
	}
	reviewer, reviewCfg, err := pickReview(cfg, s)
	if err != nil {
		return Resolved{}, err
	}
	agentSettings, err := pickAgents(cfg)
	if err != nil {
		return Resolved{}, err
	}
	webSearch, err := pickWebSearch(cfg)
	if err != nil {
		return Resolved{}, err
	}
	prepare, err := pickContextPreparation(in, cfg)
	if err != nil {
		return Resolved{}, err
	}
	verbosity, err := pickVerbosity(in, cfg)
	if err != nil {
		return Resolved{}, err
	}
	updates, err := pickOnOff(in.EffortUpdates, EnvEffortUpdates, true)
	if err != nil {
		return Resolved{}, err
	}
	tools, err := pickTools(in, resumed, cfg)
	if err != nil {
		return Resolved{}, err
	}

	return Resolved{
		Settings: s, MaxDisk: maxDisk, Instructions: !in.NoInstructions && cfg.InstructionsEnabled(), ContextPreparation: prepare, EffortUpdates: updates, RequestUserInput: tools.questions,
		Sandbox: policy, Env: envPolicy, Compaction: compact, CompactPromptFile: promptFile, Approval: approvalPolicy, Rules: configured,
		ApprovalsReviewer: reviewer, Review: reviewCfg, Agents: agentSettings, WebSearch: webSearch, Verbosity: verbosity, DefaultModel: defaulted,
		Goals: tools.goals, Tools: tools.policy, NoSkills: tools.noSkills,
	}, nil
}

// pickToolPolicy narrows [tools] allow and deny with the resumed session's
// policy, then with --tools and --deny-tools: every source can only take
// tools away.
func pickToolPolicy(in Inputs, resumed session.Info, cfg config.Config) (toolpolicy.Policy, error) {
	p := cfg.ToolPolicy()
	if resumed.Tools != nil {
		p = p.Narrow(*resumed.Tools)
	}
	if err := toolpolicy.Validate(append(slices.Clone(in.Tools), in.DenyTools...)); err != nil {
		return toolpolicy.Policy{}, usage(fmt.Errorf("--tools or --deny-tools: %w", err))
	}

	return p.Narrow(toolpolicy.Policy{Allow: in.Tools, Deny: in.DenyTools}), nil
}

// toolSettings are what pickTools decides.
type toolSettings struct {
	questions bool
	goals     goal.Settings
	policy    toolpolicy.Policy
	noSkills  bool
}

// pickTools is whether the question tool is on, the goal settings, the
// tool policy, and whether skills are off. The default prompt names
// request_user_input only when the policy lets the agent have it.
func pickTools(in Inputs, resumed session.Info, cfg config.Config) (toolSettings, error) {
	questions, err := pickOnOff(in.RequestUserInput, EnvRequestUserInput, cfg.RequestUserInputEnabled())
	if err != nil {
		return toolSettings{}, err
	}
	goals, err := pickGoals(cfg)
	if err != nil {
		return toolSettings{}, err
	}
	policy, err := pickToolPolicy(in, resumed, cfg)
	if err != nil {
		return toolSettings{}, err
	}

	return toolSettings{
		questions: questions && policy.Allows(engine.QuestionToolName), goals: goals, policy: policy,
		noSkills: in.NoSkills || !cfg.SkillsEnabled(),
	}, nil
}

// pickGoals is [features] goals and the [goals] table, with uah's default
// cap on continuations.
func pickGoals(cfg config.Config) (goal.Settings, error) {
	g := goal.Settings{Disabled: !cfg.GoalsEnabled(), MaxTokenBudget: cfg.Goals.MaxGoalTokenBudget, MaxContinuations: goal.DefaultMaxContinuations}
	if g.MaxTokenBudget < 0 {
		return goal.Settings{}, usage(fmt.Errorf("invalid [goals] max_goal_token_budget %d (want a positive number, or 0 for none)", g.MaxTokenBudget))
	}
	if m := cfg.Goals.MaxContinuations; m != nil {
		if *m < 0 {
			return goal.Settings{}, usage(fmt.Errorf("invalid [goals] max_continuations %d (want a positive number, or 0 for no limit)", *m))
		}
		g.MaxContinuations = *m
	}

	return g, nil
}

// pickContextPreparation is the flag or its variable, on or off, else
// context_preparation.
func pickContextPreparation(in Inputs, cfg config.Config) (bool, error) {
	return pickOnOff(in.ContextPreparation, EnvContextPreparation, cfg.ContextPreparationEnabled())
}

// pickOnOff is value, on or off from the variable env, else configured.
func pickOnOff(value, env string, configured bool) (bool, error) {
	switch value {
	case "":
		return configured, nil
	case "on":
		return true, nil
	case "off":
		return false, nil
	}

	return false, usage(fmt.Errorf("invalid %s %q (want on or off)", env, value))
}

// pickEnv checks the configured environment policy.
func pickEnv(c config.ShellEnvironmentPolicy) (sandbox.EnvPolicy, error) {
	switch c.Inherit {
	case "", sandbox.InheritAll, sandbox.InheritCore, sandbox.InheritNone:
	default:
		return sandbox.EnvPolicy{}, usage(fmt.Errorf("invalid shell_environment_policy.inherit %q (want all, core, or none)", c.Inherit))
	}

	return sandbox.EnvPolicy{
		Inherit: c.Inherit, IgnoreDefaultExcludes: c.IgnoreDefaultExcludes,
		Exclude: c.Exclude, IncludeOnly: c.IncludeOnly, Set: c.Set,
	}, nil
}

// pickModel is the named model, empty when nothing names one. A
// provider flag that changes the provider drops the resumed and configured
// models, which belong to the other provider.
func pickModel(in Inputs, resumed session.Info, cfg config.Config) string {
	if !providerChanged(in, resumed, cfg) {
		return first(in.Model, resumed.Model, cfg.Model)
	}

	return in.Model
}

// providerChanged reports whether the provider flag picks another provider
// than the one that would apply without it: the resumed session's, the
// configured one, or the default.
func providerChanged(in Inputs, resumed session.Info, cfg config.Config) bool {
	return in.Provider != "" && in.Provider != first(resumed.Provider, cfg.Provider, CodexProvider)
}

// pickMaxAttempts is the max-attempts flag or its environment variable,
// else request_max_attempts, else uah's default.
func pickMaxAttempts(in Inputs, cfg config.Config) (int, error) {
	switch {
	case in.MaxAttempts < 0:
		return 0, usage(fmt.Errorf("invalid --max-attempts %d (want 1 or more)", in.MaxAttempts))
	case in.MaxAttempts > 0:
		return in.MaxAttempts, nil
	case cfg.RequestMaxAttempts < 0:
		return 0, usage(fmt.Errorf("invalid request_max_attempts %d (want 1 or more)", cfg.RequestMaxAttempts))
	case cfg.RequestMaxAttempts > 0:
		return cfg.RequestMaxAttempts, nil
	}

	return engine.DefaultMaxAttempts, nil
}

// pickMaxDisk is the max-disk flag when given, else the configured limit,
// else the flag's default.
func pickMaxDisk(in Inputs, cfg config.Config) (int64, error) {
	text := in.MaxDisk
	if !in.MaxDiskSet && cfg.MaxDisk != "" {
		text = cfg.MaxDisk
	}
	n, err := ParseSize(text)
	if err != nil {
		return 0, usage(errors.New("max_disk: " + err.Error()))
	}

	return n, nil
}

// workspaceFor is the workspace flag, the resumed session's, or the current
// directory, possibly relative.
func workspaceFor(in Inputs, resumed session.Info) string {
	return first(in.Workspace, resumed.Workspace, ".")
}

// first returns the first non-empty value.
func first(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}

	return ""
}

// ParseSize parses sizes like 500M, 5G, or 1024 (bytes). "0" disables the limit.
func ParseSize(s string) (int64, error) {
	s = strings.ToUpper(strings.TrimSuffix(strings.TrimSpace(s), "B"))
	mult := int64(1)
	if n := len(s); n > 0 {
		switch s[n-1] {
		case 'K':
			mult, s = 1<<10, s[:n-1]
		case 'M':
			mult, s = 1<<20, s[:n-1]
		case 'G':
			mult, s = 1<<30, s[:n-1]
		}
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v < 0 {
		return 0, errors.New("invalid size " + strconv.Quote(s))
	}

	return int64(v * float64(mult)), nil
}
