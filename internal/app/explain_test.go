package app_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/app"
	"github.com/viktordanov/uah/internal/approval"
	"github.com/viktordanov/uah/internal/config"
	"github.com/viktordanov/uah/internal/mcp"
	"github.com/viktordanov/uah/internal/session"
)

// explained maps each key to its value and sources.
type explained map[string]app.Setting

func explain(t *testing.T, in app.Inputs, o app.Origins) (app.Report, explained) {
	t.Helper()
	if o.Dir == "" {
		o.Dir = "/cwd"
	}
	rep, err := app.Explain(in, o)
	require.NoError(t, err)
	byKey := explained{"workspace": {Key: "workspace", Value: rep.Workspace, Sources: []app.Source{rep.WorkspaceSource}}}
	for _, s := range rep.Settings {
		byKey[s.Key] = s
	}

	return rep, byKey
}

func (e explained) source(key string) string { return e[key].SourceText() }

func TestExplainSources(t *testing.T) {
	t.Parallel()
	defaults := app.Inputs{ConfigPath: "/cfg/config.toml", MaxDisk: "5G"}
	trusted := func(user, project config.Config) config.Layers {
		user.Projects = map[string]config.Project{"/ws": {Trusted: true}}

		return config.Layers{User: user, Project: project, UserFile: "/cfg/config.toml", ProjectFile: "/ws/.uah/config.toml", Trusted: true}
	}
	resumed := session.Info{Provider: "openai", Model: "gpt-resumed", Effort: "low", Workspace: "/resumed"}

	tests := []struct {
		name string
		in   func(*app.Inputs)
		o    app.Origins
		want map[string]string // key: sources
		vals map[string]string // key: value text
	}{
		{
			name: "defaults",
			want: map[string]string{
				"workspace": "default", "provider": "default", "model": "default", "effort": "default",
				"fast": "default", "sandbox_mode": "default", "approvals.allow": "default", "request_max_attempts": "default",
				"projects.<workspace>.trusted": "default",
			},
			vals: map[string]string{
				"workspace": "/cwd", "provider": "openai-codex", "model": "gpt-6-sol", "effort": "high", "max_disk": "5G", "request_max_attempts": "10",
				"sandbox_mode": "workspace-write", "approval_policy": "on-request", "approvals_reviewer": "user",
				"review.model": "codex-auto-review", "auto_compact_percent": "90", "project_root_markers": "[.git]",
				"shell_environment_policy.set": "{}", "approvals.allow": "[]",
			},
		},
		{
			name: "a flag equal to its environment variable counts as the environment",
			in: func(in *app.Inputs) {
				in.Provider, in.Sandbox, in.Ask, in.Effort = "openai", "read-only", "never", "max"
				in.Workspace = "rel"
			},
			o: app.Origins{Env: map[string]string{app.EnvProvider: "openai"}},
			want: map[string]string{
				"provider": "env", "sandbox_mode": "flag", "approval_policy": "flag", "effort": "flag", "workspace": "flag",
			},
			vals: map[string]string{"workspace": "/cwd/rel", "model": "gpt-6-astra"},
		},
		{
			name: "the resumed session beats the files",
			o:    app.Origins{Resumed: resumed, Layers: config.Layers{User: config.Config{Provider: "openrouter", Model: "m", MaxDisk: "1G"}}},
			want: map[string]string{"provider": "session", "model": "session", "effort": "session", "workspace": "session", "max_disk": "user file"},
			vals: map[string]string{"model": "gpt-resumed", "max_disk": "1G"},
		},
		{
			name: "the resumed session's saved fast mode and permission mode beat the files",
			o: app.Origins{
				Resumed: session.Info{Provider: "openai-codex", Model: "gpt-saved", Fast: new(true), AdaptiveEffort: "1-step", Mode: approval.ModeAuto, Saved: true},
				Layers:  config.Layers{User: config.Config{SandboxMode: "read-only", Fast: false, AdaptiveEffort: "2-steps"}},
			},
			want: map[string]string{"model": "session", "fast": "session", "adaptive_effort": "session", "permission_mode": "session", "sandbox_mode": "session"},
			vals: map[string]string{"fast": "true", "adaptive_effort": "1-step", "permission_mode": "auto", "sandbox_mode": "workspace-write"},
		},
		{
			name: "--adaptive-effort beats the resumed session's, and its variable counts as env",
			in:   func(in *app.Inputs) { in.AdaptiveEffort = "off" },
			o: app.Origins{
				Env:     map[string]string{app.EnvAdaptiveEffort: "off"},
				Resumed: session.Info{AdaptiveEffort: "2-steps", Saved: true},
				Layers:  config.Layers{User: config.Config{AdaptiveEffort: "1-step"}},
			},
			want: map[string]string{"adaptive_effort": "env"},
			vals: map[string]string{"adaptive_effort": "off"},
		},
		{
			name: "adaptive_effort in a file is the default for a new session",
			o:    app.Origins{Layers: config.Layers{User: config.Config{AdaptiveEffort: "2-steps"}}},
			want: map[string]string{"adaptive_effort": "user file"},
			vals: map[string]string{"adaptive_effort": "2-steps"},
		},
		{
			name: "permission_mode in a file sets the sandbox mode",
			in:   func(in *app.Inputs) { in.Workspace = "/ws" },
			o:    app.Origins{Layers: trusted(config.Config{SandboxMode: "workspace-write"}, config.Config{PermissionMode: "read-only"})},
			want: map[string]string{"permission_mode": "project file", "sandbox_mode": "project file"},
			vals: map[string]string{"permission_mode": "read-only", "sandbox_mode": "read-only"},
		},
		{
			name: "a provider flag that changes the resumed provider drops the resumed and configured models",
			in:   func(in *app.Inputs) { in.Provider = "openai-codex" },
			o:    app.Origins{Resumed: resumed, Layers: config.Layers{User: config.Config{Model: "m"}}},
			want: map[string]string{"provider": "flag", "model": "default", "effort": "session"},
			vals: map[string]string{"model": "gpt-6-sol"},
		},
		{
			name: "flags with defaults count only when given",
			in: func(in *app.Inputs) {
				in.MaxDisk, in.MaxDiskSet = "1G", true
				in.FastSet, in.NoInstructions = true, true
			},
			o:    app.Origins{Layers: config.Layers{User: config.Config{MaxDisk: "2G", Fast: true}}},
			want: map[string]string{"max_disk": "flag", "fast": "flag", "instructions.enabled": "flag"},
			vals: map[string]string{"max_disk": "1G", "fast": "false", "instructions.enabled": "false"},
		},
		{
			name: "--max-attempts beats request_max_attempts",
			in:   func(in *app.Inputs) { in.MaxAttempts = 3 },
			o:    app.Origins{Layers: config.Layers{User: config.Config{RequestMaxAttempts: 20}}},
			want: map[string]string{"request_max_attempts": "flag"},
			vals: map[string]string{"request_max_attempts": "3"},
		},
		{
			name: "the project file overrides, adds, ORs, and replaces by name",
			in:   func(in *app.Inputs) { in.Workspace = "/ws" },
			o: app.Origins{Layers: trusted(
				config.Config{
					Effort: "low", Model: "m", Fast: true, Approvals: config.Approvals{Allow: []string{"go test"}},
					MCPServers: map[string]mcp.ServerConfig{"docs": {Command: "user-docs"}, "mine": {URL: "https://x"}},
					Hooks:      map[string][]config.Hook{"Stop": {{Command: "user-stop"}}},
				},
				config.Config{
					Effort: "max", Approvals: config.Approvals{Allow: []string{"make"}, Forbid: []string{"rm"}},
					ShellEnvironmentPolicy: config.ShellEnvironmentPolicy{Set: map[string]string{"A": "1"}},
					MCPServers:             map[string]mcp.ServerConfig{"docs": {Command: "npx", Args: []string{"docs"}}},
					Hooks:                  map[string][]config.Hook{"Stop": {{Command: "project-stop"}}},
				},
			)},
			want: map[string]string{
				"effort": "project file", "model": "user file", "fast": "user file",
				"approvals.allow": "user file + project file", "approvals.forbid": "project file",
				"mcp_servers.docs": "project file", "mcp_servers.mine": "user file", "hooks.Stop": "user file + project file",
				"shell_environment_policy.set": "project file", "projects.<workspace>.trusted": "user file",
			},
			vals: map[string]string{
				"effort": "max", "approvals.allow": "[go test, make]", "mcp_servers.docs": "npx docs", "mcp_servers.mine": "https://x",
				"hooks.Stop": "[user-stop, project-stop]", "shell_environment_policy.set": `{A="1"}`, "projects.<workspace>.trusted": "true",
			},
		},
		{
			name: "UAH_LLM_MAX_ATTEMPTS beats request_max_attempts",
			in:   func(in *app.Inputs) { in.MaxAttempts = 4 },
			o:    app.Origins{Env: map[string]string{app.EnvMaxAttempts: "4"}, Layers: config.Layers{User: config.Config{RequestMaxAttempts: 20}}},
			want: map[string]string{"request_max_attempts": "env"},
			vals: map[string]string{"request_max_attempts": "4"},
		},
		{
			name: "request_max_attempts from the user file",
			o:    app.Origins{Layers: config.Layers{User: config.Config{RequestMaxAttempts: 20}}},
			want: map[string]string{"request_max_attempts": "user file"},
			vals: map[string]string{"request_max_attempts": "20"},
		},
		{
			name: "model_instructions_file from the project file",
			in:   func(in *app.Inputs) { in.Workspace = "/ws" },
			o: app.Origins{Layers: trusted(
				config.Config{ModelInstructionsFile: "/cfg/system.md"}, config.Config{ModelInstructionsFile: "/ws/.uah/system.md"},
			)},
			want: map[string]string{"model_instructions_file": "project file"},
			vals: map[string]string{"model_instructions_file": "/ws/.uah/system.md"},
		},
		{
			name: "the project file's max_threads alias overrides the user file's key",
			in:   func(in *app.Inputs) { in.Workspace = "/ws" },
			o: app.Origins{Layers: trusted(
				config.Config{Agents: config.Agents{MaxConcurrentThreadsPerSession: new(6)}}, config.Config{Agents: config.Agents{MaxThreads: new(2)}},
			)},
			want: map[string]string{"agents.max_concurrent_threads_per_session": "project file"},
			vals: map[string]string{"agents.max_concurrent_threads_per_session": "2"},
		},
		{
			name: "the project file's key overrides the user file's max_threads alias",
			in:   func(in *app.Inputs) { in.Workspace = "/ws" },
			o: app.Origins{Layers: trusted(
				config.Config{Agents: config.Agents{MaxThreads: new(6)}}, config.Config{Agents: config.Agents{MaxConcurrentThreadsPerSession: new(2)}},
			)},
			want: map[string]string{"agents.max_concurrent_threads_per_session": "project file"},
			vals: map[string]string{"agents.max_concurrent_threads_per_session": "2"},
		},
		{
			name: "no model_instructions_file keeps uah's default prompt",
			want: map[string]string{"model_instructions_file": "default"},
			vals: map[string]string{"model_instructions_file": "uah's default prompt"},
		},
		{
			name: "project_doc_max_bytes falls back to [instructions] max_bytes",
			o:    app.Origins{Layers: config.Layers{User: config.Config{Instructions: config.Instructions{MaxBytes: 100}}}},
			want: map[string]string{"project_doc_max_bytes": "user file", "instructions.max_bytes": "user file"},
			vals: map[string]string{"project_doc_max_bytes": "100", "instructions.max_bytes": "100"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := defaults
			if tt.in != nil {
				tt.in(&in)
			}
			_, got := explain(t, in, tt.o)
			for key, want := range tt.want {
				assert.Equal(t, want, got.source(key), key)
			}
			for key, want := range tt.vals {
				assert.Equal(t, want, got[key].Text(), key)
			}
		})
	}
}

func TestExplainFiles(t *testing.T) {
	t.Parallel()
	in := app.Inputs{ConfigPath: "/cfg/config.toml", Workspace: "/ws", MaxDisk: "5G"}

	rep, _ := explain(t, in, app.Origins{})
	assert.Equal(t, app.File{Path: "/cfg/config.toml", State: "not found"}, rep.UserFile)
	assert.Equal(t, app.File{Path: "/ws/.uah/config.toml", State: "not trusted"}, rep.ProjectFile)

	rep, _ = explain(t, in, app.Origins{Layers: config.Layers{UserFile: "/cfg/config.toml", Trusted: true}})
	assert.Equal(t, "read", rep.UserFile.State)
	assert.Equal(t, "not found", rep.ProjectFile.State)

	rep, _ = explain(t, in, app.Origins{Layers: config.Layers{Trusted: true, ProjectFile: "/ws/.uah/config.toml"}})
	assert.Equal(t, "read", rep.ProjectFile.State)
}

func TestExplainInvalid(t *testing.T) {
	t.Parallel()
	_, err := app.Explain(app.Inputs{MaxDisk: "5G"}, app.Origins{Dir: "/cwd", Layers: config.Layers{User: config.Config{SandboxMode: "wide-open"}}})

	var usageErr *app.UsageError
	require.ErrorAs(t, err, &usageErr)
}
