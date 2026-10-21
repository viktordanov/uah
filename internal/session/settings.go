package session

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/approval"
)

// Efforts are the thinking levels the runner accepts.
var Efforts = []string{"low", "medium", "high", "xhigh", "max", "ultra"}

// Adaptive effort's values: how many effort levels below the session's a
// follow-up request after tool results goes. On, a new main session also
// starts with the workspace's context.
const (
	AdaptiveOff      = "off"
	AdaptiveOneStep  = "1-step"
	AdaptiveTwoSteps = "2-steps"
)

// AdaptiveEfforts are adaptive effort's values, in /config's cycle order.
var AdaptiveEfforts = []string{AdaptiveOff, AdaptiveOneStep, AdaptiveTwoSteps}

// AdaptiveSteps is the number of effort levels a follow-up goes down: 0
// when adaptive effort is off ("" or "off").
func AdaptiveSteps(value string) int {
	switch value {
	case AdaptiveOneStep:
		return 1
	case AdaptiveTwoSteps:
		return 2
	}

	return 0
}

// FollowUpEffort is the effort a follow-up after tool results goes at with
// adaptive effort value: steps levels below effort in Efforts, never below
// low (ultra is the level above max).
func FollowUpEffort(effort, value string) string {
	i := slices.Index(Efforts, effort)
	if i < 0 {
		return effort
	}

	return Efforts[max(i-AdaptiveSteps(value), 0)]
}

// Providers are the backends the runner supports.
var Providers = []string{"openai", "openai-codex", "openrouter", "fireworks", "ollama"}

// Settings are what a session sends with every run. Changing them applies
// live when the engine supports it, and otherwise from the next run.
type Settings struct {
	Provider    string
	Model       string
	Effort      string
	ServiceTier string // "" or "priority"
	// AdaptiveEffort is AdaptiveOff, AdaptiveOneStep, or AdaptiveTwoSteps
	// ("": off).
	AdaptiveEffort string
	Workspace      string
	BaseURL        string
	AllowDotenv    bool
	// AllowStateInWorkspace runs when the workspace holds the state
	// directory, with a warning (block_state_in_workspace off).
	AllowStateInWorkspace bool
	// MaxAttempts is how many times a model request is sent before the
	// run fails (0: the engine's default).
	MaxAttempts int
	// SystemPrompt replaces the runner's host prompt when set; empty,
	// the engine uses uah's default prompt.
	SystemPrompt string
	// Mode is the permission mode: the sandbox commands run in and who
	// decides what needs approval ("": the engine's configured sandbox).
	// Change it with WithMode, which keeps Sandbox in step.
	Mode approval.Mode
	// Sandbox is Mode's sandbox mode, for display.
	Sandbox string
	// ContextWindow overrides the model table's context window (tokens), for
	// the context meter; the engine was built with the same value.
	ContextWindow int64
}

// Validate checks values without asking the provider which models exist.
func (s Settings) Validate() error {
	if !slices.Contains(Providers, s.Provider) {
		return fmt.Errorf("invalid provider %q (want %s)", s.Provider, strings.Join(Providers, ", "))
	}
	if s.Effort != "" && !slices.Contains(Efforts, s.Effort) {
		return fmt.Errorf("invalid effort %q (want %s)", s.Effort, strings.Join(Efforts, ", "))
	}
	if err := validateModel(s.Model); err != nil {
		return err
	}
	if s.ServiceTier != "" && s.ServiceTier != "priority" {
		return fmt.Errorf("invalid service tier %q (want priority or empty)", s.ServiceTier)
	}
	if s.AdaptiveEffort != "" && !slices.Contains(AdaptiveEfforts, s.AdaptiveEffort) {
		return fmt.Errorf("invalid adaptive effort %q (want off, 1-step, or 2-steps)", s.AdaptiveEffort)
	}
	if s.Workspace == "" {
		return errors.New("the workspace is not set")
	}
	if s.Mode != "" {
		if _, err := approval.ParseMode(string(s.Mode)); err != nil {
			return err
		}
	}

	return nil
}

// WithMode returns the settings with the permission mode m and its sandbox
// mode.
func (s Settings) WithMode(m approval.Mode) Settings {
	s.Mode, s.Sandbox = m, string(m.Sandbox())

	return s
}

// validateModel checks syntax only; the provider decides which models exist.
func validateModel(model string) error {
	if strings.HasPrefix(model, "-") {
		return fmt.Errorf("invalid model %q: it starts with a dash", model)
	}
	if strings.IndexFunc(model, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) >= 0 {
		return fmt.Errorf("invalid model %q: it contains spaces or control characters", model)
	}

	return nil
}

// request builds the runner request for one run of the session.
func (s Settings) request(sessionID string, messages []core.UserInput) core.Request {
	return core.Request{
		SessionID:    sessionID,
		Messages:     messages,
		Provider:     s.Provider,
		Model:        s.Model,
		Effort:       s.Effort,
		BaseURL:      s.BaseURL,
		SystemPrompt: s.SystemPrompt,
		Workspace:    s.Workspace,
		AllowDotenv:  s.AllowDotenv,
		MaxAttempts:  s.MaxAttempts,

		AllowStateInWorkspace: s.AllowStateInWorkspace,
	}
}

// WithRequest returns the settings with the fields a run's request carries
// taken from req: the inverse of request, so a subagent starts from its
// parent's run as it is.
func (s Settings) WithRequest(req core.Request) Settings {
	s.Provider, s.Model, s.Effort, s.BaseURL = req.Provider, req.Model, req.Effort, req.BaseURL
	s.SystemPrompt, s.Workspace, s.AllowDotenv = req.SystemPrompt, req.Workspace, req.AllowDotenv
	s.MaxAttempts, s.AllowStateInWorkspace = req.MaxAttempts, req.AllowStateInWorkspace

	return s
}
