package app

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"

	"github.com/viktordanov/uah-core/harness/operation"

	"github.com/viktordanov/uah/internal/config"
	"github.com/viktordanov/uah/internal/contextprep"
	"github.com/viktordanov/uah/internal/hooks"
	"github.com/viktordanov/uah/internal/sandbox"
	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/internal/shellenv"
)

// ContextSettings are context preparation's module settings: the user's
// prompts folder next to the configuration file, the ids [context] modules
// turns on, and project modules trusted like project hooks, in the hook
// trust file, read when a session starts.
func ContextSettings(configPath string, cfg config.Config) contextprep.Settings {
	return contextprep.Settings{
		UserDir: filepath.Join(filepath.Dir(configPath), "prompts"),
		Enable:  cfg.Context.Modules,
		Trusted: func(workspace, key string) bool {
			t, err := hooks.LoadTrust(HookTrustFile())

			return err == nil && t.Trusted(workspace, key)
		},
	}
}

// ContextCheck runs a module's check in a read-only sandbox with no
// network, as the engine does; nil where the system has no sandbox.
func ContextCheck(workspace string, getenv func(string) string) contextprep.Checker {
	p := sandbox.Policy{Mode: sandbox.ReadOnly, Workspace: workspace}
	if _, err := p.Wrap([]string{"/bin/sh"}); err != nil {
		return nil
	}

	return contextprep.ExecChecker(p.Wrap, []string{"PATH=" + getenv("PATH"), "HOME=" + getenv("HOME")})
}

// ContextPreview is what `uah context` reports for a workspace.
type ContextPreview struct {
	Workspace string `json:"workspace"`
	// Modules are every module, with whether it applies to a new main
	// session and why.
	Modules []contextprep.Status `json:"modules"`
	// Main and Subagent are the prepared context a new main session and a
	// read-only subagent would get.
	Main     string `json:"main,omitempty"`
	Subagent string `json:"subagent,omitempty"`
}

// PreviewContext evaluates the modules for a new session in the inputs'
// workspace with the configuration's settings, as the engine would, and
// prepares the context of a main session and of a read-only subagent.
func PreviewContext(ctx context.Context, in Inputs, getenv func(string) string) (ContextPreview, error) {
	workspace, err := filepath.Abs(in.Workspace)
	if err != nil {
		return ContextPreview{}, fmt.Errorf("failed to resolve workspace: %w", err)
	}
	in.Workspace = workspace
	cfg, _, err := config.Load(in.ConfigPath, workspace)
	if err != nil {
		return ContextPreview{}, usage(err)
	}
	r, err := Resolve(in, session.Info{}, cfg)
	if err != nil {
		return ContextPreview{}, err
	}
	main := contextprep.Facts{
		Workspace: workspace, GOOS: runtime.GOOS, MaxOutputLength: operation.DefaultMaxOutputLength,
		InstructionsOff: !r.Instructions, // as newEngine sets it
	}.WithEnvironment(getenv, r.Env.Getenv(getenv, shellenv.Keys...)) // commands get r.Env, as in a session
	if r.Instructions {
		loaded, _, err := loadInstructions(workspace, cfg)
		if err != nil {
			return ContextPreview{}, err
		}
		main.InstructionFiles = instructionFiles(loaded)
	}
	stateDir, err := filepath.Abs(in.StateDir)
	if err != nil {
		return ContextPreview{}, fmt.Errorf("failed to resolve state dir: %w", err)
	}
	tmp := session.TempDir(filepath.Join(stateDir, "sessions"), "<session-id>")
	policy := absPolicy(r.Sandbox, workspace)
	sub := main
	sub.Subagent = true
	if _, err := policy.Wrap([]string{main.Shell}); err == nil { //nolint:contextcheck,nolintlint // on Linux, the sandbox probes bwrap once per process, with its own timeout; not on darwin
		if mode := sandbox.Mode(r.Settings.Sandbox); mode != sandbox.FullAccess && mode != "" {
			main.Sandbox = contextprep.Sandbox{Mode: string(mode), Network: policy.Network && mode == sandbox.WorkspaceWrite, TempDir: tmp}
		}
		sub.Sandbox = contextprep.Sandbox{Mode: string(sandbox.ReadOnly), TempDir: tmp} // the read-only sandbox has no network
	}

	check := ContextCheck(workspace, getenv) //nolint:contextcheck,nolintlint // on Linux, the sandbox probes bwrap once per process, with its own timeout; not on darwin
	ms := contextprep.Load(ContextSettings(in.ConfigPath, cfg).Sources(workspace, check))
	p := ContextPreview{Workspace: workspace, Modules: ms.Explain(ctx, main)}
	if r.ContextPreparation {
		p.Main = contextprep.Prepare(ctx, main, ms.Adapters()...)
		p.Subagent = contextprep.Prepare(ctx, sub, ms.Adapters()...)
	}

	return p, nil
}

// TrustContextModules trusts the workspace's project modules as they are
// now, in the hook trust file, and returns their paths.
func TrustContextModules(in Inputs) ([]string, error) {
	workspace, err := filepath.Abs(in.Workspace)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve workspace: %w", err)
	}
	cfg, _, err := config.Load(in.ConfigPath, workspace)
	if err != nil {
		return nil, usage(err)
	}
	ms := contextprep.Load(ContextSettings(in.ConfigPath, cfg).Sources(workspace, nil))
	var keys, paths []string
	for _, m := range ms.Untrusted() {
		keys, paths = append(keys, contextprep.TrustKey(m)), append(paths, m.File)
	}
	if len(keys) == 0 {
		return nil, nil
	}
	t, err := hooks.LoadTrust(HookTrustFile())
	if err != nil {
		return nil, err
	}

	return paths, t.Allow(workspace, keys...)
}
