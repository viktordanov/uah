package app_test

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/viktordanov/uah-core/harness/llm"

	"github.com/viktordanov/uah/internal/agents"
	"github.com/viktordanov/uah/internal/app"
	"github.com/viktordanov/uah/internal/approval"
	"github.com/viktordanov/uah/internal/compaction"
	"github.com/viktordanov/uah/internal/config"
	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/goal"
	"github.com/viktordanov/uah/internal/review"
	"github.com/viktordanov/uah/internal/rules"
	"github.com/viktordanov/uah/internal/sandbox"
	"github.com/viktordanov/uah/internal/session"
)

// flagDefaults are the inputs the CLI passes when no flag is given.
func flagDefaults() app.Inputs {
	return app.Inputs{Workspace: "/ws", MaxDisk: "5G"}
}

var defaultReviewLimits = agents.ReviewLimits{Time: agents.DefaultReviewTime, Tokens: agents.DefaultReviewTokens, Command: agents.DefaultReviewCommand}

func TestResolve(t *testing.T) {
	t.Parallel()
	resumed := session.Info{Provider: "openai", Model: "gpt-resumed", Effort: "low", Workspace: "/resumed"}
	configured := config.Config{Provider: "openrouter", Model: "cfg-model", Effort: "medium"}

	tests := []struct {
		name    string
		in      func(*app.Inputs)
		resumed session.Info
		cfg     config.Config
		want    func(*app.Resolved)
	}{
		{
			name: "defaults: codex, its model, high effort, embedded",
			want: func(r *app.Resolved) {},
		},
		{
			name: "the config file beats the defaults",
			cfg:  configured,
			want: func(r *app.Resolved) {
				r.Settings.Provider, r.Settings.Model, r.Settings.Effort = "openrouter", "cfg-model", "medium"
			},
		},
		{
			name:    "the resumed session beats the config file",
			resumed: resumed,
			cfg:     configured,
			want: func(r *app.Resolved) {
				r.Settings.Provider, r.Settings.Model, r.Settings.Effort = "openai", "gpt-resumed", "low"
			},
		},
		{
			name:    "flags beat the resumed session",
			in:      func(in *app.Inputs) { in.Provider, in.Model, in.Effort = "openai", "gpt-flag", "max" },
			resumed: resumed,
			cfg:     configured,
			want: func(r *app.Resolved) {
				r.Settings.Provider, r.Settings.Model, r.Settings.Effort = "openai", "gpt-flag", "max"
			},
		},
		{
			name:    "a provider change drops the resumed and configured models",
			in:      func(in *app.Inputs) { in.Provider = "ollama" },
			resumed: resumed,
			cfg:     configured,
			want: func(r *app.Resolved) {
				r.Settings.Provider, r.Settings.Model, r.Settings.Effort = "ollama", "", "low"
			},
		},
		{
			name:    "a provider change to codex gets the codex default model",
			in:      func(in *app.Inputs) { in.Provider = app.CodexProvider },
			resumed: resumed,
			want:    func(r *app.Resolved) { r.Settings.Effort = "low" },
		},
		{
			name:    "the same provider by flag keeps the resumed model",
			in:      func(in *app.Inputs) { in.Provider = "openai" },
			resumed: resumed,
			want: func(r *app.Resolved) {
				r.Settings.Provider, r.Settings.Model, r.Settings.Effort = "openai", "gpt-resumed", "low"
			},
		},
		{
			name: "openai falls back to the runner's default model until its list is seen",
			cfg:  config.Config{Provider: "openai"},
			want: func(r *app.Resolved) {
				r.Settings.Provider, r.Settings.Model = "openai", "gpt-6-astra"
				r.Review.Model = "gpt-6-astra"
			},
		},
		{
			name: "another provider has no default model",
			cfg:  config.Config{Provider: "openrouter"},
			want: func(r *app.Resolved) {
				r.Settings.Provider, r.Settings.Model = "openrouter", ""
				r.Review.Model = ""
			},
		},
		{
			name:    "the resumed workspace when no flag is given",
			in:      func(in *app.Inputs) { in.Workspace = "" },
			resumed: session.Info{Workspace: "/resumed"},
			want:    func(r *app.Resolved) { r.Settings.Workspace = "/resumed" },
		},
		{
			name: "the current directory when nothing sets the workspace",
			in:   func(in *app.Inputs) { in.Workspace = "" },
			want: func(r *app.Resolved) { r.Settings.Workspace = "." },
		},
		{
			name: "fast by flag",
			in:   func(in *app.Inputs) { in.Fast, in.FastSet = true, true },
			want: func(r *app.Resolved) { r.Settings.ServiceTier = "priority" },
		},
		{
			name: "fast from the config file",
			cfg:  config.Config{Fast: true},
			want: func(r *app.Resolved) { r.Settings.ServiceTier = "priority" },
		},
		{
			name: "--fast=false beats the config file",
			in:   func(in *app.Inputs) { in.FastSet = true },
			cfg:  config.Config{Fast: true},
			want: func(r *app.Resolved) {},
		},
		{
			name: "max disk from the config file",
			cfg:  config.Config{MaxDisk: "500M"},
			want: func(r *app.Resolved) { r.MaxDisk = 500 << 20 },
		},
		{
			name: "a max disk flag beats the config file",
			in:   func(in *app.Inputs) { in.MaxDisk, in.MaxDiskSet = "0", true },
			cfg:  config.Config{MaxDisk: "500M"},
			want: func(r *app.Resolved) { r.MaxDisk = 0 },
		},
		{
			name: "instructions off by flag",
			in:   func(in *app.Inputs) { in.NoInstructions = true },
			want: func(r *app.Resolved) { r.Instructions = false },
		},
		{
			name: "instructions off in the config file",
			cfg:  config.Config{Instructions: config.Instructions{Enabled: new(false)}},
			want: func(r *app.Resolved) { r.Instructions = false },
		},
		{
			name: "a provider flag equal to the configured provider keeps the configured model",
			in:   func(in *app.Inputs) { in.Provider = "openrouter" },
			cfg:  config.Config{Provider: "openrouter", Model: "cfg-model"},
			want: func(r *app.Resolved) {
				r.Settings.Provider, r.Settings.Model = "openrouter", "cfg-model"
				r.Review.Model = "cfg-model"
			},
		},
		{
			name: "the config file sets the sandbox",
			cfg: config.Config{SandboxMode: "read-only", SandboxWorkspaceWrite: config.SandboxWorkspaceWrite{
				NetworkAccess: true, WritableRoots: []string{"~/.cache"},
			}},
			want: func(r *app.Resolved) {
				r.Settings = r.Settings.WithMode(approval.ModeReadOnly)
				r.Sandbox = sandbox.Policy{Mode: sandbox.ReadOnly, WritableRoots: []string{"~/.cache"}, Network: true}
			},
		},
		{
			name: "--sandbox beats the config file",
			in:   func(in *app.Inputs) { in.Sandbox = "read-only" },
			cfg:  config.Config{PermissionMode: "auto"},
			want: func(r *app.Resolved) {
				r.Settings = r.Settings.WithMode(approval.ModeReadOnly)
				r.Sandbox.Mode = sandbox.ReadOnly
			},
		},
		{
			name: "--yolo is yolo mode with no sandbox",
			in:   func(in *app.Inputs) { in.Yolo = true },
			cfg:  config.Config{PermissionMode: "read-only"},
			want: func(r *app.Resolved) {
				r.Settings = r.Settings.WithMode(approval.ModeYolo)
				r.Sandbox.Mode = sandbox.FullAccess
			},
		},
		{
			name:    "a session resumed from yolo mode without --yolo takes the next mode",
			resumed: session.Info{Mode: approval.ModeYolo},
			cfg:     config.Config{PermissionMode: "auto"},
			want:    func(r *app.Resolved) { r.Settings = r.Settings.WithMode(approval.ModeAuto) },
		},
		{
			name:    "--yolo keeps a resumed session in yolo mode",
			in:      func(in *app.Inputs) { in.Yolo = true },
			resumed: session.Info{Mode: approval.ModeYolo},
			want: func(r *app.Resolved) {
				r.Settings = r.Settings.WithMode(approval.ModeYolo)
				r.Sandbox.Mode = sandbox.FullAccess
			},
		},
		{
			name: "permission_mode beats sandbox_mode",
			cfg:  config.Config{SandboxMode: "read-only", PermissionMode: "auto"},
			want: func(r *app.Resolved) { r.Settings = r.Settings.WithMode(approval.ModeAuto) },
		},
		{
			name:    "the resumed session's mode and fast mode beat the config file",
			resumed: session.Info{Provider: app.CodexProvider, Mode: approval.ModeReadOnly, Fast: new(false)},
			cfg:     config.Config{PermissionMode: "auto", Fast: true},
			want: func(r *app.Resolved) {
				r.Settings = r.Settings.WithMode(approval.ModeReadOnly)
				r.Sandbox.Mode = sandbox.ReadOnly
			},
		},
		{
			name:    "flags beat the resumed session's mode and fast mode",
			in:      func(in *app.Inputs) { in.Sandbox, in.Fast, in.FastSet = "workspace-write", true, true },
			resumed: session.Info{Provider: app.CodexProvider, Mode: approval.ModeReadOnly, Fast: new(false)},
			want:    func(r *app.Resolved) { r.Settings.ServiceTier = "priority" },
		},
		{
			name:    "a session without saved settings keeps the configured mode and fast mode",
			resumed: session.Info{Provider: app.CodexProvider},
			cfg:     config.Config{SandboxMode: "read-only", Fast: true},
			want: func(r *app.Resolved) {
				r.Settings = r.Settings.WithMode(approval.ModeReadOnly)
				r.Settings.ServiceTier = "priority"
				r.Sandbox.Mode = sandbox.ReadOnly
			},
		},
		{
			name:    "the session's fast mode stays with its provider",
			in:      func(in *app.Inputs) { in.Provider = "openai" },
			resumed: session.Info{Provider: app.CodexProvider, Fast: new(true)},
			want: func(r *app.Resolved) {
				r.Settings.Provider, r.Settings.Model = "openai", "gpt-6-astra"
				r.Review.Model = "gpt-6-astra"
			},
		},
		{
			name: "the config file sets the approval policy and approvals",
			cfg:  config.Config{ApprovalPolicy: "never", Approvals: config.Approvals{Allow: []string{"git status"}, Forbid: []string{"rm -rf"}}},
			want: func(r *app.Resolved) {
				r.Approval = approval.Never
				r.Rules = []rules.Rule{
					{Pattern: [][]string{{"git"}, {"status"}}, Decision: rules.Allow, Source: "[approvals] allow"},
					{Pattern: [][]string{{"rm"}, {"-rf"}}, Decision: rules.Forbidden, Source: "[approvals] forbid"},
				}
			},
		},
		{
			name: "--ask beats the config file",
			in:   func(in *app.Inputs) { in.Ask = "on-request" },
			cfg:  config.Config{ApprovalPolicy: "never"},
			want: func(*app.Resolved) {},
		},
		{
			name: "base URL and dotenv pass through",
			in:   func(in *app.Inputs) { in.BaseURL, in.AllowDotenv = "http://llm", true },
			want: func(r *app.Resolved) { r.Settings.BaseURL, r.Settings.AllowDotenv = "http://llm", true },
		},
		{
			name: "the reviewer uses the session model off openai-codex",
			in:   func(in *app.Inputs) { in.Provider, in.Model = "openai", "gpt-x" },
			want: func(r *app.Resolved) {
				r.Settings.Provider, r.Settings.Model = "openai", "gpt-x"
				r.Review.Model = "gpt-x"
			},
		},
		{
			name: "reviewer keys",
			cfg: config.Config{ApprovalsReviewer: "user", Review: config.Review{
				Model: "rev", Effort: "medium", Timeout: "30s",
			}},
			want: func(r *app.Resolved) {
				r.ApprovalsReviewer = review.ReviewerUser
				r.Review = review.Config{Model: "rev", Effort: llm.ReasoningEffortMedium, Timeout: 30 * time.Second}
			},
		},
		{
			name: "agent keys",
			cfg: config.Config{Agents: config.Agents{
				Enabled: new(false), MaxThreads: new(2), MaxDepth: new(2), DefaultSubagentModel: "small", DefaultSubagentReasoningEffort: "low",
			}},
			want: func(r *app.Resolved) {
				r.Agents = app.Agents{MaxThreads: 2, MaxDepth: 2, Model: "small", Effort: "low", ReviewLimits: defaultReviewLimits}
			},
		},
		{
			name: "max_concurrent_threads_per_session beats Codex's alias",
			cfg:  config.Config{Agents: config.Agents{MaxConcurrentThreadsPerSession: new(8), MaxThreads: new(2)}},
			want: func(r *app.Resolved) { r.Agents.MaxThreads = 8 },
		},
		{
			name: "compaction keys",
			cfg:  config.Config{AutoCompactPercent: new(0), ModelContextWindow: 128_000},
			want: func(r *app.Resolved) { r.Compaction.Percent, r.Settings.ContextWindow = 0, 128_000 },
		},
		{
			name: "Codex's compaction keys and uah's summary model",
			cfg: config.Config{
				ModelAutoCompactTokenLimit: 200_000, CompactPrompt: "  Summarize briefly.  ", ExperimentalCompactPromptFile: "/ignored/when/inline.md",
				CompactModel: "gpt-small", CompactEffort: "low", CompactUserMessageMaxTokens: 5000,
			},
			want: func(r *app.Resolved) {
				r.Compaction = compaction.Settings{
					Percent: 90, TokenLimit: 200_000, Model: "gpt-small", Effort: llm.ReasoningEffortLow,
					Prompt: "Summarize briefly.", UserMessageMaxTokens: 5000, Elision: compaction.DefaultElision, KeepCalls: compaction.DefaultKeepCalls, Remote: true,
				}
			},
		},
		{
			name: "remote_compaction",
			cfg:  config.Config{RemoteCompaction: new(false)},
			want: func(r *app.Resolved) { r.Compaction.Remote = false },
		},
		{
			name: "compact_keep_recent_calls",
			cfg:  config.Config{CompactKeepRecentCalls: new(0)},
			want: func(r *app.Resolved) { r.Compaction.KeepCalls = 0 },
		},
		{
			name: "compact_elide_after_calls",
			cfg:  config.Config{CompactElideAfterCalls: new(0)},
			want: func(r *app.Resolved) { r.Compaction.Elision.AfterCalls = 0 },
		},
		{
			name: "request_max_attempts beats the default",
			cfg:  config.Config{RequestMaxAttempts: 20},
			want: func(r *app.Resolved) { r.Settings.MaxAttempts = 20 },
		},
		{
			name: "--max-attempts or its variable beats request_max_attempts",
			in:   func(in *app.Inputs) { in.MaxAttempts = 3 },
			cfg:  config.Config{RequestMaxAttempts: 20},
			want: func(r *app.Resolved) { r.Settings.MaxAttempts = 3 },
		},
		{
			name: "web_search disabled",
			cfg:  config.Config{WebSearch: "disabled"},
			want: func(r *app.Resolved) { r.WebSearch = app.WebSearchDisabled },
		},
		{
			name: "adaptive_effort is the default for a new session",
			cfg:  config.Config{AdaptiveEffort: "2-steps"},
			want: func(r *app.Resolved) { r.Settings.AdaptiveEffort = session.AdaptiveTwoSteps },
		},
		{
			name:    "the resumed session's adaptive effort wins over the configuration",
			resumed: session.Info{AdaptiveEffort: "1-step"},
			cfg:     config.Config{AdaptiveEffort: "2-steps"},
			want:    func(r *app.Resolved) { r.Settings.AdaptiveEffort = session.AdaptiveOneStep },
		},
		{
			name:    "a session from before adaptive effort takes the configuration's",
			resumed: session.Info{Saved: true},
			cfg:     config.Config{AdaptiveEffort: "1-step"},
			want:    func(r *app.Resolved) { r.Settings.AdaptiveEffort = session.AdaptiveOneStep },
		},
		{
			name:    "--adaptive-effort wins over the resumed session's",
			in:      func(in *app.Inputs) { in.AdaptiveEffort = "off" },
			resumed: session.Info{AdaptiveEffort: "2-steps"},
			cfg:     config.Config{AdaptiveEffort: "2-steps"},
			want:    func(r *app.Resolved) {},
		},
		{
			name: "context_preparation off in the config file",
			cfg:  config.Config{ContextPreparation: new(false)},
			want: func(r *app.Resolved) { r.ContextPreparation = false },
		},
		{
			name: "--no-context-preparation or its variable beats the config file",
			in:   func(in *app.Inputs) { in.ContextPreparation = "off" },
			cfg:  config.Config{ContextPreparation: new(true)},
			want: func(r *app.Resolved) { r.ContextPreparation = false },
		},
		{
			name: "UAH_CONTEXT_PREPARATION=on beats the config file",
			in:   func(in *app.Inputs) { in.ContextPreparation = "on" },
			cfg:  config.Config{ContextPreparation: new(false)},
			want: func(r *app.Resolved) {},
		},
		{
			name: "UAH_EFFORT_UPDATES=off",
			in:   func(in *app.Inputs) { in.EffortUpdates = "off" },
			want: func(r *app.Resolved) { r.EffortUpdates = false },
		},
		{
			name: "UAH_EFFORT_UPDATES=on",
			in:   func(in *app.Inputs) { in.EffortUpdates = "on" },
			want: func(r *app.Resolved) {},
		},
		{
			name: "goals: off, a token budget cap, and no continuation cap",
			cfg:  config.Config{Features: config.Features{Goals: new(false)}, Goals: config.Goals{MaxGoalTokenBudget: 50_000, MaxContinuations: new(0)}},
			want: func(r *app.Resolved) { r.Goals = goal.Settings{Disabled: true, MaxTokenBudget: 50_000} },
		},
		{
			name: "the prompt file is read by Setup",
			cfg:  config.Config{ExperimentalCompactPromptFile: "/prompts/compact.md"},
			want: func(r *app.Resolved) { r.CompactPromptFile = "/prompts/compact.md" },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := flagDefaults()
			if tt.in != nil {
				tt.in(&in)
			}
			want := app.Resolved{
				Settings: session.Settings{
					Provider: app.CodexProvider, Model: app.FallbackCodexModel, Effort: app.DefaultEffort,
					Workspace: "/ws", Mode: approval.ModeWorkspace, Sandbox: string(sandbox.WorkspaceWrite),
					MaxAttempts: engine.DefaultMaxAttempts, AdaptiveEffort: session.AdaptiveOff,
				},
				MaxDisk: 5 << 30, Instructions: true, ContextPreparation: true, RequestUserInput: true, EffortUpdates: true,
				Sandbox: sandbox.Policy{Mode: sandbox.WorkspaceWrite}, Compaction: compaction.Settings{Percent: 90, Elision: compaction.DefaultElision, KeepCalls: compaction.DefaultKeepCalls, Remote: true}, Approval: approval.OnRequest,
				ApprovalsReviewer: review.ReviewerUser,
				Review:            review.Config{Model: review.CodexModel, Effort: llm.ReasoningEffortLow, Timeout: review.DefaultTimeout},
				Agents:            app.Agents{Enabled: true, MaxThreads: 4, MaxDepth: 1, ReviewLimits: defaultReviewLimits},
				WebSearch:         app.WebSearchLive,
				Goals:             goal.Settings{MaxContinuations: goal.DefaultMaxContinuations},
			}
			tt.want(&want)
			// No test names a fallback model itself.
			want.DefaultModel = want.Settings.Model == app.FallbackCodexModel || want.Settings.Model == "gpt-6-astra"
			want.Sandbox.Workspace = want.Settings.Workspace
			if want.Review.Model == review.CodexModel && want.Settings.Provider != app.CodexProvider {
				want.Review.Model = want.Settings.Model // off openai-codex, the session model reviews
			}

			got, err := app.Resolve(in, tt.resumed, tt.cfg)

			require.NoError(t, err)
			assert.Equal(t, want, got)
		})
	}
}

func TestResolveUsageErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   func(*app.Inputs)
		cfg  config.Config
		want string
	}{
		{name: "invalid config provider", cfg: config.Config{Provider: "acme"}, want: `invalid provider "acme"`},
		{name: "invalid config effort", cfg: config.Config{Effort: "huge"}, want: `invalid effort "huge"`},
		{name: "invalid model", in: func(in *app.Inputs) { in.Model = "-x" }, want: "starts with a dash"},
		{name: "invalid config max disk", cfg: config.Config{MaxDisk: "lots"}, want: `max_disk: invalid size "LOTS"`},
		{name: "invalid auto_compact_percent", cfg: config.Config{AutoCompactPercent: new(101)}, want: "invalid auto_compact_percent 101"},
		{name: "invalid request_max_attempts", cfg: config.Config{RequestMaxAttempts: -1}, want: "invalid request_max_attempts -1"},
		{name: "invalid --max-attempts", in: func(in *app.Inputs) { in.MaxAttempts = -2 }, want: "invalid --max-attempts -2"},
		{name: "invalid model_context_window", cfg: config.Config{ModelContextWindow: -1}, want: "invalid model_context_window -1"},
		{name: "invalid model_auto_compact_token_limit", cfg: config.Config{ModelAutoCompactTokenLimit: -5}, want: "invalid model_auto_compact_token_limit -5"},
		{name: "invalid compact_effort", cfg: config.Config{CompactEffort: "huge"}, want: `invalid compact_effort "huge"`},
		{name: "invalid compact_user_message_max_tokens", cfg: config.Config{CompactUserMessageMaxTokens: -1}, want: "invalid compact_user_message_max_tokens -1"},
		{name: "relative experimental_compact_prompt_file", cfg: config.Config{ExperimentalCompactPromptFile: "prompt.md"}, want: `invalid experimental_compact_prompt_file "prompt.md"`},
		{name: "invalid web_search", cfg: config.Config{WebSearch: "sometimes"}, want: `invalid web_search "sometimes"`},
		{name: "cached web_search", cfg: config.Config{WebSearch: "cached"}, want: `web_search = "cached" is not available`},
		{name: "invalid model_verbosity", cfg: config.Config{ModelVerbosity: "terse"}, want: `invalid model_verbosity "terse" (want low, medium, high)`},
		{name: "invalid --model-verbosity", in: func(in *app.Inputs) { in.ModelVerbosity = "max" }, want: `invalid model_verbosity "max"`},
		{name: "invalid adaptive_effort", cfg: config.Config{AdaptiveEffort: "on"}, want: `invalid adaptive effort "on" (want off, 1-step, or 2-steps)`},
		{name: "invalid approvals_reviewer", cfg: config.Config{ApprovalsReviewer: "robot"}, want: `invalid approvals_reviewer "robot"`},
		{name: "invalid review effort", cfg: config.Config{Review: config.Review{Effort: "huge"}}, want: `invalid review.effort "huge"`},
		{name: "invalid review timeout", cfg: config.Config{Review: config.Review{Timeout: "-1s"}}, want: `invalid review.timeout "-1s"`},
		{name: "relative review policy_file", cfg: config.Config{Review: config.Review{PolicyFile: "review.md"}}, want: `invalid review.policy_file "review.md"`},
		{name: "invalid agents.max_concurrent_threads_per_session", cfg: config.Config{Agents: config.Agents{MaxConcurrentThreadsPerSession: new(0)}}, want: "invalid agents.max_concurrent_threads_per_session 0"},
		{name: "invalid agents.max_depth", cfg: config.Config{Agents: config.Agents{MaxDepth: new(-1)}}, want: "invalid agents.max_depth -1"},
		{name: "invalid agents effort", cfg: config.Config{Agents: config.Agents{DefaultSubagentReasoningEffort: "huge"}}, want: `invalid agents.default_subagent_reasoning_effort "huge"`},
		{name: "invalid sandbox mode", cfg: config.Config{SandboxMode: "yolo"}, want: `invalid sandbox mode "yolo"`},
		{name: "invalid permission mode", cfg: config.Config{PermissionMode: "full-access"}, want: `invalid permission mode "full-access" (want read-only, workspace, or auto)`},
		{name: "yolo from a file", cfg: config.Config{PermissionMode: "yolo"}, want: `invalid permission mode "yolo" (want read-only, workspace, or auto)`},
		{name: "no sandbox from a file", cfg: config.Config{SandboxMode: "danger-full-access"}, want: `invalid sandbox mode "danger-full-access" (want read-only or workspace-write)`},
		{name: "no sandbox from the flag", in: func(in *app.Inputs) { in.Sandbox = "danger-full-access" }, want: `invalid sandbox mode "danger-full-access"`},
		{name: "yolo with --ask", in: func(in *app.Inputs) { in.Yolo, in.Ask = true, "never" }, want: "takes no --sandbox or --ask"},
		{name: "invalid approval policy", in: func(in *app.Inputs) { in.Ask = "untrusted" }, want: `invalid approval policy "untrusted"`},
		{name: "invalid approval prefix", cfg: config.Config{Approvals: config.Approvals{Allow: []string{"echo $HOME"}}}, want: "not a simple command prefix"},
		{
			name: "fast on a provider without priority processing",
			in:   func(in *app.Inputs) { in.Fast, in.FastSet, in.Provider = true, true, "ollama" },
			want: "--fast needs the openai or openai-codex provider",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := flagDefaults()
			if tt.in != nil {
				tt.in(&in)
			}

			_, err := app.Resolve(in, session.Info{}, tt.cfg)

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
			var usage *app.UsageError
			assert.True(t, errors.As(err, &usage), "a usage error")
		})
	}
}

func TestParseSize(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{in: "1024", want: 1024},
		{in: "0", want: 0},
		{in: "2k", want: 2 << 10},
		{in: "500M", want: 500 << 20},
		{in: "500MB", want: 500 << 20},
		{in: " 1.5G ", want: 3 << 29},
		{in: "", wantErr: true},
		{in: "-1", wantErr: true},
		{in: "5T", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := app.ParseSize(tt.in)
			if tt.wantErr {
				assert.Error(t, err)

				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
