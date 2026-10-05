package app

import (
	"context"
	"fmt"
	"slices"

	"github.com/viktordanov/uah/internal/agents"
	"github.com/viktordanov/uah/internal/config"
	"github.com/viktordanov/uah/internal/models"
	"github.com/viktordanov/uah/internal/session"
)

// Agents are the resolved [agents] settings.
type Agents struct {
	Enabled bool
	// MaxThreads is how many subagents a session keeps open at once.
	MaxThreads int
	// MaxDepth is how deep subagents nest, as configured; newAgents clamps
	// it to agents.MaxDepth (1: children cannot spawn).
	MaxDepth int
	// Model and Effort are the subagents' defaults ("": the parent's).
	Model  string
	Effort string
	// ReviewLimits bound each /review (review_time_limit and the others).
	ReviewLimits agents.ReviewLimits
}

// pickAgents checks the [agents] keys and applies Codex's defaults, and
// the /review limits (pickReviewLimits).
func pickAgents(cfg config.Config) (Agents, error) {
	limits, err := pickReviewLimits(cfg)
	if err != nil {
		return Agents{}, err
	}
	c := cfg.Agents
	a := Agents{
		Enabled: c.Enabled == nil || *c.Enabled, MaxThreads: agents.DefaultMaxThreads, MaxDepth: agents.DefaultMaxDepth,
		Model: c.DefaultSubagentModel, Effort: c.DefaultSubagentReasoningEffort, ReviewLimits: limits,
	}
	if n := c.MaxThreadsValue(); n != nil {
		if *n < 1 {
			return Agents{}, usage(fmt.Errorf("invalid agents.max_concurrent_threads_per_session %d (want 1 or more)", *n))
		}
		a.MaxThreads = *n
	}
	if c.MaxDepth != nil {
		if *c.MaxDepth < 0 {
			return Agents{}, usage(fmt.Errorf("invalid agents.max_depth %d (want 0 or more)", *c.MaxDepth))
		}
		a.MaxDepth = *c.MaxDepth
	}
	if a.Effort != "" && !slices.Contains(session.Efforts, a.Effort) {
		return Agents{}, usage(fmt.Errorf("invalid agents.default_subagent_reasoning_effort %q", a.Effort))
	}

	return a, nil
}

// newAgents builds the subagent manager with the user's and, in a trusted
// workspace, the project's role files. With agents off it offers no tools
// but still answers a resumed session's past calls. Role file warnings
// become notices.
func newAgents(r Resolved, cfg config.Config, workspace string, opts *session.Options, catalog *models.Manager) *agents.Manager {
	depth := r.Agents.MaxDepth
	if !r.Agents.Enabled {
		depth = 0
	}
	if depth > agents.MaxDepth { // subagents never start subagents
		opts.Notices = append(opts.Notices, fmt.Sprintf("agents.max_depth = %d is above %d: subagents never start subagents, so it is %d", depth, agents.MaxDepth, agents.MaxDepth))
		depth = agents.MaxDepth
	}
	var roles []agents.Role
	if depth > 0 {
		dirs := []string{config.AgentsDir()}
		if cfg.Projects[workspace].Trusted {
			dirs = append(dirs, config.ProjectAgentsDir(workspace))
		}
		var warnings []string
		roles, warnings = agents.LoadRoles(dirs...)
		opts.Notices = append(opts.Notices, warnings...)
	}

	// spawn_agent checks a model against the provider's live list, as Codex
	// checks it against its catalog; children run on the root's provider.
	provider := r.Settings.Provider
	validate := func(ctx context.Context, model string) error { return catalog.Validate(ctx, provider, model) }

	return agents.New(agents.Config{
		MaxThreads: r.Agents.MaxThreads, MaxDepth: depth, Model: r.Agents.Model, Effort: r.Agents.Effort,
		ReviewModel: cfg.ReviewModel, ReviewLimits: r.Agents.ReviewLimits, Roles: roles, Validate: validate,
	})
}
