package embedded

import (
	"context"
	"errors"
	"fmt"
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
	"github.com/viktordanov/uah/internal/shellenv"
)

// Context preparation: a new session, a subagent's included, starts with
// a developer message before its first user message, the prepared context
// (internal/contextprep) that the adapters gather from the session's
// facts. The system prompt stays the same, byte for byte, and the message
// is recorded with the session, so the prompt cache holds. Resumed and
// forked sessions get none: theirs is in their history. A fork's is its
// parent's, which names the parent's $TMPDIR, so its first run gets a
// developer message that names its own (forkNote), after the copied
// history, so the request keeps the parent's prefix.

// prepared adds the prepared context, as a developer message, before the
// messages of a new session, when context preparation is on, or a fork's
// correction before the messages of its first run. req has the session's
// ID.
func (w *wiring) prepared(ctx context.Context, req core.Request, messages []core.UserInput) []core.UserInput {
	if note := w.forkNote(req); note != "" {
		return append([]core.UserInput{{ID: uuid.NewString(), Text: note, Role: core.RoleDeveloper}}, messages...)
	}
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

// forkNote is what a fork's first run is told of its own $TMPDIR, when
// the history it copied names its parent's (Engine.Fork, forkTempPath);
// "" otherwise. Once the run's store records it, the wiring removes the
// file (doneForkNote), so a later run does not repeat it.
func (w *wiring) forkNote(req core.Request) string {
	parent, err := os.ReadFile(forkTempPath(w.l.SessionsDir, req.SessionID))
	if err != nil || w.e.cfg.Sandbox == nil {
		return ""
	}

	return fmt.Sprintf(forkTempNote, session.TempDir(w.l.SessionsDir, req.SessionID), parent)
}

// doneForkNote removes a fork's note once the store has recorded the run's
// messages with it.
func (w *wiring) doneForkNote(req core.Request, s runStore) {
	if len(s.early) > 0 {
		_ = os.Remove(forkTempPath(w.l.SessionsDir, req.SessionID))
	}
}

// forkTempNote corrects the prepared context a fork copied from its parent.
const forkTempNote = "You are a fork of the session above, with a session of your own: $TMPDIR (%s) is your private scratch directory, writable in every mode, and %s, which the context above names, is your parent's, not yours."

// facts are what the adapters know about the session: its workspace and
// the instruction files in its system prompt, or the ones its system
// prompt leaves out, the shell its commands run in, and the sandbox of its
// permission mode when it starts, with the session's private $TMPDIR.
func (w *wiring) facts(req core.Request) contextprep.Facts {
	f := contextprep.Facts{
		Workspace: req.Workspace, GOOS: runtime.GOOS,
		MaxOutputLength: operation.DefaultMaxOutputLength,
		Subagent:        strings.HasPrefix(req.SessionID, session.SubagentIDPrefix),
		InstructionsOff: w.e.cfg.InstructionsOff,
	}.WithEnvironment(w.getenv)
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

// shell is the user's shell, which commands run in: $SHELL, else the
// login shell, else /bin/sh (shellenv.Resolve).
func (w *wiring) shell() string { return shellenv.Current(w.getenv).Path }
