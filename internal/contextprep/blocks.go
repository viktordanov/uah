package contextprep

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
)

// block is a built-in block: its name and its modules, in order. The
// modules' text and conditions are in their files; the block joins the
// ones that apply, a line apart.
type block struct {
	name  string
	paths []string
}

// blocks are the built-in blocks with modules, by name.
var blocks = []block{
	{"environment", []string{
		"environment/intro", "environment/bash", "environment/sh", "environment/zsh", "environment/fish", "environment/nu",
		"environment/xonsh", "environment/elvish", "environment/pwsh", "environment/cmd", "environment/csh", "environment/other",
		"os/darwin", "os/bsd", "os/windows",
	}},
	{keySandbox, []string{
		"sandbox/read-only", "sandbox/workspace-write", "sandbox/tmpdir", "sandbox/bash-heredoc", "sandbox/processes", "sandbox/local-sockets",
	}},
	{"agent files", []string{"agent-files", "agent-files-omitted", "agent-files-off", "agent-files-none"}},
	{"harness", []string{"harness/output"}},
}

// sectionOrder is every built-in block's modules, in order.
func sectionOrder() []string {
	var out []string
	for _, b := range blocks {
		out = append(out, b.paths...)
	}

	return out
}

// blockOf names the built-in block a module path belongs to, or "".
func blockOf(p string) string {
	for _, b := range blocks {
		if slices.Contains(b.paths, p) {
			return b.name
		}
	}

	return ""
}

// The built-in blocks' adapters. Each renders its modules from Modules,
// or the built-ins when Modules is nil.
type (
	// Environment is the "environment" block: the shell and the OS, with
	// the constructs that break in them.
	Environment struct{ Modules *Modules }
	// SandboxNotes is the "sandbox" block: what sandboxed commands may
	// write, the session's $TMPDIR, and what the sandbox blocks.
	SandboxNotes struct{ Modules *Modules }
	// AgentFiles is the "agent files" block: the instruction files, said
	// to be all of them, or the ones the system prompt leaves out.
	AgentFiles struct{ Modules *Modules }
	// Harness is the "harness" block: how to use uah's tools well.
	Harness struct{ Modules *Modules }
)

// Name implements Adapter.
func (Environment) Name() string { return "environment" }

// Name implements Adapter.
func (SandboxNotes) Name() string { return keySandbox }

// Name implements Adapter.
func (AgentFiles) Name() string { return "agent files" }

// Name implements Adapter.
func (Harness) Name() string { return "harness" }

// Prepare implements Adapter.
func (a Environment) Prepare(ctx context.Context, f Facts) string {
	return orBuiltin(a.Modules).block(ctx, a.Name(), f)
}

// Prepare implements Adapter.
func (a SandboxNotes) Prepare(ctx context.Context, f Facts) string {
	return orBuiltin(a.Modules).block(ctx, a.Name(), f)
}

// Prepare implements Adapter.
func (a AgentFiles) Prepare(ctx context.Context, f Facts) string {
	return orBuiltin(a.Modules).block(ctx, a.Name(), f)
}

// Prepare implements Adapter.
func (a Harness) Prepare(ctx context.Context, f Facts) string {
	return orBuiltin(a.Modules).block(ctx, a.Name(), f)
}

// orBuiltin is ms, or the built-ins alone.
func orBuiltin(ms *Modules) *Modules {
	if ms != nil {
		return ms
	}

	return Load(Sources{})
}

// extra is the block of one extra module, under its id.
type extra struct {
	ms *Modules
	m  *Module
}

// Name implements Adapter.
func (e extra) Name() string { return e.m.Meta.ID }

// Prepare implements Adapter.
func (e extra) Prepare(ctx context.Context, f Facts) string { return e.ms.Evaluate(ctx, e.m, f).Text }

// Adapters are the session's blocks, in order: the environment, the
// sandbox, the workspace, the agent files, the harness, then one block per
// extra module that applies (the library's that are enabled, the user's,
// and the project's).
func (ms *Modules) Adapters() []Adapter {
	out := []Adapter{Environment{ms}, SandboxNotes{ms}, Workspace{}, AgentFiles{ms}, Harness{ms}}
	for _, m := range ms.extras {
		if m.Err == nil {
			out = append(out, extra{ms, m})
		}
	}

	return out
}

// block joins the text of the block's modules that apply, a line apart.
func (ms *Modules) block(ctx context.Context, name string, f Facts) string {
	var lines []string
	for _, b := range blocks {
		if b.name != name {
			continue
		}
		for _, p := range b.paths {
			if m := ms.builtin[p]; m != nil {
				if r := ms.Evaluate(ctx, m, f); r.Text != "" {
					lines = append(lines, r.Text)
				}
			}
		}
	}

	return strings.Join(lines, "\n")
}

// Result is whether a module applies to a session, and why.
type Result struct {
	Applies bool
	// Reason says why it applies or not, such as "when.shell: zsh is not
	// fish".
	Reason string
	// Text is the module's rendered text when it applies.
	Text string
}

// Evaluate decides whether m applies to the session: it must parse, be
// enabled and trusted, match When and Files, have a value for each of its
// placeholders, and pass its check, in that order, so a check runs only
// for a module that would otherwise apply.
func (ms *Modules) Evaluate(ctx context.Context, m *Module, f Facts) Result {
	switch {
	case m.Err != nil:
		return Result{Reason: "error: " + m.Err.Error()}
	case !ms.Enabled(m):
		return Result{Reason: "disabled (enabled: false; [context] modules can turn it on)"}
	case !ms.Trusted(m):
		return Result{Reason: "untrusted project module: run `uah context trust` to use it"}
	}
	if why := m.Meta.When.mismatch(f); why != "" {
		return Result{Reason: why}
	}
	if len(m.Meta.Files) > 0 && !anyFile(f.Workspace, m.Meta.Files) {
		return Result{Reason: "files: none of " + strings.Join(m.Meta.Files, ", ") + " in the workspace"}
	}
	text, err := render(m.Body, variables(f))
	if err != nil {
		return Result{Reason: err.Error()}
	}
	if len(m.Meta.Check) > 0 {
		if err := ms.check(ctx, m.Meta.Check, f.Workspace); err != nil {
			return Result{Reason: fmt.Sprintf("check %q failed: %v", m.Meta.Check, err)}
		}
	}

	return Result{Applies: true, Reason: "applies", Text: text}
}

// check runs a check once per Modules and directory.
func (ms *Modules) check(ctx context.Context, argv []string, dir string) error {
	if ms.src.Check == nil {
		return ErrNoSandbox
	}
	key := dir + "\x00" + strings.Join(argv, "\x00")
	ms.mu.Lock()
	if ms.checked == nil {
		ms.checked = map[string]error{}
	}
	err, ok := ms.checked[key]
	ms.mu.Unlock()
	if ok {
		return err
	}
	err = ms.src.Check(ctx, argv, dir)
	ms.mu.Lock()
	ms.checked[key] = err
	ms.mu.Unlock()

	return err
}

// mismatch is the first condition the session does not meet, or "".
func (w When) mismatch(f Facts) string {
	f = f.normalized()
	for _, c := range []struct {
		key  string
		want []string
		have string
	}{
		{keyShell, w.Shell, shellKey(f.Shell)},
		{"shell_path", w.ShellPath, f.Shell},
		{"os", w.OS, f.GOOS},
		{keySandbox, w.Sandbox, cmp.Or(f.Sandbox.Mode, "none")},
		{keyAgent, w.Agent, agentOf(f)},
	} {
		if len(c.want) > 0 && !slices.Contains(c.want, c.have) {
			return fmt.Sprintf("when.%s: %s is not %s", c.key, c.have, strings.Join(c.want, " or "))
		}
	}
	if w.Network != nil && *w.Network != f.Sandbox.Network {
		return fmt.Sprintf("when.network: the sandbox's network is %t", f.Sandbox.Network)
	}
	if w.Instructions != nil && *w.Instructions != (len(f.InstructionFiles) > 0) {
		if len(f.InstructionFiles) > 0 {
			return "when.instructions: instruction files were loaded"
		}

		return "when.instructions: no instruction files were loaded"
	}
	if w.InstructionsOmitted != nil && *w.InstructionsOmitted != (len(f.OmittedInstructionFiles) > 0) {
		if len(f.OmittedInstructionFiles) > 0 {
			return "when.instructions_omitted: the system prompt leaves the instruction files out"
		}

		return "when.instructions_omitted: no instruction files were left out"
	}
	if w.InstructionsOff != nil && *w.InstructionsOff != f.InstructionsOff {
		if f.InstructionsOff {
			return "when.instructions_off: loading instruction files is turned off"
		}

		return "when.instructions_off: loading instruction files is on"
	}

	return ""
}

// anyFile reports whether one of the patterns names a file in the
// workspace.
func anyFile(workspace string, patterns []string) bool {
	if workspace == "" {
		return false
	}
	for _, p := range patterns {
		matches, err := filepath.Glob(filepath.Join(workspace, filepath.FromSlash(p)))
		if err != nil {
			continue
		}
		for _, m := range matches {
			if _, err := os.Stat(m); err == nil {
				return true
			}
		}
	}

	return false
}

// normalized fills the defaults: /bin/sh, which uah runs commands with
// when $SHELL is unset, and the running OS.
func (f Facts) normalized() Facts {
	f.Shell = cmp.Or(f.Shell, "/bin/sh")
	f.GOOS = cmp.Or(f.GOOS, runtime.GOOS)

	return f
}

// agentOf is "main" or "subagent".
func agentOf(f Facts) string {
	if f.Subagent {
		return "subagent"
	}

	return "main"
}

// variables are the placeholders' values; "" is no value.
func variables(f Facts) map[string]string {
	f = f.normalized()
	vars := map[string]string{
		keyShell: f.Shell, "shell_name": shellName(f.Shell), "os": osName(f.GOOS), "goos": f.GOOS,
		"mode": cmp.Or(f.Sandbox.Mode, "none"), "tmpdir": f.Sandbox.TempDir, keyWorkspace: f.Workspace, keyAgent: agentOf(f),
	}
	if len(f.InstructionFiles) > 0 {
		vars["instruction_files"] = "- " + strings.Join(f.InstructionFiles, "\n- ")
	}
	if len(f.OmittedInstructionFiles) > 0 {
		vars["omitted_instruction_files"] = "- " + strings.Join(f.OmittedInstructionFiles, "\n- ")
	}
	if f.MaxOutputLength > 0 {
		vars["max_output_length"] = strconv.Itoa(f.MaxOutputLength)
	}

	return vars
}
