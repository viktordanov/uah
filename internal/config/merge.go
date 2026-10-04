package config

import (
	"maps"
	"slices"

	"github.com/viktordanov/uah/internal/mcp"
)

// merge returns base with every value set in over (a layer or a project
// file) replacing it. Hooks, writable roots, approval lists, and environment
// patterns add up; an MCP server replaces the one of the same name whole;
// booleans that turn something on stay on. It changes neither argument's
// maps or slices.
func merge(base, over Config) Config {
	set(&base.Provider, over.Provider)
	set(&base.Model, over.Model)
	set(&base.Effort, over.Effort)
	set(&base.MaxDisk, over.MaxDisk)
	set(&base.WebSearch, over.WebSearch)
	set(&base.ModelVerbosity, over.ModelVerbosity)
	base.Fast = base.Fast || over.Fast
	base.TUI.Details = base.TUI.Details || over.TUI.Details
	if over.TUI.Mouse != nil {
		base.TUI.Mouse = over.TUI.Mouse
	}
	if over.TUI.Title != nil {
		base.TUI.Title = over.TUI.Title
	}
	set(&base.History.Persistence, over.History.Persistence)
	if over.History.MaxBytes != nil {
		base.History.MaxBytes = over.History.MaxBytes
	}
	if over.ContextPreparation != nil {
		base.ContextPreparation = over.ContextPreparation
	}
	if over.Tools.ExperimentalRequestUserInput.Enabled != nil {
		base.Tools.ExperimentalRequestUserInput.Enabled = over.Tools.ExperimentalRequestUserInput.Enabled
	}
	if over.Features.Goals != nil {
		base.Features.Goals = over.Features.Goals
	}
	if over.Goals.MaxGoalTokenBudget != 0 {
		base.Goals.MaxGoalTokenBudget = over.Goals.MaxGoalTokenBudget
	}
	if over.Goals.MaxContinuations != nil {
		base.Goals.MaxContinuations = over.Goals.MaxContinuations
	}
	if over.Context.Modules != nil {
		base.Context.Modules = over.Context.Modules
	}
	if over.AutoCompactPercent != nil {
		base.AutoCompactPercent = over.AutoCompactPercent
	}
	if over.RequestMaxAttempts != 0 {
		base.RequestMaxAttempts = over.RequestMaxAttempts
	}
	if over.ModelContextWindow != 0 {
		base.ModelContextWindow = over.ModelContextWindow
	}
	mergeCompaction(&base, over)
	mergeSandbox(&base, over)
	mergeEnv(&base.ShellEnvironmentPolicy, over.ShellEnvironmentPolicy)
	mergeInstructions(&base, over)
	mergeAgents(&base.Agents, over.Agents)
	base.Hooks = mergeMap(base.Hooks, over.Hooks, func(a, b []Hook) []Hook { return slices.Concat(a, b) })
	base.MCPServers = mergeMap(base.MCPServers, over.MCPServers, func(_, b mcp.ServerConfig) mcp.ServerConfig { return b })
	set(&base.MCPOAuthCredentialsStore, over.MCPOAuthCredentialsStore)
	set(&base.MCPOAuthCallbackURL, over.MCPOAuthCallbackURL)
	if over.MCPOAuthCallbackPort != 0 {
		base.MCPOAuthCallbackPort = over.MCPOAuthCallbackPort
	}
	base.Projects = mergeMap(base.Projects, over.Projects, func(_, b Project) Project { return b })

	return base
}

func set(dst *string, v string) {
	if v != "" {
		*dst = v
	}
}

// mergeSandbox merges the sandbox, approval, and review keys.
func mergeSandbox(base *Config, over Config) {
	set(&base.SandboxMode, over.SandboxMode)
	set(&base.PermissionMode, over.PermissionMode)
	set(&base.ApprovalPolicy, over.ApprovalPolicy)
	base.Approvals.Allow = slices.Concat(base.Approvals.Allow, over.Approvals.Allow)
	base.Approvals.Forbid = slices.Concat(base.Approvals.Forbid, over.Approvals.Forbid)
	set(&base.ApprovalsReviewer, over.ApprovalsReviewer)
	base.UserShellSandbox = base.UserShellSandbox || over.UserShellSandbox
	set(&base.AdaptiveEffort, over.AdaptiveEffort)
	set(&base.Review.Model, over.Review.Model)
	set(&base.Review.Effort, over.Review.Effort)
	set(&base.Review.Timeout, over.Review.Timeout)
	set(&base.Review.PolicyFile, over.Review.PolicyFile)
	w := &base.SandboxWorkspaceWrite
	w.NetworkAccess = w.NetworkAccess || over.SandboxWorkspaceWrite.NetworkAccess
	w.WritableRoots = slices.Concat(w.WritableRoots, over.SandboxWorkspaceWrite.WritableRoots)
}

func mergeEnv(env *ShellEnvironmentPolicy, over ShellEnvironmentPolicy) {
	set(&env.Inherit, over.Inherit)
	if over.IgnoreDefaultExcludes != nil {
		env.IgnoreDefaultExcludes = over.IgnoreDefaultExcludes
	}
	env.Exclude = slices.Concat(env.Exclude, over.Exclude)
	env.IncludeOnly = slices.Concat(env.IncludeOnly, over.IncludeOnly)
	env.Set = mergeMap(env.Set, over.Set, func(_, b string) string { return b })
}

func mergeInstructions(base *Config, over Config) {
	set(&base.ModelInstructionsFile, over.ModelInstructionsFile)
	if over.Instructions.Enabled != nil {
		base.Instructions.Enabled = over.Instructions.Enabled
	}
	if over.Instructions.MaxBytes != 0 {
		base.Instructions.MaxBytes = over.Instructions.MaxBytes
	}
	if over.ProjectDocFallbackFilenames != nil {
		base.ProjectDocFallbackFilenames = over.ProjectDocFallbackFilenames
	}
	if over.ProjectRootMarkers != nil {
		base.ProjectRootMarkers = over.ProjectRootMarkers
	}
	if over.ProjectDocMaxBytes != 0 {
		base.ProjectDocMaxBytes = over.ProjectDocMaxBytes
	}
}

// mergeMap returns a copy of base with over's entries added, combining a key
// both have.
func mergeMap[V any](base, over map[string]V, combine func(a, b V) V) map[string]V {
	base = maps.Clone(base)
	for k, v := range over {
		if base == nil {
			base = map[string]V{}
		}
		base[k] = combine(base[k], v)
	}

	return base
}

func mergeAgents(base *Agents, over Agents) {
	if over.Enabled != nil {
		base.Enabled = over.Enabled
	}
	if over.MaxConcurrentThreadsPerSession != nil {
		base.MaxConcurrentThreadsPerSession = over.MaxConcurrentThreadsPerSession
	}
	if over.MaxThreads != nil {
		base.MaxThreads = over.MaxThreads
	}
	if over.MaxDepth != nil {
		base.MaxDepth = over.MaxDepth
	}
	set(&base.DefaultSubagentModel, over.DefaultSubagentModel)
	set(&base.DefaultSubagentReasoningEffort, over.DefaultSubagentReasoningEffort)
}

func mergeCompaction(base *Config, over Config) {
	if over.ModelAutoCompactTokenLimit != 0 {
		base.ModelAutoCompactTokenLimit = over.ModelAutoCompactTokenLimit
	}
	set(&base.CompactPrompt, over.CompactPrompt)
	set(&base.ExperimentalCompactPromptFile, over.ExperimentalCompactPromptFile)
	set(&base.CompactModel, over.CompactModel)
	set(&base.ReviewModel, over.ReviewModel)
	set(&base.CompactEffort, over.CompactEffort)
	if over.CompactUserMessageMaxTokens != 0 {
		base.CompactUserMessageMaxTokens = over.CompactUserMessageMaxTokens
	}
	if over.CompactElideAfterCalls != nil {
		base.CompactElideAfterCalls = over.CompactElideAfterCalls
	}
	if over.CompactKeepRecentCalls != nil {
		base.CompactKeepRecentCalls = over.CompactKeepRecentCalls
	}
	if over.RemoteCompaction != nil {
		base.RemoteCompaction = over.RemoteCompaction
	}
}
