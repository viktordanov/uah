---
name: uah-customization
description: How uah itself works and how to customize it - the prepared context a session starts with (context modules, `uah context`), prompts, hooks, skills, and configuration layers. Use when the user asks about uah's behavior or wants to add or change a context module, prompt, hook, skill, or setting.
---

# uah: context preparation and customization

Check the live state before you answer; these commands only read:

- `uah context`: every context module, its block, source, state, and whether it applies to a new session in this workspace, and why. `--show` also prints the prepared context of a new main session and of a read-only subagent; `--json` prints JSON.
- `uah prompts show context/<path>`: a built-in module as uah ships it, such as `context/environment/fish` or `context/library/go`.
- `uah prompts status`: the user's prompt files, context overrides (it flags copies identical to the built-in), and the user's own modules.
- `uah config`: each setting's value and where it came from. `uah hooks`: the hooks and whether each runs.

The full guide is `docs/context-preparation.md` in the uah repository (github.com/viktordanov/uah); the keys are in `docs/configuration.md`. `~/.uah` below is `$UAH_HOME` when that is set. Files under `~/.uah` are outside the workspace, so writing them needs the user's approval; change them only when the user asks.

## Context preparation

When a new session starts (a main session or a subagent, not a resumed or forked one), uah sends one developer message, `<context_preparation>`, before the first user message. Its blocks, in order: `environment` (shell and OS traps), `sandbox` (what commands may write, `$TMPDIR`), `workspace` (git branch, status, tracked files; code, not modules), `agent files` (the instruction files, said to be all of them, or the ones the system prompt leaves out), `harness` (sizing Bash output), then one block per extra module that applies, titled with its id. A block is cut at 4 KiB and the message at 16 KiB. Which modules apply is decided once, at session start: a change to a module reaches new sessions only. Turn it off with `context_preparation = false`, `--no-context-preparation`, or `UAH_CONTEXT_PREPARATION=off`.

Where modules come from:

1. Built-in modules of the blocks, embedded in uah. A file at `~/.uah/prompts/context/<path>.md` replaces the built-in of the same path (the path `uah prompts show context/<path>` takes). While that file exists, uah updates to that module do not reach the user, so keep only copies the user changed; `uah prompts prune` deletes copies identical to the built-in. `~/.uah/prompts/context.defaults/`, written by `uah prompts init`, is a reference to copy from; uah never reads it.
2. The library, embedded under `library/`: modules that ship with `enabled: false` and apply once the configuration names them: `[context] modules = ["go"]`. `uah context` lists them.
3. The user's modules: `~/.uah/prompts/context.d/<id>.md`, each a block of its own.
4. The project's modules: `<workspace>/.uah/context.d/<id>.md`, used only after `uah context trust`, and again after each change to the file.

An extra module's id must be unused by the library and by the extras read before it; prefix your own ids (such as `my-` or a team name) so a later library module cannot take them.

## The module format

A module is front matter between `---` lines, then the text, which may be Markdown:

```markdown
---
id: my-go
description: Our Go conventions, for repositories with a go.mod
when: {os: [darwin, linux], agent: [main]}
files: [go.mod]
check: [go, version]
enabled: false
---
Run mage test instead of go test; the CI runs the same target. Caches go in {{tmpdir}}.
```

| Key | Value |
| --- | --- |
| `id` | Required. The file's name without `.md`: lower-case letters, digits, `-` and `_`, at most 40 |
| `description` | Required. 1 to 200 characters, shown by `uah context` |
| `when` | The session facts it needs. Absent: any session |
| `files` | Workspace paths, relative, no `..`, `*` the only pattern (within one path element); one must exist |
| `check` | A command as an argv list, such as `[go, version]`, that must exit 0 |
| `enabled` | `false`: applies only where `[context] modules` names its id. Default true. A built-in block's module cannot be turned on this way |

The keys of `when`, each a list unless noted: `shell` (`bash`, `zsh`, `sh`, `fish`, `nu`, `xonsh`, `elvish`, `pwsh`, `cmd`, `csh`, `other`), `shell_path` (absolute, such as `/bin/bash`), `os` (Go's names: `darwin`, `linux`, `freebsd`, `windows`), `sandbox` (`read-only`, `workspace-write`, `none`), `agent` (`main`, `subagent`), and four booleans, `network` (the sandbox has network access), `instructions` (instruction files were loaded into the system prompt), `instructions_omitted` (the system prompt leaves them out on purpose, as `/review`'s reviewer's does), and `instructions_off` (loading them is turned off: `--no-instructions` or `instructions.enabled = false`).

Matching: every key present must match (AND); within a list, one value is enough (OR); an empty or absent key matches anything. uah decides in this order and stops at the first failure: the file parses, the module is enabled, it is trusted (project modules), `when` matches, a `files` path exists, each placeholder has a value, the check passes. So a check runs only for a module that would otherwise apply.

A check never goes through a shell: `argv[0]` is a name on `PATH` or an absolute path, `;` and `$(...)` are plain words, and it runs in a read-only sandbox with no network, no stdin, only `PATH` and `HOME`, for 2 seconds at most. Without a sandbox it does not run, and the module does not apply. A module's text is never run.

Placeholders in the text: `{{shell}}` (path), `{{shell_name}}`, `{{os}}` (macOS, Linux, ...), `{{goos}}`, `{{mode}}` (sandbox mode or `none`), `{{tmpdir}}`, `{{workspace}}`, `{{agent}}`, `{{instruction_files}}` (a `- ` list), `{{omitted_instruction_files}}` (a `- ` list of the files the system prompt leaves out), `{{max_output_length}}`. A module whose placeholder has no value in the session, such as `{{tmpdir}}` without a sandbox, does not apply.

The schema is strict: an unknown key, a wrong type, an unknown `when` value, or an unknown `{{placeholder}}` is an error; `uah context` lists the file with its error and it never applies. A module file is at most 16 KiB.

To add a module: write it in `~/.uah/prompts/context.d/` (or the project's `.uah/context.d/`, then `uah context trust`), run `uah context` to see whether it applies and why, and `uah context --show` to read the result. To change a built-in: copy `~/.uah/prompts/context.defaults/<path>.md` (or the output of `uah prompts show context/<path>`) to `~/.uah/prompts/context/<path>.md` and edit the copy.

## Other customization points

- Prompts: `uah prompts init` writes `compact.md`, `system.md`, `system-codex.md`, `system-runner.md`, and `review.md` to `~/.uah/prompts`. Each applies only when a key names it: `model_instructions_file`, `experimental_compact_prompt_file`, `[review] policy_file`. `uah prompts show <name>` prints the built-in.
- Instructions: `~/.uah/AGENTS.md`, then one `AGENTS.override.md` or `AGENTS.md` per directory from the project root down to the workspace; `project_doc_fallback_filenames = ["CLAUDE.md"]` reads Claude Code's files. A line that is only `@path` includes that file.
- Skills: `<name>/SKILL.md` with `name` and `description` front matter, in `.agents/skills` (each directory from the workspace up to the project root), `<workspace>/.harness/skills`, `~/.uah/skills`, and `$CODEX_HOME/skills`; for one name, the first folder in that order wins. This skill is built into uah (written to `~/.uah/skills/.system/`, which uah rewrites); a skill of the same name in any of those folders replaces it.
- Hooks: `[[hooks.<Event>]]` with `command`, and optionally `matcher` and `timeout`, for `SessionStart`, `SessionEnd`, `UserPromptSubmit`, `PreToolUse`, `PostToolUse`, `PermissionRequest`, `Stop`, `SubagentStart`, `SubagentStop`, `PreCompact`. Claude Code's contract: the event as JSON on stdin, exit 2 blocks with stderr as the reason. A project's hooks run only after `uah hooks trust`.
- Configuration: `~/.uah/config.toml`, then `~/.uah/config.d/*.toml` in lexical order, then the file `UAH_EXTRA_CONFIG` names, then `<workspace>/.uah/config.toml` when the user file marks the workspace `[projects."<path>"] trusted = true`; each later file wins, and flags and environment variables win over all. Unknown keys are errors. Subagent definitions go in `~/.uah/agents/` or a trusted project's `.uah/agents/`.
