// Package contextprep prepares the context a session starts with: short
// blocks about the environment, the sandbox, the agent files, and the
// harness, which uah sends once, as a developer message before the first
// user message.
package contextprep

import (
	"context"

	"github.com/viktordanov/uah/internal/shellenv"
)

// Facts is what the adapters know about a session.
type Facts struct {
	// Workspace is the session's working directory.
	Workspace string
	// InstructionFiles are the instruction files (AGENTS.md) the system
	// prompt holds, in order.
	InstructionFiles []string
	// OmittedInstructionFiles are the instruction files the session's
	// system prompt leaves out on purpose, in order: a session whose
	// system prompt replaces uah's, such as /review's reviewer, has the
	// workspace's files here instead of in InstructionFiles.
	OmittedInstructionFiles []string
	// InstructionsOff is true when loading instruction files is turned
	// off (--no-instructions, or instructions.enabled = false): the
	// workspace's files are not looked for, so the lists above are empty
	// whether or not it has any.
	InstructionsOff bool
	// Shell is the shell commands run in, a path such as /bin/zsh.
	Shell string
	// ShellSource says where Shell came from: "" or "env" for $SHELL,
	// "login" for the login shell when $SHELL was unset or not an
	// executable file, "default" for /bin/sh when neither was usable.
	ShellSource string
	// Locale is the variable that decides the character set and its
	// value, such as LANG=C, or "" when none is set.
	Locale string
	// NotUTF8 is true when the locale does not name UTF-8, so tools may
	// mangle or refuse non-ASCII text.
	NotUTF8 bool
	// MissingPathDirs are the user's tool directories that exist, such as
	// /opt/homebrew/bin, when PATH has none of them; empty otherwise.
	MissingPathDirs []string
	// GOOS is the operating system, as runtime.GOOS names it.
	GOOS string
	// Sandbox describes where commands run.
	Sandbox Sandbox
	// Subagent is true in a subagent's session.
	Subagent bool
	// MaxOutputLength is the Bash tool's default max_output_length, or 0
	// when it is not known.
	MaxOutputLength int
}

// Sandbox describes the sandbox commands run in.
type Sandbox struct {
	// Mode is "read-only" or "workspace-write", or "" when commands run
	// without a sandbox.
	Mode string
	// Network is true when sandboxed commands may use the network.
	Network bool
	// TempDir is the session's private writable temporary directory, or "".
	TempDir string
}

// Adapter contributes one block of the prepared context.
type Adapter interface {
	// Name names the block, such as "environment".
	Name() string
	// Prepare returns the block's text, or "" when it has nothing to say.
	Prepare(ctx context.Context, f Facts) string
}

// WithEnvironment fills the facts the process environment gives: the shell
// ($SHELL, else the login shell, else /bin/sh) and where it came from, the
// locale, and the user's tool directories that PATH lacks.
func (f Facts) WithEnvironment(getenv func(string) string) Facts {
	sh := shellenv.Current(getenv)
	f.Shell, f.ShellSource = sh.Path, string(sh.Source)
	var utf8 bool
	f.Locale, utf8 = shellenv.Locale(getenv)
	f.NotUTF8 = !utf8
	f.MissingPathDirs = shellenv.MissingToolDirs(getenv)

	return f
}
