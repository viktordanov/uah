package embedded

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/google/uuid"

	"github.com/viktordanov/uagent/core"
	"github.com/viktordanov/uah-core/harness/operation"

	"github.com/viktordanov/uah/internal/contextprep"
	"github.com/viktordanov/uah/internal/instructions"
	"github.com/viktordanov/uah/internal/sandbox"
	"github.com/viktordanov/uah/internal/session"
)

// Context preparation: a new session, a subagent's included, starts with
// a developer message before its first user message, the prepared context
// (internal/contextprep) that the adapters gather from the session's
// facts. The system prompt stays the same, byte for byte, and the message
// is recorded with the session, so the prompt cache holds. Resumed and
// forked sessions get none: theirs is in their history.

// prepared adds the prepared context, as a developer message, before the
// messages of a new session, when context preparation is on. req has the
// session's ID.
func (w *wiring) prepared(ctx context.Context, req core.Request, messages []core.UserInput) []core.UserInput {
	if !w.e.cfg.ContextPreparation {
		return messages
	}
	_, err := os.Stat(filepath.Join(w.l.SessionsDir, req.SessionID+".session.jsonl"))
	if !errors.Is(err, fs.ErrNotExist) {
		return messages // resumed or forked
	}
	text := contextprep.Prepare(ctx, w.facts(req), w.modules(req).Adapters()...) //nolint:contextcheck,nolintlint // on Linux, the sandbox probes bwrap once per process, with its own timeout; not on darwin
	if text == "" {
		return messages
	}

	return append([]core.UserInput{{ID: uuid.NewString(), Text: text, Role: core.RoleDeveloper}}, messages...)
}

// facts are what the adapters know about the session: its workspace and
// the instruction files in its system prompt, or the ones its system
// prompt leaves out, the shell its commands run in, and the sandbox of its
// permission mode when it starts, with the session's private $TMPDIR.
func (w *wiring) facts(req core.Request) contextprep.Facts {
	f := contextprep.Facts{
		Workspace: req.Workspace, Shell: w.shell(), GOOS: runtime.GOOS,
		MaxOutputLength: operation.DefaultMaxOutputLength,
		Subagent:        strings.HasPrefix(req.SessionID, session.SubagentIDPrefix),
		InstructionsOff: w.e.cfg.InstructionsOff,
	}
	if instructions.HasProject(req.SystemPrompt) {
		f.InstructionFiles = w.e.cfg.InstructionFiles
	} else {
		f.OmittedInstructionFiles = w.e.cfg.InstructionFiles // a system prompt that replaces uah's: /review's
	}
	if w.e.cfg.Sandbox == nil {
		return f
	}
	mode := w.mode.get().Sandbox()
	p := w.policy(req, mode)
	if mode == sandbox.FullAccess {
		return f
	}
	if _, err := p.Wrap([]string{f.Shell}); err != nil {
		return f // no sandbox on this system: commands ask instead
	}
	f.Sandbox = contextprep.Sandbox{Mode: string(mode), Network: p.Network, TempDir: p.TempDir}

	return f
}

// modules are the session's context modules: the built-ins, the user's
// and the project's, and the library's the configuration turns on. A
// module's check runs in a read-only sandbox with no network, with only
// PATH and HOME; without a sandbox, checks do not run.
func (w *wiring) modules(req core.Request) *contextprep.Modules {
	var check contextprep.Checker
	if w.e.cfg.Sandbox != nil {
		p := sandbox.Policy{Mode: sandbox.ReadOnly, Workspace: req.Workspace}
		check = contextprep.ExecChecker(p.Wrap, []string{"PATH=" + w.getenv("PATH"), "HOME=" + w.getenv("HOME")})
	}

	return contextprep.Load(w.e.cfg.ContextModules.Sources(req.Workspace, check))
}

// shell is the user's shell, which commands run in: $SHELL, or /bin/sh.
func (w *wiring) shell() string {
	if shell := strings.TrimSpace(w.getenv("SHELL")); shell != "" {
		return shell
	}

	return "/bin/sh"
}
