<!-- memoria:section id="overview" files="contract.go prepare.go catalog.go" -->
# Context preparation

Context preparation gives a new session the facts its first turns would otherwise spend tool calls on. At the start of the session, adapters each write a short block about one part of the session: the environment, the sandbox, the workspace, the agent files, and the harness. The blocks' text is data: Markdown modules whose front matter says when each applies, which a user can replace and add to ([Modules](#modules)). uah sends the joined blocks once, as a developer message before the first user message. The model reads a developer message as the harness's, not the user's, so the block is no user turn and no request of its own: it goes with the first user message.

<!-- memoria:export id="summary" -->
Every new session starts with one developer message of prepared context, before the first user message. This includes a subagent's session. The message has the git branch, the status, and the tracked files. It names the loaded instruction files, so the model does not search for more. It also gives the shell's and the OS's traps, the sandbox's limits and the session's private `$TMPDIR`, and how to size the Bash tool's output. The text comes from Markdown modules: a file under `~/.uah/prompts/context/` replaces the built-in of its path, `~/.uah/prompts/context.d/` and a project's `.uah/context.d/` add modules, and `uah context` shows which apply and why. The system prompt does not change, and the message stays in the session's history, so the prompt cache holds. Resumed and forked sessions get no new message; a fork's first run gets one sentence naming its own `$TMPDIR`, since the copied message names its parent's. Turn it off with `context_preparation = false`, `--no-context-preparation`, or `UAH_CONTEXT_PREPARATION=off`.
<!-- /memoria:export -->

1. [The contract](#the-contract)
2. [The prepared block](#the-prepared-block)
3. [The adapters](#the-adapters)
4. [Modules](#modules)
5. [Tests](#tests)

The engine calls `Prepare` in `internal/engine/embedded/prepare.go`; the [engine README](../engine/README.md#context-preparation) describes when. This README is the implementation reference; the [user guide](../../docs/context-preparation.md) explains the feature to users, with recipes and troubleshooting.
<!-- /memoria:section -->

<!-- memoria:section id="contract" files="contract.go" -->
## The contract

`contract.go` holds the shared contract. Each adapter implements `Adapter`: `Name()` is the block's title, and `Prepare(ctx, Facts)` returns the block, or `""` when the adapter has nothing to say. `Facts` is what the engine knows when a session starts:

| Field | Value |
| --- | --- |
| `Workspace` | The session's working directory |
| `InstructionFiles` | The instruction files in the system prompt, in order, as `internal/app` loaded them (`embedded.Config.InstructionFiles`) |
| `Shell` | The shell commands run in: `$SHELL`, or `/bin/sh` |
| `GOOS` | The operating system, as `runtime.GOOS` names it |
| `Sandbox` | The sandbox mode of the permission mode when the session starts (`read-only`, `workspace-write`, or `""` with no sandbox, also when the system has none), its network access, and the session's private temporary directory |
| `Subagent` | True in a subagent's session |
| `MaxOutputLength` | The Bash tool's default `max_output_length`, which the engine passes from uah-core (40,000), or 0 when it is not known |

The text must be stable for the session: an adapter reads only the facts and the files, never the clock.

The package is a domain package ([architecture](../../docs/documentation/architecture.md#import-directions), rule 2): it imports no uah package and nothing of the runtime. The engine (`internal/engine/embedded/prepare.go`) fills `Facts`, including what only the runtime knows, such as `MaxOutputLength`, and gives the modules a `Checker` that runs checks in its sandbox. No adapter parses another package's text: the instruction files come as paths, not from the system prompt's headers. `Workspace` runs read-only git through `os/exec`, as the domain package `internal/gitdiff` does.
<!-- /memoria:section -->

<!-- memoria:section id="block" files="prepare.go" -->
## The prepared block

`Prepare(ctx, facts, adapters...)` runs the adapters at the same time and joins their blocks in the adapters' order:

```text
<context_preparation>
uah prepared this when the session started, so you need not look it up again. It describes the session as it began; files and git's state may change as you work.

## environment
Commands run in zsh (/bin/zsh -c) on macOS.
...

## sandbox
...

## workspace
...

## agent files
...

## harness
...
</context_preparation>
```

An adapter with nothing to say gets no section. Each block is cut to 4 KiB (`MaxAdapterBytes`), or to the adapter's own cap when it implements `Limited`. The whole message is cut to 16 KiB (`MaxBytes`). A cut ends at a line break with `(cut: the rest is over N bytes)`. When no adapter has anything to say, there is no message.

The engine sends it as a developer message (`core.RoleDeveloper`), so the TUI, `uah sessions show`, the auto-reviewer, and the agent benchmark tell it from the user's messages by its role: a `core.DeveloperMessage` event, a `developer` input in the session file. The TUI shows it as a one-line notice and `uah sessions show` as one line; the auto-reviewer and the benchmark's turn count leave it out. A session from before the developer role has it as a user message; `IsPrepared` recognizes it by its tag there, and the same places treat it the same way.
<!-- /memoria:section -->

<!-- memoria:section id="adapters" files="blocks.go environment.go workspace.go" -->
## The adapters

`Modules.Adapters()` gives the engine the adapters in this order. Each built-in block but `workspace` joins the text of its [modules](#modules) that apply, a line apart, in the order `blocks` (`blocks.go`) lists them; the table says what the modules there say. Then each extra module that applies is a block of its own, under its id.

| Adapter | Block |
| --- | --- |
| `Environment` (`environment`) | The shell that runs commands and the operating system, always, at least one line ("Commands run in bash (/bin/bash -c) on Linux."). Models write POSIX sh and GNU flags whatever the shell, so for a shell that is not POSIX (fish, nushell, xonsh, elvish, PowerShell, cmd, csh) it lists the constructs that break and what to write instead, or says to run one `sh -c '…'`. It also lists zsh's glob and word-splitting gotchas, the limits of macOS `/bin/bash` 3.2, and the flags that differ in the BSD tools on macOS and the BSDs. The shell's family comes from its base name. Modules `environment/intro`, `environment/<shell>` (`bash`, `sh`, `zsh`, `fish`, `nu`, `xonsh`, `elvish`, `pwsh`, `cmd`, `csh`, `other`), and `os/darwin`, `os/bsd`, `os/windows` |
| `SandboxNotes` (`sandbox`) | What sandboxed commands may write in the session's sandbox mode, where the session's private `$TMPDIR` is (writable in every mode, read-only included) and what it is for, such as `GOCACHE=$TMPDIR/go-build`. On macOS it adds what Seatbelt blocks that a model does not expect: `ps` and `pgrep` (`lsof` works) and, without network, local sockets such as docker's; in read-only with `/bin/bash`, that bash 3.2 ignores `$TMPDIR` for heredocs. The Bash tool's description already says to escalate network commands from the first try, so the block does not repeat it. Without a sandbox (yolo mode, or no sandbox on the system) the block is empty. Modules `sandbox/read-only`, `workspace-write`, `tmpdir`, `bash-heredoc`, `processes`, `local-sockets` |
| `Workspace` (`workspace`) | The git branch, `git status --short` (at most 20 lines), and `git ls-files` by top directory with file counts (at most 40 entries). Each git command has 2 seconds. Outside a git repository the block is empty. This block is code, not modules: it reports git's output under short labels |
| `AgentFiles` (`agent files`) | `Facts.InstructionFiles`, in order, with the statement that these are all of them, so there is no need to search for more AGENTS.md or CLAUDE.md files, and that their `@` lines are expanded in place. The [instructions loader](../instructions/README.md#includes) expands them in the system prompt. A session whose system prompt replaces uah's without them, such as `/review`'s reviewer, has them in `Facts.OmittedInstructionFiles` instead (the engine checks the system prompt for the project instructions' header), and the block names them as left out of the system prompt on purpose, for the model to read when the task needs them. Whether to load a skill is left to the model. Modules `agent-files`, `agent-files-omitted`, and `agent-files-none` |
| `Harness` (`harness`) | How to use the Bash tool's `max_output_length` (its default, `MaxOutputLength`, which the engine passes from uah-core: 40,000 characters, which this does not change): leave it unset when the whole output is needed, set it only for noisy commands and to what will be read, and narrow a command whose output was cut instead of running it again with a bigger limit. Module `harness/output` |
<!-- /memoria:section -->

<!-- memoria:section id="modules" files="module.go catalog.go blocks.go check.go listing.go overrides.go" -->
## Modules

Every text of the built-in blocks is a Markdown file under `context/`, embedded with `go:embed`: the blocks' modules and the library (`library/`). A block's code only computes the facts and joins the modules that apply, so the wording and the conditions live in the files. With the built-ins alone the blocks are byte for byte what round 1 wrote, for every shell, OS, and sandbox mode (a sandboxed session always has its `$TMPDIR`); `TestDefaultsMatchRound1` holds them to it. Round 1 had no text for Linux or for a sandbox without network (the Bash tool's description covers that), so there are no `os/linux` or `sandbox/no-network` modules; a user can add them in `context.d`.

### The format

A module is front matter between `---` lines, then the text:

```markdown
---
id: go
description: Go toolchain notes, caches in the sandbox and running one test
when: {shell: [fish], os: [darwin], sandbox: [read-only], agent: [main, subagent]}
files: [go.mod, go.work]
check: [go, version]
enabled: false
---
Go: when the build cache cannot be written in the sandbox, set GOCACHE=$TMPDIR/go-build ...
```

| Key | Value |
| --- | --- |
| `id` | Required: the file's base name without `.md`, lower-case letters, digits, `-` and `_`, at most 40 |
| `description` | Required: 1 to 200 characters, for `uah context` |
| `when` | The facts the session must have, each a list that one value must match, or absent for any: `shell` (`bash`, `zsh`, `sh`, `fish`, `nu`, `xonsh`, `elvish`, `pwsh`, `cmd`, `csh`, `other`), `shell_path` (an absolute path, such as `/bin/bash`), `os` (`runtime.GOOS`), `sandbox` (`read-only`, `workspace-write`, `none`), `agent` (`main`, `subagent`); and three booleans, `network` (the sandbox's), `instructions` (whether instruction files were loaded into the system prompt), and `instructions_omitted` (whether the system prompt leaves the instruction files out on purpose) |
| `files` | Workspace paths, one of which must exist: relative, inside the workspace (no `..`, not absolute), with `*` as the only pattern, which matches within one path element |
| `check` | A command as an argv list that must exit 0, such as `[go, version]`; see [Checks](#checks) |
| `enabled` | `false` for a module that applies only when `[context] modules` names its id; true by default |

The schema is strict: an unknown key, a value of the wrong type, or an unknown `when` value is an error (yaml.v3 with `KnownFields`). A module file is at most 16 KiB (`MaxModuleBytes`) and its front matter at most 2 KiB; a list has at most 16 values and a check word at most 256 bytes. A module that does not parse is listed with its error by `uah context` and never applies; the built-ins are pinned by `TestBuiltins`.

The text may use placeholders: `{{shell}}` (the path), `{{shell_name}}` (the family, as the text names it), `{{os}}` (macOS, Linux, …), `{{goos}}`, `{{mode}}` (the sandbox mode, or `none`), `{{tmpdir}}`, `{{workspace}}`, `{{agent}}`, `{{instruction_files}}` (a `- ` list), `{{omitted_instruction_files}}` (a `- ` list of the files the system prompt leaves out), and `{{max_output_length}}`. Every `{{` must start one of them, or the module does not parse. Rendering is one pass: each value goes in as plain text and is never read again, so a value that looks like a placeholder stays as it is. A module whose placeholder has no value in the session, such as `{{tmpdir}}` without a sandbox, does not apply.

### Where modules come from

| Source | Where | Used |
| --- | --- | --- |
| Built-in | Embedded, `context/<path>.md` | In its block. `uah prompts show context/<path>` prints one |
| User replacement | `~/.uah/prompts/context/<path>.md` (the prompts folder next to the user file) | In place of the built-in of the same path, the library's included, for as long as the file exists. One that does not parse, or names no built-in, is listed with its error and the built-in stays |
| Library | Embedded, `context/library/<id>.md`: `go`, `python-venv`, `node`, `rust`, `docker`, `git-lfs`, each `enabled: false` with `files` and a `check` | As a block of its own, once `[context] modules` names its id |
| User extra | `~/.uah/prompts/context.d/*.md` | As a block of its own |
| Project extra | `<workspace>/.uah/context.d/*.md` | As a block of its own, only once trusted |

An extra module's id must be unused by the library and the extras read before it (library, then user, then project); a second one with the same id is listed with an error. A user's module with `enabled: false` is a library module of the user's own: `[context] modules` turns it on, and it replaces nothing, so the library can grow in later versions and reach users whatever they added. At most 64 files are read from a folder. Extra blocks follow the built-in blocks, each under `## <id>`, cut to 4 KiB like any block, and the 16 KiB total still holds.

### Overrides and the reference copies

A replacement pins its module: while the file exists, a later version's text of that module does not reach the session. So `uah prompts init` writes no replacements: it writes every built-in to `~/.uah/prompts/context.defaults/` (`DefaultsDir`), which `Load` never reads, as a reference to copy into `context/`. `Overrides(userDir)` lists the files under `context/` with their errors and marks one `Pinned` when it is byte for byte the built-in; `Status.Pinned` carries the same to `uah context`. `uah prompts status` lists them, and `uah prompts prune` deletes the pinned ones.

### Checks

`Evaluate` decides in this order, so the cheap tests go first and a check runs only for a module that would otherwise apply: the module parses, is enabled, is trusted, matches `when`, matches `files`, has a value for each placeholder, and passes its check. A check never goes through a shell: `ExecChecker` finds `argv[0]` on `PATH` (or takes an absolute path; a relative path with a `/` is refused when the module is parsed), passes the other words as they are, so `;` and `$(…)` are plain text, and runs it with no stdin, its output discarded, and only `PATH` and `HOME` in its environment, killed after 2 seconds (`CheckTimeout`). The caller's wrapper puts it in the sandbox: the engine and `uah context` use a read-only sandbox with no network on the workspace. Without a sandbox (none on the system, or a nil wrapper) checks do not run, so a module with a check does not apply. Each check runs once per session's `Modules`. A module's text is never run.

### Trust

A project module is untrusted until `uah context trust` approves it, like a project hook (`uah hooks trust`): the entry goes to the hook trust file (`~/.uah/trusted-hooks.json`), keyed by the workspace and `TrustKey`, the module's path and the SHA-256 of its content, so a changed file needs trust again. An untrusted module is listed, says so, and its check never runs.

### `uah context`

`uah context` lists every module (built-in, library, user, project, and files that failed) with its block, source, state (`on`, `off`, `untrusted`, `error`), and whether it applies to a new main session in the workspace and why, such as `when.shell: zsh is not fish` or `files: none of go.mod in the workspace` (`Modules.Explain`). A pinned replacement's source says it is identical to the built-in. The session's facts come from the same settings a session would use (`app.PreviewContext`). `--show` prints the context a new main session and a read-only subagent would get, and `--json` prints the list (and with `--show` the two contexts) as JSON. `uah context trust` trusts the workspace's project modules as they are now.
<!-- /memoria:section -->

<!-- memoria:section id="tests" files="prepare_test.go agentfiles_test.go environment_test.go sandbox_test.go module_test.go round1_test.go" -->
## Tests

| Test | Pins |
| --- | --- |
| `TestPrepare` | The adapters' order, no section for an empty block, no message when every block is empty, the per-adapter cap and an adapter's own cap, and the total cap |
| `TestWorkspace` | No block outside a repository, and the branch, the status, and the listing inside one |
| `TestHarness` | The guidance names `max_output_length` and its default, and no default when none is given |
| `TestAgentFiles` | The instruction files listed in order and said to be all, and the note when there are none |
| `TestEnvironmentName`, `TestEnvironmentPrepare`, `TestShellClaims` | Each shell family's and OS's lines; the last runs each fish and zsh "fails / use instead" claim in the installed shell and skips a shell that is missing |
| `TestSandboxNotesName`, `TestSandboxNotesPrepare` | The block in each sandbox mode, with and without network and `$TMPDIR`, on macOS and Linux, the bash 3.2 heredoc line only where it applies, and no block without a sandbox |
| `TestParseModule` | The schema: an unknown key (also in `when`), a string where argv belongs, the id, the description, unknown `when` values, a relative check path, `files` outside the workspace or with patterns other than `*`, unknown placeholders and stray `{{`, and the size caps |
| `TestBuiltins` | Every built-in parses and belongs to a block, and the library ships turned off |
| `TestRender` | A value is inserted as text, and a placeholder without a value keeps the module out |
| `TestOverrides` | A user file replaces a built-in (the library's too); a broken one, or one for no built-in, is listed with its error and the built-in stays |
| `TestExtras` | `[context] modules` turns on a library module whose files and check match; user modules and `when.agent`; an untrusted project module is not used and its check never runs; a trusted one is; a taken id; a check runs once |
| `TestTrustKey` | The key changes with the content |
| `TestUserLibrary` | A user's module with `enabled: false` stays off until `[context] modules` names it, and replaces no built-in |
| `TestPinnedOverrides` | `Overrides` lists the replacements, a copy of a built-in or a library module as pinned, an edited one and a broken one not; `Explain` marks the pinned one |
| `TestExecChecker` | argv runs without a shell (`;` and `$(…)` reach the command as words, and nothing they name runs), `argv[0]` found on `PATH` before the sandbox wraps it, a deadline stopping the check as `CheckTimeout` does, no wrapper no check |
| `TestExplain` | The listing's order, blocks, and reasons |
| `TestDefaultsMatchRound1` | With the built-ins alone, each block and the whole message equal what round 1's code wrote (`testdata/round1.json`, recorded from that code): 124 cases over every shell family, macOS, Linux, FreeBSD, Windows, each sandbox mode with and without network, the agent files, and the harness |

`internal/app/context_test.go` pins `uah context`'s preview (the user's and project's modules, `[context] modules`, trust and a changed file) and a check in the real read-only sandbox, which cannot write the workspace; `cmd/uah/context_test.go` the command's table, `--json`, `--show`, and `trust`; `cmd/uah/prompts_test.go` that `uah prompts init` writes the modules to `context.defaults` and no replacement, `uah prompts show context/<path>` prints one, and `uah prompts status` and `prune` list and delete the replacements identical to the built-in. `TestUserGuide` keeps [the user guide](../../docs/context-preparation.md) complete: a library table row for each library module, and every key and placeholder; `internal/systemskills` holds the `uah-customization` skill to the same schema.
<!-- /memoria:section -->
