# uah architecture

uah is a pure core with well-organized infrastructure around it, not layered DDD. This page lists every package, says which package may import which, and gives the rules every change follows with their known exceptions.

1. [Packages](#packages)
2. [The runtime: uah-core](#the-runtime-uah-core)
3. [Import directions](#import-directions)
4. [Naming](#naming)
5. [Rules](#rules)
6. [Exceptions](#exceptions)
7. [Size and complexity](#size-and-complexity)

## Packages

| Package | Role |
| --- | --- |
| `internal/session` | The long-lived session: one goroutine owns settings, the queue, the live run, hooks, and one ordered event stream. |
| `internal/sessionfile` | The runtime's session file as a documented, versioned format, read by those rules with no runtime code: the header, items by `Sequence`, paging, and the last item. |
| `internal/store` | A rebuildable SQLite index of the run records (`<state>/uah.db`), reconciled against the files when it opens. |
| `internal/engine` | The seam between the session and how runs execute: the `Engine` and `Run` interfaces, their options and events, and the subagent and patch seams. |
| `internal/engine/embedded` | The one engine: it runs uah-core's packages in process as a uagent `harness.Backend`, with uah's tools, transport, compaction, fork, rewind, auto-review wiring, and session log. |
| `internal/engine/codexauth` | The ChatGPT login the openai-codex provider sends: the access token and account from the environment or Codex's `auth.json`, refreshed as Codex does. |
| `internal/instructions` | AGENTS.md discovery, the default system prompt, and the host prompt, following Codex. |
| `internal/config` | TOML configuration: the user file, its layers, and trusted project files. |
| `internal/config/tomledit` | Edits a TOML file in place and keeps its comments and formatting, for `uah mcp add` and `/config`. |
| `internal/home` | uah's home, `~/.uah` or `$UAH_HOME`, where uah keeps its own files, and `Variables`, the environment variables that change what uah does. Instruction files, the workspace, and Codex's credentials live elsewhere. |
| `internal/home/migrate` | Copies the folders earlier versions used into the home once, at startup. |
| `internal/hooks` | Hook contract, execution, and the trust store (commands, and the content of a local script a command runs). |
| `internal/mcp` | MCP servers in Codex's configuration format, through the official Go SDK: starting and watching them, naming and calling their tools, OAuth logins and their storage, and editing `[mcp_servers]` in the user file. |
| `internal/sandbox` | Sandbox policies and the sandboxing shell: Seatbelt on macOS, bubblewrap on Linux. |
| `internal/rules` | Codex's `.rules` files (Starlark `prefix_rule`) and command splitting for matching. |
| `internal/approval` | The approver: rules and the approval policy decide whether a command runs sandboxed, unsandboxed, or not, and ask the user through the session. |
| `internal/patch` | Codex's `apply_patch` format, ported: parsing, applying, and the diff of what a patch changed, with Codex's messages. |
| `internal/images` | Images pasted into the prompt: their placeholders, the tag lines a message carries, and the image store. |
| `internal/images/clipboard` | Reads an image from, and writes text to, the system clipboard through the system's own tools. |
| `internal/history` | The prompt history file, `<home>/history.jsonl`, in Codex's format: appends under a lock, the size cap, and reading it back for the TUI's ↑ and ctrl+r. |
| `internal/usershell` | A command the user types in the TUI's `!` shell mode: running it (outside the sandbox and the rules unless `user_shell_sandbox`), its bounded output, and Codex's `<user_shell_command>` record the agent sees. |
| `internal/agents` | Subagents behind the `engine.Subagents` seam: Codex's v1 tools, child sessions on the parent's engine, their limits, depth, approvals through the parent, SubagentStop hooks, resume, and Codex role files. |
| `internal/goal` | Codex's `/goal` as data: the goal, its statuses and checks, Codex's continuation and steering texts and their wrapper, the goal tools' definitions and results, and the words the TUI shows. Pure. |
| `internal/compaction` | Compaction the Codex way: the request rewrite, the summary call over any `llm.Adapter`, token estimates, the window table, and the compaction log. The engine decides when to compact. |
| `internal/compaction/eval` | Measures what a compaction strategy does to one model request. Pure. |
| `internal/compaction/evalrun` | Runs that evaluation on recorded sessions, for the hidden `uah compaction eval`. |
| `internal/contextprep` | Context preparation: the Markdown modules whose front matter says when they apply (built in with `go:embed`, a library, and the user's and the project's), the blocks that join them (the environment, the sandbox, the workspace, the agent files, the harness), and the one message that joins the blocks. Module checks run through a `Checker` the caller gives it, in a sandbox. |
| `internal/contextusage` | What fills the context window, for `/context`: the system prompt, instructions, skills, tools, and the conversation. Pure. |
| `internal/llmcall` | One model call outside the agent loop over any `llm.Adapter`, for summaries and reviews. |
| `internal/review` | The auto-reviewer: one model call over `internal/llmcall` judges an action that needs approval, with Codex's prompt, a fail-closed verdict, and a circuit breaker. No engine wiring. |
| `internal/codereview` | Codex's `/review`: the targets, the reviewer's prompts and rubric, and the findings; the session runs the reviewer through `internal/agents`. |
| `internal/gitdiff` | Read-only git for `/diff` and `/review`: the work tree's changes as display diffs, the branches, the recent commits, and a merge base. |
| `internal/cmdparse` | What a shell command does, for the TUI's tool lines: a port of Codex's `parse_command`, and uah's wrapper stripping, relative paths, and heredoc folding. Pure. |
| `internal/models` | The model catalog: the models the provider offers to the login, cached with its ETag, Codex's bundled list as the fallback, and did-you-mean suggestions. `modelstest` serves catalogs to tests. |
| `internal/usage` | The ChatGPT plan's rate limits on openai-codex, for `uah usage`, `/status`, the footer, and `uah doctor`. |
| `internal/tui/state` | The pure TUI model: a reducer from events and intents to state and effects. No I/O. |
| `internal/tui/render` | Pure drawing of state to lines, with a per-item cache. |
| `internal/tui/render/markdown` | The model's Markdown as terminal lines; a growing message re-renders only its last block. Pure. |
| `internal/tui/composer` | The prompt input: a multi-line editor with textarea's key map, word wrap by cells and grapheme clusters, dynamic height then scroll, and the real cursor's place. Pure. |
| `internal/tui/term` | uah's terminal layer: the message, command, and key types, the event loop that runs the TUI's model, the line renderer, and the terminal's modes (raw mode, the alt screen, queries, ctrl+g's release, restore on a panic or a signal). Only `term/uv.go` imports ultraviolet, for its input decoder. |
| `internal/tui/bubble` | The TUI's shell on `term`: keys to intents, effects to commands, and frames. |
| `internal/app` | Session setup: `Resolve` picks settings from flags, the resumed session, the configuration, and defaults with no I/O; `Explain` reports each effective value and its source; `Setup` loads files and builds the engine; `Doctor` runs the same steps as checks. |
| `cmd/uah` | The CLI: flags, `exec` (also `run`), `resume`, `sessions`, `hooks`, `context`, `config`, `doctor`, `mcp`, `models`, `usage`, `prompts`, `completion`, and the TUI launcher. |
| `testing` | Test support only: `harnesstest` (uagent's fake runner, the real `uah-core-runner`, `RunnerEngine`, a test-only engine that spawns either, isolated state, and `IsolatedMain` for a package's `TestMain`), `fakellm` (a scripted Responses API), `mcpserver` (a stdio MCP server), and `oauthserver` (an MCP server behind a small OAuth authorization server). |
| `tools` | Development harnesses outside the product: `agentbench` (the agent benchmark against Codex) and `perf` (the performance harness); see [tools](../../tools/README.md). |

## The runtime: uah-core

uah-core (`github.com/viktordanov/uah-core`) is uah's own runtime: the coordinator, the durable operations, the session store, and the Responses client. It is derived from [unreal-agent](https://github.com/unreallabsai/unreal-agent) (MIT License); the [engine README](../../internal/engine/README.md#uah-core) lists what changed since.

- A change to the runtime is made in uah-core and tagged there, and uah takes the tag in `go.mod`. uah does not patch uah-core at runtime and keeps no copy of its code.
- uah wires uah-core's packages itself, in `internal/engine/embedded`, as `uah-core-runner` wires them. The equivalence test (`TestEmbedded_MatchesTheRunner`) gives both the same script and requires the same events and session items.
- uagent (`github.com/viktordanov/uagent`) supplies the event types, the harness, the session lock, and the run records.

## Import directions

These rules hold for the code that is not test code. `go list -f '{{.ImportPath}}: {{.Imports}}' ./...` shows the graph.

1. **Leaves import no uah package:** `cmdparse`, `config/tomledit`, `engine/codexauth`, `goal`, `history`, `home`, `images`, `instructions`, `llmcall`, `patch`, `rules`, `sandbox`, `sessionfile`, `systemskills`, `tui/composer`, and `tui/render/markdown`.
2. **Domain packages import only leaves and each other:** `approval`, `hooks`, `mcp`, `compaction`, `contextprep`, `contextusage`, `gitdiff`, `codereview`, `review`, `models`, `usage`, `usershell`, `images/clipboard`, and `config`. None of them imports `engine`, `session`, `app`, or `tui`.
3. **`internal/engine` is the seam:** it imports domain types, and never an engine implementation, `session`, or `app`.
4. **`internal/session` imports the seam and domain packages,** never `engine/embedded`, `config`, `app`, or `tui`. It reaches the engine only through `engine.Engine`.
5. **`internal/engine/embedded` implements the seam.** It imports the seam and domain packages. Only `app` and the development harnesses (`compaction/evalrun`, `tools/perf`) import it.
6. **`internal/agents` and `internal/store` build on `session` and the seam.** `app` and `cmd/uah` use them, and `home/migrate` uses `store` to copy the index.
7. **The TUI goes one way:** `tui/state` imports `session`, the seam's event types, and domain types; `tui/render` imports `tui/state` and those types; `tui/bubble` imports both, `tui/composer`, `tui/term`, and the packages that do I/O. `state` never imports `render` or `bubble`, and `render` never imports `bubble`. `tui/term` imports no other uah package, and only its `uv.go` imports ultraviolet; nothing imports Bubble Tea.
8. **Only `internal/app` and `cmd/uah` wire:** `app` may import any package under `internal` except `tui`, and `cmd/uah` may import any. Nothing under `internal` imports `app` or `cmd`.
9. **`testing` and `tools` stay outside the product:** they may import `internal`, and no product package imports them.

Known exceptions, to remove when the code there next changes:

- `engine/embedded` imports `session` for `AdaptiveSteps` and `SubagentIDPrefix`, and `config` for `config.Dir`, where it finds skills.
- `internal/engine` decodes session-file lines (`ToolOutputFromItem` in `tooloutput.go`), which is `sessionfile`'s job.
- `cmd/uah` links `compaction/evalrun`, and through it `agents` and `embedded`, for the hidden `uah compaction eval`.

## Naming

- A package name is one lower-case word for what it holds, never `util` or `common`. A package that ports a Codex feature keeps Codex's name for it (`patch` for `apply_patch`, `rules`, `codereview` for `/review`).
- When two packages could share a name, the narrower one carries the qualifier: `contextusage` (the context window) beside `usage` (the plan's limits), and `codereview` (`/review`) beside `review` (the auto-reviewer). An importer that needs both aliases the general one by its meaning, as `app` imports `usage` as `planusage`.
- A file is named after its concern. Files that pair up are named for the side each holds: `compactremote.go` is the compactor's side of remote compaction, `remotecompact.go` the transport's.
- Events are named for what happened (`RunStarted`, `InputQueued`), TUI intents for what the user asks (`ToggleDetails`, `PickerMove`), and effects start with `Eff` (`EffSteer`).
- Error variables are `Err…` and error types `…Error` (golangci-lint's `errname`).
- Tests live in the external `<pkg>_test` package. A file that tests unexported code is named `*_internal_test.go`. Test helpers shared across packages go in `<pkg>test` (`modelstest`) or under `testing`.

## Rules

- `internal/tui/state` and `internal/tui/render` do no I/O; effects are values the shell runs.
- Session state is owned by the session goroutine; other goroutines talk to it through messages.
- Files are the source of truth for state: the runtime's session files, uagent's run records, and uah's sidecars; the SQLite index can be rebuilt from them (see `docs/design/state.md`). A run that fails to save its session file ends failed.
- Wrap errors with `fmt.Errorf("failed to <action>: %w", err)`, and log with `slog` instances to stderr or the TUI log file, never the global logger.
- Test through real code paths: `fakellm`, uagent's fake runner, the real `uah-core-runner` built from go.mod, and real sandboxes, instead of mocks. A fake behind a seam interface, such as `internal/session`'s fake `engine.Engine`, is allowed.
- No test reads the user's `~/.uah` or `~/.codex`, or takes their settings from the environment. A package whose tests can reach uah's home (`internal/home`, directly or through `internal/config` and `internal/app`) runs them through `harnesstest.IsolatedMain`, which sets `UAH_HOME`, `HOME`, and `CODEX_HOME` to a temporary directory, clears `home.Variables`, `XDG_CONFIG_HOME`, and `XDG_STATE_HOME`, and keeps Go's own folders (`GOPATH`, `GOMODCACHE`, `GOCACHE`, `GOENV`) where they were; `cmd/uah`'s `TestMain` calls the same `harnesstest.Isolate`, and the perf harness does the same.

## Exceptions

- **Codex's own messages.** Where uah ports a Codex feature, its errors keep Codex's text, so a model trained on Codex reads them as it expects: `internal/patch`'s messages, `models.UnavailableError` for `spawn_agent`, and `codexauth.ErrLoginExpired` (capitalized, with a `nolint:staticcheck`).
- **Errors passed on unchanged.** A function returns its callee's error unwrapped when the callee already names what failed or the caller wraps it, such as a transport's `RoundTrip`, a store's own errors, and `tomledit.Write`; a comment on the return says so. Errors written for the user as they are (`the edited configuration does not parse`) and sentinel annotations do not start with "failed to".
- **Diagnostics on the run's stderr.** The embedded engine writes the run's diagnostics with `fmt.Fprintf` to the stderr its harness gives it, which uagent saves as the run's `stderr.log`: a failed run's error, a `model_attempt` line per model request, and sandbox warnings. That file is a record of the run, not a log; the agent benchmark reads it.
- **Terminal output.** `cmd/uah` prints to the terminal, and `internal/tui` draws to it, on purpose.
- **Dispatch switches.** The functions under [Size and complexity](#size-and-complexity) exceed the complexity target.

## Size and complexity

Files stay under about 400 lines with one concern each, and functions under about 15 cyclomatic complexity. golangci-lint's `gocyclo` fails above 20 as a backstop, so only a runaway function stops CI; each function over 20 carries a `nolint:gocyclo` comment pointing here, and `nolintlint` fails a directive that silences nothing. There is no length limit (`funlen` is off): the long functions are these dispatch switches and straight setup sequences. The exceptions are dispatch switches over closed sets, where splitting would scatter one decision table:

- the TUI reducer's `onIntent`, `onEvent`, and `onRunEvent` (`internal/tui/state`), the shell's `onKey` and effect runner `run` (`internal/tui/bubble`), and `itemLines` (`internal/tui/render`);
- the patch parser's `updateLine` state machine (`internal/patch`, ported from Codex);
- `(*printer).print` in `cmd/uah/print.go`, one line of progress per event.

The menu's `onMenu`, the shell's `Update`, and the session's `loop` are dispatch switches too, at or under 20, so they carry no comment.

Lint runs for linux and darwin (the CI matrix; locally `GOOS=linux golangci-lint run ./...` and the same with `GOOS=darwin`), because the sandbox has build-tagged halves. Test files are not linted.
