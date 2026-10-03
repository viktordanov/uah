package app

import (
	"github.com/viktordanov/uah/internal/approval"
	"github.com/viktordanov/uah/internal/config"
	"github.com/viktordanov/uah/internal/usershell"
)

// userShell runs the commands the user types in the TUI's shell mode, in
// uah itself, not in the engine: with the sandbox scripts, the
// environment policy, and the shell the agent's commands use, and with the
// sandbox and the rules only when user_shell_sandbox asks for them.
func userShell(r Resolved, cfg config.Config, sandboxDir string, approver *approval.Approver) *usershell.Runner {
	return &usershell.Runner{
		Dir: sandboxDir, Policy: r.Sandbox, Env: r.Env, Shell: RealShell(),
		Sandboxed: cfg.UserShellSandbox, Approver: approver,
	}
}
