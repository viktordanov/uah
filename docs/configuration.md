# Configuration reference

uah reads its configuration from TOML files and combines it with flags, the environment, and a resumed session. This page lists every key: its type, default, meaning, which files may set it, and how a layer or a project file merges with the files before it. `uah config` shows the effective value of each key for a workspace and where it came from.

1. [Files](#files)
2. [Precedence](#precedence)
3. [Keys](#keys): [model](#model), [sandbox and approvals](#sandbox-and-approvals), [review](#review), [compaction](#compaction), [instructions and skills](#instructions-and-skills), [hooks](#hooks), [MCP servers](#mcp-servers), [subagents](#subagents), [goals](#goals), [TUI](#tui), [projects](#projects)
4. [Environment variables](#environment-variables)
5. [uah config](#uah-config)
6. [Examples](#examples)

## Files

Everything uah reads and writes lives in its home, `~/.uah`, as Codex keeps `~/.codex`. `UAH_HOME` names another directory. The home holds the files below and the state: `sessions/`, `runs/`, the index `uah.db`, pasted `images/`, the model cache `models/`, `logs/`, and the sandbox's scratch files `sandbox/`. `--state-dir` (or `UAH_STATE_DIR`) moves the state elsewhere; `--config` (or `UAH_CONFIG`) names another user file.

The first time uah starts without `~/.uah` and without `UAH_HOME`, it copies the folders it used before: the configuration directory (`$XDG_CONFIG_HOME/uagent` or `~/.config/uagent`) and its part of the state directory it shared with uagent (`$UAGENT_STATE_DIR`, `$XDG_STATE_HOME/unreal-agent`, or `~/.local/state/unreal-agent`): sessions, run records, images, the model cache, and the index. It prints "uah: moved your config and sessions to ~/.uah (the old folders are untouched)". The old folders are only read. Once `~/.uah` exists, uah never copies again, and `uah doctor` says where the home came from.

| File | What it holds | Applies when |
| --- | --- | --- |
| `~/.uah/config.toml` | The user file: every key | Always. `--config` or `UAH_CONFIG` names another file |
| `~/.uah/config.d/*.toml` | Layers: every key, `[projects]` included | Always, each in lexical order after the user file. Files that do not end in `.toml` are skipped |
| The file `UAH_EXTRA_CONFIG` names | A layer: every key, `[projects]` included | When the variable is set, after `config.d`. A relative path is relative to the current directory, and a missing file is an error |
| `<workspace>/.uah/config.toml` | The project file: every key except `[projects]` | The user file or a layer has `[projects."<workspace>"]` with `trusted = true`. The path is the absolute workspace path, symlinks not resolved |
| `~/.uah/rules/*.rules` | The user's command rules (Starlark `prefix_rule`); "don't ask again" appends to `default.rules` | Always |
| `<workspace>/.uah/rules/*.rules` | The project's command rules | The workspace is trusted, with or without a project file |
| `~/.uah/prompts/context/<path>.md` | A user's replacement for a built-in [context module](#context-modules), such as `environment/fish.md`. It pins the module: uah updates to it stop reaching you while the file exists | When context preparation is on, in place of the built-in of the same path |
| `~/.uah/prompts/context.defaults/` | Reference copies of the built-in context modules, written by `uah prompts init` to copy from | Never read |
| `~/.uah/prompts/context.d/*.md` | The user's extra context modules | When context preparation is on |
| `<workspace>/.uah/context.d/*.md` | The project's context modules | When context preparation is on and `uah context trust` approved the file as it is now |
| `~/.uah/trusted-hooks.json` | The project hook commands `uah hooks trust` approved, by SHA-256, and the project context modules `uah context trust` approved | Written by uah; do not edit |
| `~/.uah/mcp-credentials.json` | MCP OAuth logins from `uah mcp login`, readable only by you (0600) | Written by uah when `mcp_oauth_credentials_store` is `file`, or `auto` without a usable OS keyring; do not edit |
| `~/.uah/AGENTS.md`, `$CODEX_HOME/AGENTS.md` | User instructions; see [Instructions and skills](../README.md#instructions-and-skills) | Unless `--no-instructions` or `[instructions] enabled = false` |
| `~/.uah/skills`, `$CODEX_HOME/skills`, `.agents/skills` | Skills; see [Instructions and skills](../README.md#instructions-and-skills) | Always |
| `~/.uah/skills/.system/` | uah's built-in skills, such as `uah-customization`, written by uah when a session starts | After every other skill folder, so a skill of the same name elsewhere wins; uah rewrites it, do not edit |
| `$CODEX_HOME/auth.json` (`~/.codex/auth.json`, or `OPENAI_CODEX_AUTH_FILE`) | The ChatGPT login from `codex login`. uah refreshes its token as Codex does and writes it back (0600), with a lock file beside it, `.auth.json.uah-lock`; see [the design](design/codex-auth.md) | openai-codex, unless `OPENAI_CODEX_ACCESS_TOKEN` is set, which uah never refreshes |

uah no longer reads a workspace's `.uagent` directory. When a workspace has `.uagent` and no `.uah`, uah and `uah doctor` show the command that moves it: `git mv .uagent .uah` in a repository, else `mv .uagent .uah`. uah never moves the files itself.

A missing file is not an error, except the file `UAH_EXTRA_CONFIG` names. An unknown key is, in any file, so a typo fails loudly. A project file with a `[projects]` table is an error.

The layers let another program add configuration without editing the user file: a terminal host writes its hooks into its own `config.d` file and starts a session with `UAH_EXTRA_CONFIG` pointing at that session's MCP servers and permissions. A layer's hooks run as written, like the user file's: a program that can write a layer can already write the user file, so there is no trust step.

## Precedence

Each value comes from the first of these that sets it:

1. A flag.
2. The flag's environment variable (see [Environment variables](#environment-variables)).
3. The resumed session (`--session`, `uah resume`, `uah run --last`): the `provider`, `model`, `effort`, `fast`, `adaptive_effort`, and `permission_mode` it last used, which its sidecar (`sessions/<id>.uah.json`) keeps whenever they change, and its workspace. A session from before uah kept them gives the provider, model, and effort of its last run.
4. The project file.
5. The file `UAH_EXTRA_CONFIG` names.
6. `~/.uah/config.d/*.toml`, the last in lexical order first.
7. The user file.
8. The default.

The exceptions, as the code applies them:

- A `--provider` flag that changes the provider, compared with the resumed session's, else the configured one, else openai-codex, drops the resumed and configured models. The model is then `--model`, or the provider's default (see `model` below).
- The workspace comes from `-C`, the resumed session, or the current directory; no file sets it.
- `--max-disk` (5G) has a default, but the default counts only when the flag is not given: the files come first.
- `--fast` given, even as `--fast=false`, wins. Otherwise the resumed session's fast mode wins, unless a `--provider` flag changes the provider. Otherwise `fast` is on when any file turns it on.
- The permission mode is yolo with `--yolo`, else `--sandbox` (or `UAH_SANDBOX`) as a mode, else the resumed session's unless it was yolo, else `permission_mode`, else `sandbox_mode` as a mode, else `workspace`. `sandbox_mode` follows from the mode. A project file's `sandbox_mode` does not override a user file's `permission_mode`, because `permission_mode` from any file comes first.
- `--no-instructions` turns instructions off whatever the files say; no flag turns them on over `enabled = false`.
- `--no-context-preparation` turns context preparation off; `UAH_CONTEXT_PREPARATION=on` turns it on over `context_preparation = false`.
- `project_doc_max_bytes`, from any file, wins over `[instructions] max_bytes` from any file.
- Keys without a flag come only from the files and the defaults.

Each layer, then the project file, merges into the files before it key by key, in one of four ways. The key tables below name the way for each key, in terms of a project file over the user file; a layer merges the same way.

| Merge | Rule |
| --- | --- |
| override | The project value replaces the user value when the project file sets it. An empty string, 0, or false in the project file counts as unset, except for the keys marked "can unset", whose explicit value always replaces |
| append | The project list follows the user list. For `[shell_environment_policy] set`, the project's variables are added and replace the user's of the same name |
| OR | True when either file says true; the project file cannot turn it off |
| replace by name | A project `[mcp_servers.<name>]` replaces the user's server of the same name whole; other servers stay |

## Keys

Every key may be set in the user file, in a layer, and in a trusted project file, except `[projects]`, which the project file may not set.

### Model

| Key | Type | Default | Flag, env | Merge | Meaning |
| --- | --- | --- | --- | --- | --- |
| `provider` | string | `openai-codex` | `--provider`, `UAH_LLM_PROVIDER` | override | The LLM provider: openai, openai-codex, openrouter, fireworks, or ollama |
| `model` | string | `gpt-6.1-sol` on openai-codex and openai when the login's model list has it; else `gpt-6-sol` on openai-codex, `gpt-6-astra` on openai, and none for the others | `-m`, `--model`, `UAH_LLM_MODEL` | override | The model ID. Codex rust-v0.159.1 ranks gpt-6.1-sol first, but OpenAI rolls it out by account, so without a named model uah asks the provider for its list (cached for five minutes) when the session opens. A list uah could not get counts as without it. `uah models` marks the default, and `uah config` settles it from the cached list |
| `effort` | string | `high` | `-e`, `--effort` | override | The thinking level: low, medium, high, xhigh, max, or ultra. Not every model takes every level (`uah models` lists each model's); a session warns when it opens with a level its model does not list, and `/effort` refuses one |
| `request_max_attempts` | integer | 10 | `--max-attempts`, `UAH_LLM_MAX_ATTEMPTS` | override | How many times a model request is sent before the run fails. A lost connection, a timeout, a 429, or most 5xx statuses are retried after 2 s, then 4, 8, and 16 s, and then every 30 s (less up to a fifth of jitter, or the server's `Retry-After` up to 30 s): 10 attempts wait about 3 minutes in all. The runner's own default is 5 |
| `max_disk` | size | `5G` | `--max-disk` | override | Stop a run when tool output exceeds this size (`500M`, `5G`, bytes without a suffix); `0` disables it |
| `fast` | bool | false | `--fast` | OR | Priority processing (`service_tier = "priority"`); needs the openai or openai-codex provider, and any other provider refuses it before the session starts |
| `web_search` | string | `live` | none | override | The provider's hosted web search tool, as Codex's key: `live` offers it on openai and openai-codex (other providers never get it), `disabled` does not. Codex's `cached` and `indexed` are errors: the runner sends the tool without Codex's access options, which the API treats as live search. The search runs on the provider's servers, so the sandbox's network rule does not apply; it is offered in every permission mode, as in Codex ([web search](design/web-search.md)) |
| `model_verbosity` | string | the model's `default_verbosity` | `--model-verbosity`, `UAH_MODEL_VERBOSITY` | override | How much the model writes: `low`, `medium`, or `high`, sent as the Responses API's `text.verbosity`, as Codex's key. As in Codex, only a model whose catalog entry has `support_verbosity` gets it: unset, it gets the entry's `default_verbosity` (`low` for every model in Codex's catalog at rust-v0.159.1, gpt-6.1-sol included); set, it gets this value. Any other model, or one no catalog entry describes, gets no `text` field, and a session that opens with `model_verbosity` set on such a model shows Codex's warning. `uah config` shows the value the session's model gets ("" for none) |
| `adaptive_effort` | string | `off` | `--adaptive-effort`, `UAH_ADAPTIVE_EFFORT` | override | Adaptive effort for new sessions: `off`, `1-step`, or `2-steps`. On, the model thinks less on follow-up turns: a request that only follows tool results goes one (`1-step`) or two (`2-steps`) effort levels below `effort`, never below low (at `high`, 2 steps is `low`). The first request and a request with a user message go at `effort`. A session keeps its own value, as it keeps its effort; `/adaptive`, alt+e, and `/config` change it for the current session from its next model request, and the footer marks the effort (`high↓`, `high⇊`; `high→low` while a lowered follow-up is out). Measured on the agent benchmark at high effort, at the same pass rate: 1 step cut wall time by about a quarter and cost by 16–21%; 2 steps cut wall time by about a third and cost by a quarter ([agent tuning](design/agent-tuning.md#lean-mode-rules)) |
| `context_preparation` | bool | true | `--no-context-preparation`, `UAH_CONTEXT_PREPARATION` (`on` or `off`) | override | Start each new session, subagents' included, with one developer message of [prepared context](../internal/contextprep/README.md) before the first user message: git's state and tracked files, the instruction files (said to be all of them), how to size Bash output, the shell's and OS's traps, and the sandbox. Resumed and forked sessions get none. Off, nothing is added; the agent benchmark turns it off to compare |
| `[tools.experimental_request_user_input]` `enabled` | bool | true | `UAH_REQUEST_USER_INPUT` (`on` or `off`) | override, can unset | The agent's question tool, `request_user_input`, as Codex's key (`experimental_request_user_input` in the `[tools]` table): in the TUI, the main agent stops to ask a few questions with options and waits for the answers ([questions](design/questions.md)). `false` stops offering it, and the default system prompt says to ask in the final message instead, as before the tool. `uah exec` never offers it. Set it in a `config.d` layer or the file `UAH_EXTRA_CONFIG` names to turn it off for one integration, such as a web terminal that cannot show the picker |
| `[context]` `modules` | list of strings | `[]` | none | override | The ids of [context modules](#context-modules) to turn on that ship turned off: the library's `go`, `python-venv`, `node`, `rust`, `docker`, and `git-lfs`, or a user's or project's module with `enabled: false` |

#### Context modules

The prepared context's text is in Markdown modules, each with front matter that says when it applies; the [context preparation guide](context-preparation.md) explains the format, the library, and recipes, and the [implementation reference](../internal/contextprep/README.md#modules) the code. uah ships the built-in blocks' modules and a library of modules that are off until `[context] modules` names them. A file under `~/.uah/prompts/context/` with a built-in's path replaces it for as long as it exists, so uah updates to that module stop reaching you. `uah prompts init` writes the built-ins to `~/.uah/prompts/context.defaults/`, a reference uah never reads, to copy from; `uah prompts status` lists the replacements and flags copies identical to the built-in, and `uah prompts prune` deletes those. Files in `~/.uah/prompts/context.d/` and `<workspace>/.uah/context.d/` add modules, each as its own block. A project's module is used only once `uah context trust` approved its content in that workspace, like a project hook. `uah context` lists every module and whether it applies to a new session in the workspace and why, `--show` prints the context a new main session and a read-only subagent get, and `--json` prints both as JSON.

### Sandbox and approvals

| Key | Type | Default | Flag, env | Merge | Meaning |
| --- | --- | --- | --- | --- | --- |
| `permission_mode` | string | `workspace` | `--sandbox`, `UAH_SANDBOX` (as a mode); `--yolo` | override | The permission mode: `read-only`, `workspace`, or `auto`. It sets the sandbox and who decides what needs approval (table below), and it wins over `sandbox_mode`. shift+tab in the TUI cycles `read-only`, `workspace`, and `auto`, and `yolo` after them with `--yolo`. `yolo` is not a value for a file: only `--yolo` starts it, each time |
| `sandbox_mode` | string | `workspace-write` | `--sandbox`, `UAH_SANDBOX` | override | `read-only` or `workspace-write`, as Codex names them. Without `permission_mode`, it picks the mode of the same sandbox: `read-only` or `workspace`. Codex's `danger-full-access` is not a value: no sandbox is yolo mode, which only `--yolo` gives |
| `approval_policy` | string | `on-request` | `--ask`, `UAH_ASK` | override | Who answers an escalation or a `prompt` rule: `on-request` asks the user (headless runs deny), `never` denies. In a file, Codex's `on-failure` means `on-request` |
| `approvals_reviewer` | string | `user` | none | override | Who answers an escalation in the read-only and workspace modes: `user` asks you; `auto_review` lets the auto-reviewer judge first and asks you only when it leaves the decision to you. Auto mode always uses the auto-reviewer |
| `user_shell_sandbox` | bool | false | none | OR | Run the commands you type in the TUI's shell mode (`!`) like the agent's: in the permission mode's sandbox, refused by `forbidden` rules, and outside the sandbox for `allow` rules. Off, they run as your own commands, outside the sandbox and the rules, as in Codex and Claude Code ([shell mode](design/shell-mode.md)) |

The permission modes:

| Mode | Sandbox | Escalations and `prompt` rules |
| --- | --- | --- |
| `read-only` | `read-only` | Ask you (the auto-reviewer first only with `approvals_reviewer = "auto_review"`) |
| `workspace` (default) | `workspace-write` | Ask you (the auto-reviewer first only with `approvals_reviewer = "auto_review"`) |
| `auto` | `workspace-write` | The auto-reviewer decides, also with `approvals_reviewer = "user"`. The user is not asked; a decline reaches the model with the reviewer's reason |
| `yolo` | none | Nothing asks: escalations, `prompt` rules, patches, and MCP tools with an approval mode all run, without the auto-reviewer or PermissionRequest hooks; `forbid` rules still refuse, also a command with a redirect, a subshell, or a variable that runs or may run a forbidden command, and a patch that a rule on `apply_patch` or `apply_patch <path>` forbids (also in the other modes, where a patch inside the writable roots asks no one). Only `--yolo` (alias `--dangerously-bypass-approvals-and-sandbox`, Codex's name) starts it, with no `--sandbox` or `--ask`. It adds `yolo` after `auto` in the shift+tab cycle, and a resumed session stays in it only when `--yolo` is given again |

`approval_policy = "never"` still denies whatever needs approval, in every mode but yolo, unless a PreToolUse hook allowed the call. A mode change reaches a live run from its next command and model request.

`[sandbox_workspace_write]` configures the `workspace-write` mode:

| Key | Type | Default | Merge | Meaning |
| --- | --- | --- | --- | --- |
| `network_access` | bool | false | OR | Sandboxed commands may use the network |
| `writable_roots` | list of strings | `[]` | append | More writable directories. `~` is the home directory; relative paths are relative to the workspace |

`[approvals]` lists command prefixes, such as `"git status"`, besides the rules files:

| Key | Type | Default | Merge | Meaning |
| --- | --- | --- | --- | --- |
| `allow` | list of strings | `[]` | append | Commands that start with one of these run outside the sandbox without asking |
| `forbid` | list of strings | `[]` | append | Commands that start with one of these never run, also in a pipeline, a subshell, a substitution, or with a redirect. A command that may run one, because a word it needs is a variable, a glob, or a substitution, is refused too |

`[shell_environment_policy]` is Codex's filter for the environment commands get. Empty, commands get the whole environment:

| Key | Type | Default | Merge | Meaning |
| --- | --- | --- | --- | --- |
| `inherit` | string | `all` | override | The starting set: `all`, `core` (Codex's basic variables), or `none` |
| `ignore_default_excludes` | bool | true | override, can unset | `false` drops variables whose names match `*KEY*`, `*SECRET*`, or `*TOKEN*` |
| `exclude` | list of patterns | `[]` | append | Variables to drop. Patterns ignore case; `*` matches any run of characters and `?` one |
| `include_only` | list of patterns | `[]` | append | When set, only matching variables stay, applied last |
| `set` | table of strings | `{}` | append | Variables to set, after `inherit`, the default excludes, and `exclude` |

### Review

`[review]` configures the auto-reviewer's model call ([approvals](../internal/approval/README.md)):

| Key | Type | Default | Merge | Meaning |
| --- | --- | --- | --- | --- |
| `model` | string | `codex-auto-review` on openai-codex, else the session's model | override | The review model, on the session's provider |
| `effort` | string | `low` | override | The review effort: low, medium, high, xhigh, or max |
| `timeout` | duration | `90s` | override | The limit for one review; a review that times out denies |
| `policy_file` | path | Codex's review policy | override | A file whose text replaces the review policy, as Codex's `[auto_review] policy` does inline. The fixed framing and the answer format stay. An absolute path or one under `~/`; a missing or empty file stops the session from starting |

`review_model` is a top-level key, Codex's, for the TUI's `/review` and `uah review` (a code review by a read-only subagent), not for the auto-reviewer above:

| Key | Type | Default | Merge | Meaning |
| --- | --- | --- | --- | --- |
| `review_model` | string | the session's current model, as Codex | override | The model `/review` runs its reviewer on, on the session's provider, with the session's effort ([the review design](design/review.md)) |

`uah prompts init` writes the built-in prompts to `~/.uah/prompts` as a starting point: the review policy (`review.md`), the summary prompt (`compact.md`), uah's default system prompt (`system.md`), Codex's unmodified prompt (`system-codex.md`), and the runner's short host prompt (`system-runner.md`). It prints the `policy_file`, `experimental_compact_prompt_file`, and `model_instructions_file` lines that use them, with the lines for `system-codex.md` and `system-runner.md` commented out, and it overwrites only with `--force`. `uah prompts show <name>` prints one: `compact`, `system`, `system-codex`, `system-runner`, or `review`. `uah prompts init` also writes the [context modules](#context-modules) to `~/.uah/prompts/context.defaults/` as a reference that uah never reads, and overwrites those copies each time; it writes nothing under `~/.uah/prompts/context/`. `uah prompts show context/<path>` prints a module, such as `context/environment/fish`. `uah prompts status` lists the prompt files (absent, identical to the built-in, or edited), the context replacements, and the user's own modules; `uah prompts prune` deletes the replacements identical to the built-in (`--dry-run` lists them).

### Compaction

| Key | Type | Default | Merge | Meaning |
| --- | --- | --- | --- | --- |
| `auto_compact_percent` | integer 0–100 | 90 | override, can unset | Compact before a model request once the context in use (the last response's tokens plus an estimate of what was added since) reaches this share of the context window; 0 turns automatic compaction off |
| `model_context_window` | integer | the model catalog (the provider's list, else Codex's bundled one), else 272,000 | override | The context window in tokens, for compaction and the context meter |
| `model_auto_compact_token_limit` | integer | none | override | Codex's key: compact once the context in use reaches this many tokens, when that comes before `auto_compact_percent` of the window. It only lowers the limit, as in Codex; `auto_compact_percent = 0` still turns automatic compaction off. `/context` shows the rest of the window as the buffer |
| `compact_model` | string | the session's current model, as Codex | override | The model that writes the summary, on the session's provider. A model with a smaller window gets the history trimmed from the oldest item to fit |
| `compact_effort` | string | the session's current effort | override | The summary call's effort: low, medium, high, xhigh, max, or ultra |
| `compact_prompt` | string | uah's: Codex's summary prompt in fixed sections (Goal, Constraints, Decisions, State, Errors, TODOs, Next) | override | Codex's key: the prompt the summary call ends with. Surrounding whitespace is trimmed; empty means the default |
| `experimental_compact_prompt_file` | path | none | override | Codex's key: a file whose text is the summary prompt, when `compact_prompt` is not set. An absolute path or one under `~/`; a missing or empty file stops the session from starting |
| `compact_user_message_max_tokens` | integer | 20000, at most a quarter of the window | override | The cap on user messages a compaction keeps word for word, newest first; the one that crosses it is shortened in the middle. Codex fixes it at 20,000 (`COMPACT_USER_MESSAGE_MAX_TOKENS`); uah's default is at most a quarter of the window, so a small model's compacted context is not mostly old messages. A compaction saves the cap it used, so changing it affects later compactions only |

| `compact_elide_after_calls` | integer | 10 | override | Before an automatic summary, replace the tool outputs this many calls old, and outputs over 2,000 tokens three calls old, with a short stub (tool, command, exit code, size); when that frees enough, no summary runs. SkillUse outputs stay. 0 turns it off |

| `compact_keep_recent_calls` | integer | 5 | override | A summary leaves the last this many tool calls, with their outputs and the model's output around them, word for word after it; when they take more than a quarter of the window, the summary covers them too. 0 summarizes everything, as Codex does |

| `remote_compaction` | boolean | true | override | On openai and openai-codex, compact as Codex does: the provider turns the history into an encrypted item that later requests send in its place, after the kept user messages (up to 64,000 tokens) and before uah's ledger. A failed remote compaction, a `/compact` with focus instructions, and every other provider use the summary. Codex has no key for it (its `remote_compaction_v2` feature is removed and always on) |

`/compact <instructions>` adds focus instructions to the prompt for that one summary, as in Claude Code: `/compact keep the failing test names`.

### Instructions and skills

| Key | Type | Default | Flag | Merge | Meaning |
| --- | --- | --- | --- | --- | --- |
| `model_instructions_file` | path | uah's default prompt | none | override | Codex's key: a file whose text replaces the base instructions. AGENTS.md files still follow it. A relative path is relative to the file that sets it; a missing or empty file stops the session from starting |
| `project_doc_fallback_filenames` | list of strings | `[]` | none | override, can unset | File names to read in a directory without `AGENTS.override.md` or `AGENTS.md`; `["CLAUDE.md"]` reads Claude Code's files |
| `project_root_markers` | list of strings | `[".git"]` | none | override, can unset | Names that mark the project root, where discovery starts; `[]` reads the workspace only |
| `project_doc_max_bytes` | integer | 32768 | none | override | The size cap for all instruction files together; wins over `[instructions] max_bytes` |

`[instructions]`:

| Key | Type | Default | Flag | Merge | Meaning |
| --- | --- | --- | --- | --- | --- |
| `enabled` | bool | true | `--no-instructions` turns it off | override, can unset | Load AGENTS.md files into the system prompt |
| `max_bytes` | integer | 32768 | none | override | The size cap, when `project_doc_max_bytes` is unset |

Discovery order and the skill folders are in the README's [Instructions and skills](../README.md#instructions-and-skills).

#### The system prompt from a file

`model_instructions_file` follows Codex rust-v0.156.1:

- **Replaces the base instructions.** Codex reads the file into `base_instructions`. The file wins over the inline `instructions` key and loses only to a session's own override (`codex-rs/core/src/config/mod.rs` lines 3917–3929). The key's comment says that the text overrides the model's built-in instructions (`codex-rs/config/src/config_toml.rs` lines 257–261). In uah, the text replaces uah's default prompt, `system.md`. The context builder of uah-core (as of unreal-agent v0.1.1) still puts its own preamble (turns and asynchronous tool calls) and the skill list before it (`harness/contextbuilder/builder.go`). uah does not change the runner, so this preamble stays. The AGENTS.md instructions and the `<environment_context>` block follow the file, as they follow the default prompt without it. Subagents get the same text. `/context` counts the file as the system prompt.
- **Resolves a path like the other paths in a config file.** The key is an `AbsolutePathBuf`. `~` and `~/` expand to the home directory. A relative path is resolved against the directory of the config file that sets it (`codex-rs/utils/absolute-path/src/lib.rs` lines 27–56 and 392–401, and the base directory in `codex-rs/config/src/loader/layer_io.rs` lines 197–203). A trusted project's `.uah/config.toml` resolves against `.uah`, and its value wins over the user file's.
- **Fails on a missing or empty file.** Codex trims the text and stops with an error when the file cannot be read or is empty (`try_read_non_empty_file`, `codex-rs/core/src/config/mod.rs` lines 4461–4490). uah does the same when a session starts, and `uah doctor` reports the error as `system prompt`.

Codex's inline `instructions` string is not supported, because uah's `[instructions]` table already uses that name. The other prompt keys (`policy_file`, `experimental_compact_prompt_file`) still need an absolute path or one under `~/`.

#### Codex's prompt

uah's default system prompt is Codex's base instructions for gpt-6.1-sol with nine small changes (`internal/instructions/default_prompt.md`). At rust-v0.159.1, Codex keeps a separate prompt for each model in the catalog (`model_messages.instructions_template` in `codex-rs/models-manager/models.json`) and sends it as literal text. The Markdown prompts in `codex-rs/core` (`gpt_5_codex_prompt.md`, `gpt_5_2_prompt.md`, `gpt-5.2-codex_prompt.md`, and others) are no longer read. A model that is not in the catalog gets `codex-rs/models-manager/prompt.md`. uah starts from the template of gpt-6.1-sol, because gpt-6.1-sol is uah's default model on openai-codex; a login that runs gpt-6-sol until gpt-6.1-sol reaches it gets the same prompt. The templates of the other GPT-6 and GPT-5.x models differ from it in places, such as the personality and writing style sections.

The changes fit the prompt to uah's tools; the [system prompt record](design/system-prompt.md) lists each one with its reason:

| Codex's prompt says | uah's default prompt says |
| --- | --- |
| "You are Codex, an agent based on GPT-6" | "You are uah, a coding agent in the user's terminal" |
| `exec_command` with a `cmd` argument, the shell | `Bash` with a `command` argument |
| `functions.exec` with `Promise.allSettled` to batch calls | Parallel tool calls in one response |
| `functions.request_user_input_async` | Ask in the `final` channel, which ends the turn, or, where a user answers (the TUI), with the blocking `request_user_input` and its options |
| Interactive visuals, Mermaid, inline visualizations | Codex's own terminal wording: ASCII diagrams, trees, and tables |
| Skills listed under `## Skills`, read through `skills.list` and `skills.read` | The runner's `<available_skills>` list and `SkillUse` |
| Apps in the `codex_apps` MCP server, `tool_search`, plugins | Removed; MCP tools keep their `mcp__<server>__<tool>` names |
| `$CODEX_HOME` | `$UAH_HOME` and `$CODEX_HOME` |

The `commentary` and `final` channels stay, because the runner keeps each message's Responses `phase` (`final_answer` marks the answer) and sends it back with the history. The prompt does not mention `apply_patch`, `ViewImage`, or the subagent tools; the model sees them in the tool list.

`uah prompts init` also writes `system-codex.md`, Codex's template word for word, and `system-runner.md`, unreal-agent-runner v0.1.1's short host prompt that uah used by default before. Nothing uses either file until `model_instructions_file` names it.

#### The environment context

uah ends the system prompt with Codex's `<environment_context>` block, in the format of `codex-rs/core/src/context/world_state/environment.rs` at rust-v0.156.1:

```xml
<environment_context>
  <cwd>/Users/me/code/proj</cwd>
  <shell>zsh</shell>
  <current_date>2026-09-29</current_date>
  <timezone>Europe/Berlin</timezone>
</environment_context>
```

- `cwd` is the session's workspace, and `shell` is the base name of the shell that `Bash` runs (`$SHELL`, else `/bin/sh`).
- `current_date` is the local date, and `timezone` is the IANA name from `$TZ` or the `/etc/localtime` link. As in Codex, an unknown zone gives `Etc/UTC` and the UTC date.
- The block follows `model_instructions_file` too, and it does not depend on `[instructions] enabled`.

Codex sends the block as a user message after the AGENTS.md message and sends an update when a value changes, such as the date at midnight. uah has one system message, so the block goes at its end, after the AGENTS.md files: the order is the same as Codex's. The values are fixed when the session opens. Every request of the session then sends the same system message and hits the prompt cache; a session that runs past midnight keeps the date it opened with. A resume on a later day sends the new date, which costs one cache miss. Codex also sends the sandbox's file system and network profile in the block; uah leaves those out, because the `Bash` tool's description already names the sandbox mode.

### Hooks

Each `[[hooks.<Event>]]` entry runs a command at an event. The events are `SessionStart`, `SessionEnd`, `UserPromptSubmit`, `PreToolUse`, `PostToolUse`, `Stop`, `SubagentStart`, `SubagentStop`, `PreCompact`, and `PermissionRequest`; the contract is in the README's [Hooks](../README.md#hooks). A subagent fires the tool events and `PreCompact` only, with `agent_id` and `parent_session_id` in the payload; the session events fire for the main session alone.

| Key | Type | Default | Meaning |
| --- | --- | --- | --- |
| `matcher` | string | every tool | A regular expression that must match the whole tool name, for the tool events (`PreToolUse`, `PostToolUse`, `PermissionRequest`) |
| `command` | string | required | The command, run with `/bin/sh -c` in the workspace |
| `timeout` | duration | `60s` | The limit for one run of the hook |

Merge: append, per event, the user file's hooks first, then each layer's, then the project file's. User and layer hooks run as written. Project hooks run only after `uah hooks trust` records their exact commands, for that workspace. A PreToolUse hook's `permissionDecision` `"allow"` approves the call without asking, in every mode and headless; a `forbid` rule still refuses it. `"deny"` and `"ask"` refuse the call.

### MCP servers

Each `[mcp_servers.<name>]` table is one server, in Codex's format, so a Codex section copies over ([MCP servers](../README.md#mcp-servers)). Merge: replace by name. A server has `command` (stdio) or `url` (streamable HTTP), not both.

| Key | Type | Default | Transport | Meaning |
| --- | --- | --- | --- | --- |
| `command` | string | none | stdio | The program to start |
| `args` | list of strings | `[]` | stdio | Its arguments |
| `env` | table of strings | `{}` | stdio | Variables to set; the server otherwise gets only basic variables such as `HOME` and `PATH` |
| `env_vars` | list of strings | `[]` | stdio | Variables passed through from uah's environment by name |
| `cwd` | string | the workspace | stdio | The server's working directory |
| `url` | string | none | HTTP | The server's endpoint |
| `bearer_token_env_var` | string | none | HTTP | The variable holding a bearer token |
| `http_headers` | table of strings | `{}` | HTTP | Headers to send |
| `env_http_headers` | table of strings | `{}` | HTTP | Headers whose values come from the named variables |
| `enabled` | bool | true | both | `false` does not start the server |
| `required` | bool | false | both | A run fails when the server does not start, instead of leaving it out |
| `startup_timeout_sec` | number | 30 | both | Seconds to wait for the server to start |
| `startup_timeout_ms` | integer | none | both | The same in milliseconds, when `startup_timeout_sec` is unset |
| `tool_timeout_sec` | number | 300 | both | Seconds a tool call may take |
| `enabled_tools` | list of strings | all | both | When set, only these tools are offered |
| `disabled_tools` | list of strings | `[]` | both | Tools not offered |
| `supports_parallel_tool_calls` | bool | false | both | Calls may overlap; otherwise they run one at a time |
| `default_tools_approval_mode` | string | `auto` | both | The approval mode for tools without their own |
| `tools` | tables | none | both | Per-tool settings, by tool name, below |

`[mcp_servers.<name>.tools.<tool>]` sets one tool's `approval_mode`: `auto` (ask unless the annotations say read-only, or non-destructive and closed-world), `prompt` (always ask), `writes` (ask unless read-only), or `approve` (never ask).

Three ways set these keys without editing by hand, each in the file that configures the server (the trusted project file when it has the server, else the user file), keeping its comments:

- `uah mcp add <name> --approve …` writes `default_tools_approval_mode = "approve"` for the new server.
- `uah mcp approve <name> [tool] --mode approve|prompt|writes|auto` sets the server's default, or one tool's mode. Without `--mode`, it prints the current modes.
- "Yes, and don't ask again for this tool" in the TUI's approval prompt writes `approval_mode = "approve"` for that tool, as Codex's "Allow and don't ask me again" does. The session stops asking at once.

A server that stops on its own restarts after 1, 2, 4, 8, and 16 s, at most 5 times in a row (a server that ran for a minute starts the count again); `startup_timeout_sec` bounds each attempt, and a call made meanwhile waits within its `tool_timeout_sec`. There are no keys for this.

OAuth for streamable HTTP servers, with Codex's keys. A server that answers 401 and advertises OAuth, at startup, on a later call, or on its background stream, needs `uah mcp login <name>`; until then it shows "needs login" in `/mcp` and `uah doctor`, and its calls fail with that instruction. A running session reconnects it at the next message (or `/mcp`) once a new login is stored. A server with `bearer_token_env_var` or an `Authorization` header never uses OAuth.

| Key | Type | Default | Meaning |
| --- | --- | --- | --- |
| `auth` | string | `oauth` | How uah authorizes; only `oauth` is supported (Codex's `chatgpt` and `ema_auth` need a Codex account). A server with another value does not start: it shows as failed with the reason, and the other servers start. `uah mcp list` and `get` show it with the reason, and `uah mcp login` refuses only it |
| `scopes` | list of strings | the scopes the server advertises | The scopes `uah mcp login` asks for; `--scopes` replaces them |
| `oauth_resource` | string | the server's own | The RFC 8707 resource sent with the authorization and token requests |
| `oauth.client_id` | string | none: uah registers a client dynamically | A client registered with the authorization server ahead of time, in `[mcp_servers.<name>.oauth]` |
| `oauth.callback_url` | string | `http://127.0.0.1:<port>/callback` | The redirect URI sent to the server; the listener still binds 127.0.0.1. Overrides `mcp_oauth_callback_url` |
| `oauth.callback_port` | integer | a port the OS picks | The listener's port. Overrides `mcp_oauth_callback_port` |

Top-level OAuth keys, merged as override:

| Key | Type | Default | Meaning |
| --- | --- | --- | --- |
| `mcp_oauth_credentials_store` | string | `auto` | Where logins are kept: `keyring` (the OS keyring, service "uah MCP Credentials"), `file` (`~/.uah/mcp-credentials.json`, 0600), or `auto` (the keyring, else the file, as Codex does) |
| `mcp_oauth_callback_port` | integer | a port the OS picks | The port `uah mcp login` listens on for the browser's redirect |
| `mcp_oauth_callback_url` | string | `http://127.0.0.1:<port>/callback` | The redirect URI sent to the authorization server, for a callback that reaches 127.0.0.1 some other way |

Codex keys uah does not support are errors: `bearer_token`, `http_headers_helper`, `environment_id`, `omit_tools_from`, `oauth.authorization_server_issuer`, `tools.<tool>.output_token_limit`, `env_vars` entries written as tables, and the top-level `tool_output_token_limit`.

### Subagents

`[agents]` ([README](../README.md#subagents)):

| Key | Type | Default | Merge | Meaning |
| --- | --- | --- | --- | --- |
| `enabled` | bool | true | override, can unset | Offer `spawn_agent`, `send_input`, `wait_agent`, `close_agent`, and `resume_agent` |
| `max_concurrent_threads_per_session` | int | 4 | override | Open subagents per session tree; Codex's `max_threads` is an alias |
| `max_threads` | int | none | override | Codex's older name for `max_concurrent_threads_per_session` |
| `max_depth` | int | 1 | override | 1 offers subagents, 0 turns them off. Subagents never start subagents: a value above 1 is used as 1, with a notice |
| `default_subagent_model` | string | the parent's model | override | Model for subagents a role or call does not set; on openai-codex it must be in Codex's model catalog, as `spawn_agent`'s `model` must |
| `default_subagent_reasoning_effort` | string | the parent's effort | override | Effort for subagents a role or call does not set |

There is no `default_subagent_service_tier`: Codex has no such key. Fast mode for subagents comes from a role's `service_tier`, or from the parent's `/fast`, which its children inherit.

Kinds of subagents (roles) are files in `~/.uah/agents/` and, for a trusted workspace, `<workspace>/.uah/agents/`, searched recursively: Markdown files with YAML front matter (`*.md`, as Claude Code's `.claude/agents/*.md`) and Codex role files (`*.toml`). A project file replaces a user file of the same name; in one directory, a Markdown file replaces a TOML file of the same name, with a notice. uah reads these keys and warns about the others:

| Markdown key | TOML key | Type | Meaning |
| --- | --- | --- | --- |
| `name` | `name` | string | The `agent_type` that selects the role. Required |
| `description` | `description` | string | When to use it; the `spawn_agent` description lists it. Required |
| the body | `developer_instructions` | string | Added to the agent's system prompt. Required |
| `nickname_candidates` | `nickname_candidates` | list of strings | Names for its agents instead of uah's list |
| `model` | `model` | string | The agent's model, on the parent's provider. `inherit`, and Claude Code's aliases (`sonnet`, `opus`, `haiku`, `fable`, with a notice), mean the parent's |
| `effort` or `model_reasoning_effort` | `model_reasoning_effort` | string | The agent's effort: `low`, `medium`, `high`, `xhigh`, `max`, or `ultra` |
| `fast` or `service_tier` | `service_tier` | bool or string | `true` or `"priority"` (or `"fast"`) runs its agents with priority processing (fast mode), when the provider offers it; `false` or `"default"` runs them without it; absent follows the parent |
| `tools` | `tools` | comma-separated string or list | The tools its agents are offered; absent offers every tool |
| `approve` | `approve` | list of strings | Commands and MCP tools its agents run without asking, within the permission mode |

`tools` takes uah's names (`Bash`, `ViewImage`, `SkillUse`, `apply_patch`, `mcp__<server>__<tool>`) and Claude Code's: `Edit`, `Write`, `MultiEdit`, and `NotebookEdit` are `apply_patch`, `Skill` is `SkillUse`, and `mcp__<server>` or `mcp__<server>__*` is every tool of a server. `Read`, `Grep`, `Glob`, and `LS` have no tool of their own in uah, whose agents read files with `Bash`; they are dropped with a notice. A list whose names all drop offers no tools. The spawn tools are never offered to a subagent.

`approve` takes command prefixes (`git diff`, or Claude Code's `Bash(git diff *)` and `Bash(git diff:*)`), `apply_patch` (or `Edit`, `Write`) for patches, and MCP tool names or `mcp__<server>` patterns. A pre-approved action answers the approval prompt for the agent: a command runs where an approved command would run (in the sandbox, or outside it for an escalation), and an MCP tool as with `approval_mode = "approve"`. It never widens the permission mode: an escalation in read only mode is still asked, a `forbid` rule still denies, and `approval_policy = "never"` still declines commands and patches (a pre-approved MCP tool runs, as a tool with `approval_mode = "approve"` does). Each simple command of a compound command must match.

A spawn call's `model` and `reasoning_effort` come before the role's, and the role's before `[agents]` defaults and the parent's. A role for fast reviews, in either format:

```markdown
---
name: fast-reviewer
description: Reviews a diff quickly for correctness bugs.
model: gpt-6-luna
effort: low
fast: true
tools: Bash
approve: [git diff, git log]
---

Review the diff you are given. List only real bugs, each with its file and line.
```

```toml
# ~/.uah/agents/fast-reviewer.toml
name = "fast-reviewer"
description = "Reviews a diff quickly for correctness bugs."
model = "gpt-6-luna"
model_reasoning_effort = "low"
service_tier = "priority"
tools = ["Bash"]
approve = ["git diff", "git log"]
developer_instructions = "Review the diff you are given. List only real bugs, each with its file and line."
```

### Goals

`/goal` keeps the agent working, run after run, until it marks the goal complete or a budget stops it ([README](../README.md#goals), [design](design/goal.md)). The keys are Codex's, with uah's cap on continuations:

| Key | Type | Default | Merge | Meaning |
| --- | --- | --- | --- | --- |
| `[features]` `goals` | bool | true | override, can unset | `/goal` and the goal tools (`get_goal`, `create_goal`, `update_goal`), as Codex's feature flag. `false` offers no goal tools, refuses `/goal`, and leaves a goal a session kept alone |
| `[goals]` `max_goal_token_budget` | integer | none | override | The most tokens a goal may have as its budget, and the budget of a goal set without one: `/goal`, or the model's `create_goal` without `token_budget`, as Codex's key. Tokens count as Codex counts them: input not read from the prompt cache, plus output. A goal over its budget stops as budget-limited and the model is told to wrap up |
| `[goals]` `max_continuations` | integer | 50 | override, can unset | uah's: the most runs uah starts on its own for one goal; then the goal stops as budget-limited, and `/goal resume` gives it as many again. 0 means no limit, as in Codex |

### TUI

`[tui]`:

| Key | Type | Default | Merge | Meaning |
| --- | --- | --- | --- | --- |
| `details` | bool | false | OR | Start in the detailed view; ctrl+t toggles it |
| `mouse` | bool | true | override, can unset | Report the mouse to the TUI: the wheel scrolls the transcript, and a drag, a double click, or a triple click selects its text and copies it to the clipboard (OSC 52 and pbcopy, wl-copy, or xclip); the terminal's own selection then needs Option (iTerm2, Terminal) or Shift held. `false` leaves the mouse to the terminal: it selects text as usual and its wheel sends ↑ and ↓, which recall earlier prompts on an empty composer and scroll the transcript on a draft of your own; shift+↑/↓ and pgup/pgdn always scroll. See the [selection design](design/selection.md) |
| `title` | bool | true | override, can unset | Show the session's state in the terminal's title: `uah · <workspace name>` when idle, `uah · working · <workspace name>` while the agent works, and `uah · approve? · <workspace name>` while an approval waits. `false` turns it off: uah sets no title |

### History

`[history]`, with Codex's keys, for `<home>/history.jsonl`: the prompts ↑ and ctrl+r recall in the TUI. The file holds every workspace's prompts; the TUI shows the session's workspace's only. Each prompt is one line, `{"session_id":…,"ts":…,"text":…,"workspace":…}`: Codex's `~/.codex/history.jsonl` format plus the session's workspace, an absolute path. A line without `workspace` shows in no workspace. The file is private (0600). Messages and `!` commands go in, as typed, with images as their `[Image #N]` placeholders; slash commands do not. See the [prompt history design](design/prompt-history.md).

| Key | Type | Default | Merge | Meaning |
| --- | --- | --- | --- | --- |
| `persistence` | string | `save-all` | override | `save-all` writes every prompt; `none` writes nothing, and ↑ still recalls what the file has, as in Codex. Any other value stops the TUI with an error |
| `max_bytes` | int | 8388608 (8 MiB) | override | The file's cap. Past it, the oldest prompts go until the file is at most 80% of the cap, as Codex trims; the newest prompt always stays. 0 means no cap, Codex's default |

### Projects

`[projects."<absolute workspace path>"]`, in the user file or a layer, not in the project file. Merge: replace by name, so a later layer's entry for a workspace replaces an earlier one's:

| Key | Type | Default | Meaning |
| --- | --- | --- | --- |
| `trusted` | bool | false | Apply the workspace's `.uah/config.toml` and `.uah/rules/*.rules` |

## Environment variables

| Variable | Flag | Config key | Meaning |
| --- | --- | --- | --- |
| `UAH_LLM_PROVIDER` | `--provider` | `provider` | The provider |
| `UAH_LLM_MODEL` | `--model` | `model` | The model |
| `UAH_LLM_BASE_URL` | `--base-url` | none | The LLM base URL |
| `UAH_LLM_MAX_ATTEMPTS` | `--max-attempts` | `request_max_attempts` | The attempts per model request. uah passes the resolved value to the runner's client, so the variable does not reach the runner directly |
| `UAH_SANDBOX` | `--sandbox` | `sandbox_mode` | The sandbox mode, and the permission mode of that sandbox |
| `UAH_ASK` | `--ask` | `approval_policy` | The approval policy |
| `UAH_ADAPTIVE_EFFORT` | `--adaptive-effort` | `adaptive_effort` | Adaptive effort: off, 1-step, or 2-steps |
| `UAH_CONTEXT_PREPARATION` | `--no-context-preparation` (off) | `context_preparation` | Context preparation: on or off |
| `UAH_REQUEST_USER_INPUT` | none | `[tools.experimental_request_user_input] enabled` | The agent's question tool: on or off |
| `UAH_MODEL_VERBOSITY` | `--model-verbosity` | `model_verbosity` | The model's verbosity: low, medium, or high |
| `UAH_HOME` | none | none | uah's home, `~/.uah` by default |
| `UAH_CONFIG` | `--config` | none | The user file, `<home>/config.toml` by default |
| `UAH_STATE_DIR` | `--state-dir` | none | Sessions, logs, and run records; the home by default |
| `UAH_EXTRA_CONFIG` | none | none | One more configuration layer, merged after `config.d` and before the project file |
| `CODEX_HOME` | none | none | Codex's directory (`~/.codex`) for `AGENTS.md` and skills |
| `BROWSER` | none | none | The program `uah mcp login` opens the authorization URL with, instead of the system's opener |

To turn the question tool off for one integration only, give it a layer of its own: `UAH_EXTRA_CONFIG=~/.uah/web-tty.toml uah` with that file holding `[tools.experimental_request_user_input]` and `enabled = false`, or `UAH_REQUEST_USER_INPUT=off` in its environment. A file in `config.d` turns it off everywhere.

`UAGENT_CONFIG` and `UAGENT_STATE_DIR` are no longer read. When either is set, uah prints a line naming its replacement, and `uah doctor` warns.

## uah config

`uah config` takes the session flags and shows what a session started with them would use: the home, the workspace, the files read (a `layer:` line for each layer), and one line per key with its value and source (`flag`, `env`, `session`, `project file`, `UAH_EXTRA_CONFIG`, `config.d/<file>`, `user file`, or `default`). Keys that append or OR list every file that set them in merge order, such as `user file + config.d/host.toml + project file`. `--json` prints the same as JSON. A flag equal to its environment variable's value is reported as `env`.

```sh
uah config                     # the current directory
uah config -C ~/code/proj      # another workspace
uah config --session 3f2a      # as resuming a session would
uah config --json | jq '.settings[] | select(.sources != ["default"])'
```

`/config` in the TUI shows the same values and sources for the basic settings (auto-compact and its token limit, `compact_model`, `model`, `effort`, `fast`, `adaptive_effort`, `permission_mode`, `[tui] details` and `mouse`) and changes them in the user file (`--config` or the default path). It edits one key in place and keeps the file's comments and formatting, with the editor `uah mcp add` uses; a change that would stop a session from starting is undone. The layers and the project file are never written, and a value one of them sets still wins over the change.

## Examples

A complete user file, `~/.uah/config.toml`:

```toml
provider = "openai-codex"
model = "gpt-6-sol"
effort = "high"
max_disk = "5G"                    # tool output per run; "0" disables
request_max_attempts = 10          # per model request; a lost connection is retried with backoff
fast = false                       # priority processing
web_search = "live"                # or disabled: the provider's hosted web search
adaptive_effort = "off"            # or 1-step, 2-steps: lower effort on follow-up turns
context_preparation = true         # start new sessions with the prepared context
# model_verbosity = "low"          # or medium, high; default: the model's (low on gpt-6.1-sol)
sandbox_mode = "workspace-write"   # read-only, workspace-write; no sandbox is --yolo
# permission_mode = "workspace"    # read-only, workspace, auto; wins over sandbox_mode
approval_policy = "on-request"     # or never
approvals_reviewer = "user" # or auto_review: the auto-reviewer answers first in read-only and workspace
user_shell_sandbox = false         # true: `!` commands run in the sandbox, as the agent's
auto_compact_percent = 90          # 0 turns automatic compaction off
model_context_window = 272000      # tokens; overrides the model catalog
# model_auto_compact_token_limit = 200000   # compact sooner than 90% of the window
# compact_model = "gpt-6-luna"              # a cheaper summary model; default: the session's
# compact_effort = "medium"
# review_model = "gpt-6-sol"                # /review's model; default: the session's
# compact_prompt = "Summarize for a handoff: decisions, open work, file paths."
# experimental_compact_prompt_file = "~/.uah/compact.md"
# compact_user_message_max_tokens = 20000  # default: 20000, at most a quarter of the window
# compact_elide_after_calls = 10           # stub tool outputs this many calls old first; 0 turns it off
# compact_keep_recent_calls = 5            # tool calls kept word for word after a summary; 0 is Codex's shape
# remote_compaction = false                # summarize locally on openai and openai-codex too
# model_instructions_file = "prompts/system.md"  # replaces uah's default prompt; relative to this file
project_doc_fallback_filenames = ["CLAUDE.md"]   # also read Claude Code's files
project_root_markers = [".git"]
project_doc_max_bytes = 32768

[instructions]
enabled = true
max_bytes = 32768                  # used when project_doc_max_bytes is unset

[sandbox_workspace_write]
network_access = false
writable_roots = ["~/Library/Caches/go-build"]

[approvals]
allow = ["go test", "git status"]  # run outside the sandbox without asking
forbid = ["git push --force"]      # never run

[shell_environment_policy]
inherit = "all"                    # all, core, none
ignore_default_excludes = true     # false drops *KEY*, *SECRET*, *TOKEN*
exclude = ["AWS_*"]
include_only = []
set = { CI = "1" }

[review]
model = "codex-auto-review"
effort = "low"
timeout = "90s"

[tools.experimental_request_user_input]
enabled = true                     # false: the agent asks in its final message, with no picker

[tui]
details = false
mouse = true                       # false: the terminal selects text
title = true                       # false: no state in the terminal title

[history]
persistence = "save-all"           # or none: write no prompts to ~/.uah/history.jsonl
max_bytes = 8388608                # the oldest prompts go past it; 0: no cap

[[hooks.PreToolUse]]
matcher = "Bash"                   # the whole tool name, as a regular expression
command = "~/.uah/hooks/no-rm-rf.sh"
timeout = "10s"                    # default 60s

[[hooks.Stop]]
command = "osascript -e 'display notification \"uah is idle\"'"

[mcp_servers.docs]                 # stdio
command = "npx"
args = ["-y", "@example/docs-mcp"]
env = { DOCS_LANG = "en" }
env_vars = ["DOCS_TOKEN"]
cwd = "/tmp"
enabled = true
required = false
startup_timeout_sec = 20
tool_timeout_sec = 60
disabled_tools = ["delete_page"]
supports_parallel_tool_calls = false
default_tools_approval_mode = "auto"

[mcp_servers.docs.tools.search]
approval_mode = "approve"

[mcp_servers.tracker]              # streamable HTTP
url = "https://mcp.example.com/mcp"
bearer_token_env_var = "TRACKER_TOKEN"
http_headers = { "X-Team" = "core" }
env_http_headers = { "X-Org" = "TRACKER_ORG" }
enabled_tools = ["search", "get_issue"]
startup_timeout_ms = 20000

[mcp_servers.linear]               # streamable HTTP with OAuth: `uah mcp login linear`
url = "https://mcp.linear.app/mcp"
scopes = ["read"]

[projects."/Users/me/code/proj"]
trusted = true                     # apply proj/.uah/config.toml and its rules
```

A project file, `/Users/me/code/proj/.uah/config.toml`, applied because the user file trusts the workspace:

```toml
effort = "medium"                  # override: replaces the user's "high"
auto_compact_percent = 0           # override, can unset: no automatic compaction here
fast = true                        # OR: cannot turn the user's fast off

[sandbox_workspace_write]
writable_roots = ["../shared"]     # append: after the user's roots, relative to the workspace

[approvals]
allow = ["make test"]              # append: the user's allow list plus this
forbid = ["git push --force"]      # append: never run, whatever an approval says

[[hooks.PostToolUse]]              # append; runs only after `uah hooks trust`
matcher = "apply_patch"
command = ".uah/hooks/format.sh"
timeout = "5s"

[mcp_servers.docs]                 # replace by name: the user's docs server is not used here
url = "https://docs.internal.example.com/mcp"
```

This repository's own [.uah/config.toml](../.uah/config.toml) is a working project file.
