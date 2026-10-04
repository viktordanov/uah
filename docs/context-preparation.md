# Context preparation

Every new uah session starts with a short message of facts that the model would otherwise discover by trial and error: which shell runs its commands and which constructs break in it, what the sandbox lets it write, where its private `$TMPDIR` is, the git state, which instruction files are already loaded, and how to size command output. uah writes this message once, when the session starts, from Markdown modules that you can read, replace, and add to.

This page is the user guide. The [implementation reference](../internal/contextprep/README.md) describes the code, and the [configuration reference](configuration.md#context-modules) lists the keys.

1. [Why it exists](#why-it-exists)
2. [What the model receives](#what-the-model-receives)
3. [Modules](#modules)
4. [The module format](#the-module-format)
5. [When a module applies](#when-a-module-applies)
6. [Placeholders](#placeholders)
7. [Where modules come from](#where-modules-come-from)
8. [The library](#the-library)
9. [Security](#security)
10. [Commands](#commands)
11. [Recipes](#recipes)
12. [Troubleshooting](#troubleshooting)

## Why it exists

Without it, a model writes POSIX `sh` in fish, uses GNU flags on macOS, writes into a read-only sandbox, searches the disk for AGENTS.md files that are already in its prompt, and reruns commands whose output was cut. Each mistake costs a failed command and a turn. In the agent benchmark, v1.8.0 with context preparation took 13% less wall time and had 39% fewer failed commands than v1.7.5 without it. The first round of measurements is in the [ledger](ledger.md) (row 108): fish syntax errors fell from 22 to 0, heredoc failures from 25 to 0, and AGENTS.md searches from 45 to 0.

## What the model receives

When a new session starts, uah sends one developer message before the first user message. This includes a subagent's session. A resumed or forked session gets no new message, because its history already has one. A fork's copied message is its parent's and names the parent's `$TMPDIR`, so the fork's first run adds one sentence of its own, after the copied history: its own `$TMPDIR`, and that the parent's is not its own. The model reads a developer message as the harness's, not the user's.

```text
<context_preparation>
uah prepared this when the session started, so you need not look it up again. It describes the session as it began; files and git's state may change as you work.

## environment
Commands run in fish (/opt/homebrew/bin/fish -c) on macOS.
...

## sandbox
...

## workspace
...

## agent files
...

## harness
...

## go
Go: when the build cache cannot be written in the sandbox, ...
</context_preparation>
```

The blocks come in this order:

| Block | What it says | From |
| --- | --- | --- |
| `environment` | The shell and the OS, and the constructs that break in a shell that is not POSIX, zsh's gotchas, macOS's bash 3.2, and BSD flags. When uah was started without the user's environment, also which shell uah picked as `$SHELL` was unset, that the locale is not UTF-8, and which of the user's tool directories `PATH` lacks | Modules `environment/*`, `os/*` |
| `sandbox` | What sandboxed commands may write, the session's private `$TMPDIR`, and what macOS's sandbox blocks | Modules `sandbox/*` |
| `workspace` | The git branch, `git status --short`, and the tracked files by top directory | Code (git's output), not modules |
| `agent files` | The instruction files in the system prompt, said to be all of them; or, for a session whose system prompt replaces uah's without them (`/review`'s reviewer), the files it leaves out; or, with `--no-instructions` or `instructions.enabled = false`, that loading them is off (not that there are none) | Modules `agent-files`, `agent-files-omitted`, `agent-files-off`, `agent-files-none` |
| `harness` | How to size the Bash tool's `max_output_length` | Module `harness/output` |
| One block per extra module | Each library, user, or project module that applies, titled with its id | The module |

A block with nothing to say is left out. Each block is cut at 4 KiB and the whole message at 16 KiB; a cut ends at a line break with `(cut: the rest is over N bytes)`.

The message is decided once, at session start. A module you add or change reaches the next new session, not the running one. The system prompt does not change and the message stays in the session's history, so the prompt cache holds.

To turn context preparation off, set `context_preparation = false`, pass `--no-context-preparation`, or set `UAH_CONTEXT_PREPARATION=off`.

## Modules

Every text in the message, except the `workspace` block, comes from a module: a Markdown file whose front matter says when it applies. A built-in block joins the text of its modules that apply, one line apart, in a fixed order. Every other module that applies is a block of its own.

`uah context` lists every module and whether it applies here. `uah prompts show context/<path>` prints a built-in, such as `context/environment/fish`.

## The module format

A module is front matter between two `---` lines, then the text:

```markdown
---
id: my-go
description: Our Go conventions, for repositories with a go.mod
when: {os: [darwin, linux], agent: [main]}
files: [go.mod]
check: [go, version]
enabled: false
---
Run mage test instead of go test; CI runs the same target. Put caches in {{tmpdir}}.
```

| Key | Required | Value |
| --- | --- | --- |
| `id` | Yes | The file's name without `.md`. Lower-case letters, digits, `-`, and `_`, starting with a letter or digit, at most 40 characters |
| `description` | Yes | 1 to 200 characters. `uah context` shows it |
| `when` | No | The session facts the module needs; see [When a module applies](#when-a-module-applies). Absent: any session |
| `files` | No | Workspace paths, one of which must exist. Relative, inside the workspace (no `..`, not absolute); `*` is the only pattern and matches within one path element, such as `cmd/*/main.go` |
| `check` | No | A command as an argv list, such as `[go, version]`, that must exit 0. See [Security](#security) |
| `enabled` | No | `false` keeps the module off until `[context] modules` names its id. True by default. For a built-in block's module, `false` turns it off and the configuration cannot turn it on |

The keys of `when`:

| Key | Type | Values |
| --- | --- | --- |
| `shell` | list | The shell's family, from its base name: `bash`, `zsh`, `sh`, `fish`, `nu`, `xonsh`, `elvish`, `pwsh`, `cmd`, `csh`, `other` |
| `shell_path` | list | The shell's exact absolute path, such as `/bin/bash` |
| `os` | list | Go's name for the OS: `darwin`, `linux`, `freebsd`, `windows`, and so on |
| `sandbox` | list | The sandbox mode at session start: `read-only`, `workspace-write`, or `none` (yolo, or no sandbox on the system) |
| `agent` | list | `main` or `subagent` |
| `network` | bool | Whether sandboxed commands have network access |
| `instructions` | bool | Whether any instruction files (AGENTS.md and so on) were loaded into the system prompt |
| `instructions_omitted` | bool | Whether the system prompt leaves the workspace's instruction files out on purpose, as the system prompt of `/review`'s reviewer does, which replaces uah's |
| `instructions_off` | bool | Whether loading instruction files is turned off (`--no-instructions`, or `instructions.enabled = false`); then none is looked for, so `instructions` and `instructions_omitted` are false whether or not the workspace has any |
| `shell_source` | list | Where the shell came from: `env` (`$SHELL`, a bare name looked up on `PATH`), `login` (the login shell from the user database, as `$SHELL` was unset or not an executable file), or `default` (`/bin/sh`, as neither was usable) |
| `utf8` | bool | Whether the locale commands get names UTF-8: `LC_ALL`, else `LC_CTYPE`, else `LANG`, the first that is set, after `[shell_environment_policy]` |
| `path_minimal` | bool | Whether the `PATH` commands get has none of the user's tool directories that exist: `/opt/homebrew/bin`, `/usr/local/bin`, `~/.local/bin`, `~/go/bin`, `~/.cargo/bin` |

The text after the front matter may be Markdown. It is data: uah inserts the [placeholders](#placeholders) and sends it, and never runs it.

The schema is strict. An unknown key, a value of the wrong type, an unknown `when` value, or an unknown placeholder is an error: `uah context` lists the file with the error, and the module never applies. Limits: a file is at most 16 KiB, its front matter at most 2 KiB, a list at most 16 values, and a check word at most 256 bytes. uah reads at most 64 files from a folder.

## When a module applies

The conditions combine as follows:

- Every key that is present must match: `when`, `files`, and `check` are combined with AND, and so are the keys inside `when`.
- Inside one list, one value is enough: `shell: [fish, nu]` matches fish or nushell (OR). The same holds for `files`: one path must exist.
- An absent key, or an empty list, matches every session.

uah evaluates a module in this order and stops at the first step that fails:

1. The file parses.
2. The module is enabled.
3. It is trusted (a project's module only).
4. `when` matches.
5. One of the `files` exists in the workspace.
6. Each placeholder in the text has a value in this session.
7. The `check` exits 0.

The cheap steps come first, so a check runs only for a module that would otherwise apply, and at most once per session for the same command. All of it happens once, when the session starts.

## Placeholders

The text may use these placeholders. Each is replaced with the session's value as plain text, in one pass, so a value that looks like a placeholder stays as it is.

| Placeholder | Value |
| --- | --- |
| `{{shell}}` | The shell's path, such as `/bin/zsh` |
| `{{shell_name}}` | The shell's family as the text names it, such as `zsh` |
| `{{os}}` | The OS's name: macOS, Linux, FreeBSD, Windows, and so on |
| `{{goos}}` | Go's name for the OS, such as `darwin` |
| `{{mode}}` | The sandbox mode, or `none` |
| `{{tmpdir}}` | The session's private temporary directory; none without a sandbox |
| `{{workspace}}` | The workspace's path |
| `{{agent}}` | `main` or `subagent` |
| `{{instruction_files}}` | The loaded instruction files as a `- ` list; none when none were loaded |
| `{{omitted_instruction_files}}` | The instruction files the system prompt leaves out on purpose, as a `- ` list; none when it leaves none out |
| `{{max_output_length}}` | The Bash tool's default output cap |
| `{{locale}}` | The variable that sets the character set and its value, such as `LANG=C`, or `LC_ALL, LC_CTYPE, and LANG unset` when none is set |
| `{{missing_path_dirs}}` | The user's tool directories that exist but `PATH` lacks, comma-separated; none when `PATH` has one of them |

A module whose placeholder has no value in the session does not apply. For example, a module that uses `{{tmpdir}}` does not apply in yolo mode. Every `{{` in the text must start one of these placeholders.

## Where modules come from

| Source | Where | How it is used |
| --- | --- | --- |
| Built-in | Embedded in uah, at paths such as `environment/fish` and `sandbox/tmpdir` | In its block |
| Your override | `~/.uah/prompts/context/<path>.md` | In place of the built-in of the same path, for as long as the file exists |
| Library | Embedded in uah, at `library/<id>` | As a block of its own, once `[context] modules` names its id |
| Your modules | `~/.uah/prompts/context.d/<id>.md` | As a block of its own |
| Project modules | `<workspace>/.uah/context.d/<id>.md` | As a block of its own, once `uah context trust` approves the file as it is |

`~/.uah` is `$UAH_HOME` when that is set; the prompts folder is next to the user configuration file.

Extra blocks follow the built-in blocks: the library's first, then yours, then the project's, each folder in file-name order. An extra module's id must be unused by the library and by the extras read before it. A second module with an id that is taken is listed with an error and does not apply. Give your own modules a prefix, such as `my-` or your team's name, so that a library module added in a later version cannot take the id.

### Overrides and updates

An override replaces the built-in module completely, for as long as the file exists. When a later version of uah improves that module, the improvement does not reach you. Override only the modules you change, and delete an override when you no longer need it.

> [!WARNING]
> A file under `~/.uah/prompts/context/` pins its module: uah updates to that module stop reaching you. `uah prompts status` lists your overrides and flags copies that are identical to the built-in, and `uah prompts prune` deletes those copies.

`uah prompts init` does not create overrides. It writes every built-in module to `~/.uah/prompts/context.defaults/`, a reference folder that uah never reads. To change a module, copy its file from there to the same relative path under `~/.uah/prompts/context/` and edit the copy. Each run of `uah prompts init` rewrites the reference copies with the running version's text.

An override that does not parse, or whose path names no built-in, is listed with its error by `uah context` and `uah prompts status`, and the built-in stays in use. An override may also replace a library module (`library/<id>`).

## The library

The library ships with uah, turned off. Each library module has `enabled: false`, a `files` condition, and a `check`, so it applies only where you turn it on, the project has the files, and the tool is installed. New versions of uah can add library modules; they reach you because nothing on disk overrides them.

| Id | What it tells the model | Files (one must exist) | Check | Turn it on |
| --- | --- | --- | --- | --- |
| `go` | Set `GOCACHE` and `GOTMPDIR` under `$TMPDIR` when the build cache cannot be written; run one test with `-run '^TestName$'`; run `go vet ./...` before finishing | `go.mod`, `go.work` | `go version` | `[context] modules = ["go"]` |
| `python-venv` | Use the project's virtualenv, never `pip install` into the system, and point pip's cache at `$TMPDIR` | `pyproject.toml`, `requirements.txt`, `setup.py` | `python3 --version` | `[context] modules = ["python-venv"]` |
| `node` | Use the package manager the lockfile names, set `npm_config_cache` under `$TMPDIR`, and run one test file | `package.json` | `node --version` | `[context] modules = ["node"]` |
| `rust` | Use `cargo check` before `cargo build`, filter tests by name, and set `CARGO_HOME` under `$TMPDIR` when needed | `Cargo.toml` | `cargo --version` | `[context] modules = ["rust"]` |
| `docker` | The daemon's socket counts as network in the sandbox, so ask for escalation from the first try; use `docker compose` | `Dockerfile`, `compose.yaml`, `compose.yml`, `docker-compose.yml`, `docker-compose.yaml` | `docker --version` | `[context] modules = ["docker"]` |
| `git-lfs` | LFS files may be pointer files; `git lfs ls-files` lists them, and `git lfs pull` needs escalation for the network | `.gitattributes` | `git lfs version` | `[context] modules = ["git-lfs"]` |

`[context] modules` takes a list, so `modules = ["go", "docker"]` turns on both. Set it in the user file for every workspace, or in a trusted project's `.uah/config.toml` for one. `uah prompts show context/library/<id>` prints a module's full text.

## Security

Modules are text, and uah never runs a module's text. Only a module's `check` runs, and it runs with these limits:

- No shell. `argv[0]` must be a name that uah finds on `PATH`, or an absolute path; a relative path with a `/` is an error. The other words are passed as they are, so `;`, `|`, and `$(...)` are plain characters.
- In a sandbox: read-only, with no network, in the workspace. Where the system has no sandbox, checks do not run, and a module with a check does not apply.
- Its environment is only `PATH` and `HOME`. It has no stdin, its output is discarded, and it is killed after 2 seconds.
- It runs only when every earlier condition matched, and at most once per session for the same command.

A project's modules come from the repository, so uah treats them like a project's hooks. A project module is listed but not used, and its check never runs, until you run `uah context trust` in the workspace. Trust is recorded in `~/.uah/trusted-hooks.json`, keyed by the workspace, the module's path, and the SHA-256 of its content. When the file changes, it is untrusted again until you trust it again.

Your own modules in `~/.uah/prompts` need no trust: they are yours.

## Commands

| Command | Does |
| --- | --- |
| `uah context` | Lists every module: its block, path, source, state (`on`, `off`, `untrusted`, `error`), whether it applies to a new main session in this workspace, and why. Checks run as a session would run them |
| `uah context --show` | Also prints the prepared context of a new main session and of a read-only subagent |
| `uah context --json` | Prints the list as JSON; with `--show`, the two contexts too |
| `uah context trust` | Trusts the workspace's project modules as they are now |
| `uah prompts show context/<path>` | Prints a built-in module as uah ships it |
| `uah prompts init` | Writes the prompt files to `~/.uah/prompts` and the built-in modules to `~/.uah/prompts/context.defaults/` as a reference |
| `uah prompts status` | Lists your prompt files, your overrides (each marked edited, identical to the built-in, or not used because of an error), and your own modules |
| `uah prompts prune` | Deletes the overrides that are identical to the built-in; `--dry-run` only lists them |

`uah context` takes the same flags as a session, such as `--workspace`, `--sandbox`, and `--config`, so it shows what a session with those settings would get.

A row of `uah context`:

```text
BLOCK        MODULE            SOURCE                                   STATE  APPLIES  WHY
environment  environment/fish  builtin                                  on     no       when.shell: zsh is not fish
go           library/go        library                                  on     yes      applies
my-go        context.d/my-go   user ~/.uah/prompts/context.d/my-go.md   off    no       disabled (enabled: false; [context] modules can turn it on)
```

## Recipes

### Add your own module

1. Write `~/.uah/prompts/context.d/my-notes.md`:

   ```markdown
   ---
   id: my-notes
   description: How I like commits and tests
   when: {agent: [main]}
   ---
   Commit with a one-line subject in the imperative. Run the narrowest test that covers a change first.
   ```

2. Run `uah context` in a workspace and find the `my-notes` row. Its WHY column says `applies`, or the reason it does not.
3. Run `uah context --show` to read the message a new session gets.

### Enable a library module

1. Add to `~/.uah/config.toml`:

   ```toml
   [context]
   modules = ["go"]
   ```

2. Run `uah context` in a Go repository. The `library/go` row applies once `go.mod` or `go.work` exists and `go version` runs.

### Add a library-style module of your own

A module with `enabled: false` in your `context.d` behaves like a library module: it applies only where `[context] modules` names it. Use this for notes that suit some setups only.

1. Write `~/.uah/prompts/context.d/my-bazel.md`:

   ```markdown
   ---
   id: my-bazel
   description: Bazel, its cache in the sandbox and running one target
   files: [MODULE.bazel, WORKSPACE]
   check: [bazel, --version]
   enabled: false
   ---
   Bazel: pass --disk_cache=$TMPDIR/bazel when the cache cannot be written, and build one target, not //...
   ```

2. Turn it on where you want it: `[context] modules = ["my-bazel"]` in the user file, or in a trusted project's `.uah/config.toml` for that project only.

Your module adds to the library and replaces nothing, so uah updates still reach every built-in.

### Change one built-in module

1. Run `uah prompts init` once, or print the module with `uah prompts show context/environment/fish`.
2. Copy it to the same path under `context`:

   ```sh
   mkdir -p ~/.uah/prompts/context/environment
   cp ~/.uah/prompts/context.defaults/environment/fish.md ~/.uah/prompts/context/environment/fish.md
   ```

3. Edit the copy. Keep its `id`. `uah context` shows the row with `user (replaces the built-in)`.
4. To go back to the built-in, delete the file.

To turn a built-in module off, override it with `enabled: false`.

### Share a module with a project's team

1. Commit `<repository>/.uah/context.d/repo-tests.md`:

   ```markdown
   ---
   id: repo-tests
   description: How this repository runs its tests
   files: [Makefile]
   ---
   Run make test-unit for a quick check; make test runs the integration suite and needs Docker.
   ```

2. Each teammate runs `uah context` in the repository (the row is `untrusted`), reads the file, and runs `uah context trust`.
3. After each change to the file, each teammate trusts it again.

## Troubleshooting

`uah context` answers most questions: its WHY column gives the first condition that failed.

| WHY says | Meaning and fix |
| --- | --- |
| `disabled (enabled: false; ...)` | Add the id to `[context] modules`. A built-in block's module that you set to `enabled: false` stays off |
| `untrusted project module: ...` | Run `uah context trust` in the workspace, after reading the file |
| `when.<key>: ... is not ...` | The session does not match `when`. Check `--sandbox`, `$SHELL`, and the OS |
| `files: none of ... in the workspace` | None of the paths exists. Paths are relative to the workspace root |
| `{{name}} has no value in this session` | For example, `{{tmpdir}}` without a sandbox. Remove the placeholder or add `when: {sandbox: [read-only, workspace-write]}` |
| `check [...] failed: ...` | The command is not on `PATH`, exited non-zero, needed the network or a write, or took over 2 seconds. Without a sandbox, checks do not run at all |
| `error: ...` | The file does not parse, its id is not its file name, or its id is taken. The message names the problem |

Other problems:

- A change does not show in the running session: modules are read when a session starts. Start a new session; a resumed session keeps its first message.
- A uah update's new text does not show: an override pins that module. Run `uah prompts status`, and `uah prompts prune` for identical copies; delete an edited override if you no longer need it.
- A block ends with `(cut: ...)`: the block is over 4 KiB or the message is over 16 KiB. Shorten the module, or split it with narrower `when` and `files` conditions.
- To compare sessions with and without the message, use `--no-context-preparation`.

The agent can answer questions about this system too: uah ships a built-in skill, `uah-customization`, that describes the module format and points the model to these commands.
