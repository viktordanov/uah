# Using uah

Task guides for uah, one section per task, grouped by area. Each section says what the feature does, how to use it, how it behaves, and which settings change it. The [README](../README.md) has the install, the keys, and the providers; the [configuration reference](configuration.md) has every key.

1. [Setup](#1-setup)
   - [1.1 AGENTS.md and skills](#11-agentsmd-and-skills)
   - [1.2 MCP servers](#12-mcp-servers)
   - [1.3 Hooks](#13-hooks)
   - [1.4 Prompts and context modules](#14-prompts-and-context-modules)
   - [1.5 `/config` and `uah config`](#15-config-and-uah-config)
   - [1.6 The shell commands run in](#16-the-shell-commands-run-in)
2. [Models and requests](#2-models-and-requests)
   - [2.1 Model and effort](#21-model-and-effort)
   - [2.2 Adaptive effort](#22-adaptive-effort)
   - [2.3 Plan usage](#23-plan-usage)
   - [2.4 Retries](#24-retries)
3. [Working in a session](#3-working-in-a-session)
   - [3.1 The agent's questions](#31-the-agents-questions)
   - [3.2 Goals](#32-goals)
   - [3.3 Subagents and agent files](#33-subagents-and-agent-files)
   - [3.4 Images](#34-images)
   - [3.5 Shell mode](#35-shell-mode)
   - [3.6 Web search](#36-web-search)
   - [3.7 `/diff` and `/review`](#37-diff-and-review)
   - [3.8 Compaction and `/context`](#38-compaction-and-context)
   - [3.9 Background shells](#39-background-shells)
   - [3.10 Scripted tool calls](#310-scripted-tool-calls)
4. [Permissions](#4-permissions)
   - [4.1 Permission modes](#41-permission-modes)
   - [4.2 Command rules](#42-command-rules)
5. [Sessions and history](#5-sessions-and-history)
   - [5.1 Resume a session](#51-resume-a-session)
   - [5.2 Rewind to an earlier message](#52-rewind-to-an-earlier-message)
   - [5.3 Prompt history](#53-prompt-history)
   - [5.4 List, search, and delete sessions](#54-list-search-and-delete-sessions)
   - [5.5 Select and copy text](#55-select-and-copy-text)
   - [5.6 File links and the terminal title](#56-file-links-and-the-terminal-title)
   - [5.7 Status for the terminal](#57-status-for-the-terminal)
   - [5.8 Token statistics](#58-token-statistics)
6. [Headless: `uah exec`](#6-headless-uah-exec)
   - [6.1 Running it](#61-running-it)
   - [6.2 JSON events](#62-json-events)
   - [6.3 Exit codes](#63-exit-codes)
   - [6.4 Tool policy](#64-tool-policy)

---

## 1. Setup

The files and commands that extend uah: instructions, skills, MCP servers, hooks, prompts, and settings.

### 1.1 AGENTS.md and skills

Instruction files are `AGENTS.md` at the repository root or in any directory below it; `project_doc_fallback_filenames = ["CLAUDE.md"]` reads `CLAUDE.md` too. Skills are `<name>/SKILL.md` folders in `.agents/skills/`, in `~/.uah/skills/` for every workspace, or in `$CODEX_HOME/skills`. `/context` shows how much of the context window they take.

- **Naming a skill:** type `$` in the composer for a menu of the skills, each by its display name with its description; tab or enter fills in `$name `. Sending a message with `$name` loads the skill's `SKILL.md` before the message, so the model has it without a `SkillUse` call. `[tui] skill_mentions = false` turns off the menu and the loading.
- **uah's own skill,** `uah-customization`, explains context preparation and uah's other customization points (prompts, hooks, skills, and the configuration layers), so you can ask the agent how uah works or how to extend it. uah writes it to `~/.uah/skills/.system/` and rewrites it on update; a skill of the same name in any skill folder replaces it.

<sub>[↑ Back to top](#using-uah)</sub>

### 1.2 MCP servers

`uah mcp` adds, lists, logs in to, and approves MCP servers, in Codex's `[mcp_servers.<name>]` format in your user file, so a Codex configuration copies over.

```sh
uah mcp add docs -- npx -y @example/docs-mcp            # a stdio server (--env KEY=VALUE)
uah mcp add linear --url https://mcp.linear.app/mcp     # an HTTP server
uah mcp login linear                                     # OAuth in the browser, if the server asks for it
uah mcp list                                             # every server, its status, and its auth
```

- **In the TUI,** `/mcp` shows each server and its tools. A server's prompts are commands (`/mcp__docs__review main.go`, quoted arguments kept together), and its resources come after `@` with your files: `@docs:docs://index` adds the resource to the message. The agent lists and reads resources itself, with `list_mcp_resources` and `read_mcp_resource`.
- **Connecting:** servers connect when a TUI session opens, before any message (`uah exec` connects them on its first run), and stop with the session; `/clear` keeps them, and `/new` and `/resume` reconnect them once; `/new` also picks up a server added while the TUI runs. One that fails to start is shown then and left out; with `required = true`, every message fails until the server is fixed and `/new` connects it.
- **Restarts and tool changes:** a server that stops restarts after 1, 2, 4, 8, and 16 s, at most 5 times in a row, and keeps its tools meanwhile, so the prompt cache holds; a call made while it restarts waits for it. A changed tool list applies from your next message, never in the middle of a turn.
- **Calls** run in the background, so the agent keeps working, and each asks for approval by its tool's `approval_mode`.
- **OAuth tokens** are kept in the OS keyring (or a 0600 file without one) and refreshed as they expire. A server that needs a login shows "needs login" in `/mcp` and `uah doctor`, and reconnects at the next message once `uah mcp login`, in any terminal, stored a new login.

To stop a server's tools from asking for approval:

```sh
uah mcp add docs --approve -- npx -y @example/docs-mcp   # its tools never ask
uah mcp approve docs search --mode approve              # one tool never asks
uah mcp approve docs --mode prompt                      # every other tool always asks
uah mcp approve docs                                    # print the current modes
```

When the TUI asks about an MCP call, `a` ("Yes, and don't ask again for this tool") saves `approval_mode = "approve"` for the tool, and the session stops asking at once.

The [MCP README](../internal/mcp/README.md) has the rest, and the [MCP design](design/mcp.md) the decisions.

<sub>[↑ Back to top](#using-uah)</sub>

### 1.3 Hooks

A hook runs a command at a session event, such as a notification when the agent is idle:

```toml
[[hooks.Stop]]
command = "osascript -e 'display notification \"uah is idle\"'"
```

Hooks in a project's `.uah/config.toml` run only after `uah hooks trust`; hooks in the user file and the layers (`~/.uah/config.d/*.toml`, `UAH_EXTRA_CONFIG`) run as written. `uah hooks` lists them all and whether each runs. The [hooks README](../internal/hooks/README.md) has every event and its payload.

<sub>[↑ Back to top](#using-uah)</sub>

### 1.4 Prompts and context modules

The system prompt, the compaction prompt, the auto-review policy, and the context modules are files you can replace.

```sh
uah prompts init           # writes compact.md, system.md, system-codex.md, system-runner.md, and review.md to ~/.uah/prompts
uah prompts show system    # prints a built-in prompt: compact, system, system-codex, system-runner, or review
uah prompts show context/environment/fish   # prints a context module
uah prompts status         # lists your prompt files, context overrides, and context modules
uah context --show         # lists the context modules for this workspace and prints the context a new session gets
```

- **A prompt file takes effect only when its key names it.** `uah prompts init` prints the lines to add to your user file: `experimental_compact_prompt_file`, `model_instructions_file`, and `[review] policy_file`. Each new session reads the files. It overwrites existing files only with `--force`; to undo, delete the files and the lines.
- **The system prompts:** the default (`system.md`) is Codex's prompt for gpt-6.1-sol with uah's tool names ([the changes](configuration.md#54-codexs-prompt)). `system-codex.md` is Codex's unmodified prompt and `system-runner.md` the runner's short host prompt; their `model_instructions_file` lines are printed commented out, so each is used only when you choose it. AGENTS.md files and the [environment context](configuration.md#55-the-environment-context) still follow the system prompt.
- **Context modules:** `uah prompts init` writes the built-in modules of the [prepared context](../README.md#context-preparation) to `~/.uah/prompts/context.defaults/`, a reference that uah never reads. To change one, copy it to the same path under `~/.uah/prompts/context/` and edit the copy. A file there replaces the built-in for as long as it exists, so uah updates to that module stop reaching you: copy only what you change. `uah prompts status` flags copies identical to the built-in, and `uah prompts prune` deletes those. Modules of your own go in `~/.uah/prompts/context.d/`; see the [context preparation guide](context-preparation.md).

<sub>[↑ Back to top](#using-uah)</sub>

### 1.5 `/config` and `uah config`

`/config` in the TUI changes the basic settings: compaction, the model and effort, fast mode, adaptive effort, the permission mode, the number of open subagents, web search, the details view, the mouse, and file links, each with its value and its source. ↑↓ choose, enter or space changes, esc closes.

- **Saving:** each change is saved to your user file, keeping its comments, and applies to the running session where it can; compaction settings apply from the next session.
- **Precedence:** a flag, a configuration layer, or a trusted project file that sets the same key still wins, and `/config` says so.
- **`uah config`** shows each setting's value and where it came from, and `uah doctor` checks that everything works.
- **In your home directory:** uah runs in `~`, and in `~/.uah` itself, though its state lies inside, with a warning. The state directory's `.ignore`, which uah writes, keeps `rg`, `fd`, and `ag` out of its sessions; `block_state_in_workspace = true` refuses such a session instead, as earlier versions did.
- **Colors:** on a terminal the listings (`uah config`, `uah context`, `uah sessions`, `uah hooks`, `uah mcp list`, `uah doctor`, `uah models`, `uah usage`) use the TUI's colors: what is on or applies plain or green, what is off, a default, or does not apply dim, what needs a look yellow, what is broken red. A pipe, a file, or `NO_COLOR` gets plain text.

<sub>[↑ Back to top](#using-uah)</sub>

### 1.6 The shell commands run in

Commands run in `$SHELL` when it names an executable file (a bare name such as `fish` is looked up on `PATH`), else in the user's login shell from the user database (as Codex reads it, `internal/shellenv`) unless that is a service account's `nologin` or `false`, else in `/bin/sh`.

A service that starts uah without the user's environment can leave out `SHELL`, the locale, and the user's tool directories on `PATH`. The environment block of the prepared context then says which shell uah picked, that the locale is not UTF-8, and which tool directories exist but are not on `PATH`, and a session opens with a warning, in the TUI and in `uah exec`, when `SHELL` was unset or unusable. uah changes neither `PATH` nor the locale.

<sub>[↑ Back to top](#using-uah)</sub>

---

## 2. Models and requests

Which model answers, how hard it thinks, how much of the plan it uses, and what happens when a request fails.

### 2.1 Model and effort

The model and effort change from the next model request, even mid-run, and a session keeps its own.

| Where | Model | Effort |
| --- | --- | --- |
| This session, in the TUI | `/model`, then a model and one of its efforts (its default preselected; esc goes back). `/model gpt-6-luna low` sets both | `/effort low`, or alt+, and alt+. |
| One launch | `uah -m gpt-6-luna -e medium` | `-e`, in the same command; `--fast` adds priority processing |
| Every session | `model` in the [configuration](../README.md#configuration) | `effort` |

- **Defaults:** gpt-6.1-sol on openai-codex when your login's model list has it, else gpt-6-sol (OpenAI rolls new models out by account); on openai, gpt-6.1-sol when listed, else gpt-6-astra. Effort `high`; the levels are low, medium, high, xhigh, max, and ultra, where the model accepts them.
- **What the provider offers:** `uah models` (`--json`, `--refresh`), `/model ` then tab, or tab after `-m`. `/model` refuses a model the provider does not list, with the nearest names ("gpt-luna-6 is not available on openai-codex; did you mean gpt-6-luna?"), and `uah doctor` warns about one. `-m` and the configuration are not checked: the model goes to the provider as written.
- **The prompt cache survives an effort change** on gpt-6.1-sol and the other gpt-6 models, because uah sends the change as an update in the conversation rather than in the request, as Codex does. A backend that rejects the update gets the request again without it, and the session then changes the effort per request, with a one-line notice.

<sub>[↑ Back to top](#using-uah)</sub>

### 2.2 Adaptive effort

Adaptive effort lowers the effort on the turns that only follow tool results, where the model needs less thought. `1-step` goes one level below your effort on those turns and `2-steps` two, never below low; the first request and every turn with your message stay at your effort. On the agent benchmark at high effort, at the same pass rate, it cut wall time by 18–35% and cost by 16–25%, more at 2 steps.

- **Set it** with alt+e, `/adaptive [off|1-step|2-steps]`, or the `/config` row, from the next model request; with `--adaptive-effort` at start; or with `adaptive_effort` for new sessions. `off` is the default, and a session keeps its own value.
- **The footer** shows `high↓` or `high⇊`, and `high→low` while a lowered follow-up is out.

<sub>[↑ Back to top](#using-uah)</sub>

### 2.3 Plan usage

On `openai-codex`, uah shows how much of the ChatGPT plan's usage is left, from the same endpoint Codex reads. Other providers have no usage to show.

```sh
uah usage          # pro plan (openai-codex)
                   # weekly  [███████████████░░░░░]  22% used · 78% left · resets 15:44 on 26 Sep
uah usage --json   # the same for scripts
```

- **In the TUI:** `/usage` shows the same, `/status` a row per window, and the footer the tightest window beside the context meter (`weekly 78% left · 64% context left`).
- **Warnings:** a notice once when a window passes 75, 90, and 95% used, and when a run stops at the limit, when to try again. `uah doctor` warns from 90% used.
- **Windows** are named by their length (5h, daily, weekly), because a plan can have only a weekly window. uah reads the usage when you ask and after each run, never on a timer.
- **Prompt cache:** `/usage`, `/status`, and `uah sessions show` also show how much of the session's input the cache served, and the input it missed by cause, such as `prompt cache 86% · missed 119k: effort switches 72k, cold start 9k, other 38k · ≈20% of usage (API-price estimate)`. See [session prompt cache](../internal/usage/README.md#session-prompt-cache).

<sub>[↑ Back to top](#using-uah)</sub>

### 2.4 Retries

A model request that fails because the connection dropped, it timed out, or the provider answered 429 or a server error is sent again: after 2 s, then 4, 8, and 16 s, then every 30 s. uah allows 10 attempts, about 3 minutes, so a Wi-Fi switch or a short outage does not end the run. The working line says so meanwhile:

```text
λ Reconnecting, attempt 3 of 10 (retrying in 8s • esc to interrupt)
```

When every attempt loses the connection, the run fails with "gave up after 10 attempts because the connection to the model was lost". Change the limit with `request_max_attempts` in the [configuration](../README.md#configuration), `--max-attempts`, or `UAH_LLM_MAX_ATTEMPTS`.

<sub>[↑ Back to top](#using-uah)</sub>

---

## 3. Working in a session

What the agent can do during a session, and how you steer it.

### 3.1 The agent's questions

When a decision has a few plausible answers, the agent stops and asks with the `request_user_input` tool: one to three questions above the prompt, each with its options and what each one means, the recommended one first, and `Type your own answer`. When the options are things to compare, such as three versions of a handler, the highlighted option's preview shows beside the list (under it on a narrow terminal). The agent waits, with no time limit.

| Key | Does |
| --- | --- |
| ↑ / ↓ | Choose an option; other letters do nothing, so nothing is answered by accident |
| 1–9 | Pick an option at once |
| `n` | Add a note to the highlighted option; enter keeps it (✎), esc drops it. The note goes with the answer |
| enter | Answer and show the next question; on the last, send the answers |
| tab, shift+tab | Go between the questions |
| esc | Interrupt the agent instead, keeping what you typed |

- **The last row**, `Type your own answer`, takes your own words in the prompt.
- **The transcript** shows the call as `ASK`, and your answers under it. Approvals use the same framed panel.
- **Only the TUI offers the tool.** `uah exec` has no one to answer, so the agent asks in its final answer, and a script replies with `uah exec --last`. Subagents ask their parent.
- **Turn it off**, as for a terminal that cannot show the picker, with `enabled = false` under `[tools.experimental_request_user_input]` (Codex's key) or `UAH_REQUEST_USER_INPUT=off`; the agent then asks in its final message.

See the [questions design](design/questions.md).

<sub>[↑ Back to top](#using-uah)</sub>

### 3.2 Goals

A goal keeps the agent working, turn after turn, until it marks the goal complete with `update_goal`. Each continuation is Codex's continuation message, which tells the agent to keep the full objective, work from the current state, and mark the goal complete only after it checks every requirement against evidence.

| Command | Does |
| --- | --- |
| `/goal`, `/goal status` | Show the goal: its status, objective, time, tokens, and continuations |
| `/goal <objective>` | Set a goal and start on it; an unfinished goal must be cleared or edited first |
| `/goal edit <objective>` | Change the objective and keep the usage; `/goal edit` alone puts the objective in the prompt to change |
| `/goal pause`, `/goal resume` | Stop continuing, and go on |
| `/goal clear` | Drop the goal; `/clear` drops it too |

- **Runs and hooks:** one run holds up to 16 goal turns, so its Stop hooks run once for them, and the next continuation starts a new run (`[goals] turns_per_run`; 0 or 1 starts a run for each). A continuation never fires UserPromptSubmit.
- **What you see:** the transcript marks each `↻ continuing the goal automatically (3 of 50)`, and the footer shows `Pursuing goal (12m)`, or the tokens against the budget.
- **It stops by itself** when the agent calls `update_goal` with `complete` (the footer says `Goal achieved`), or with `blocked` after the same blocker for three runs; when the token budget is used (`[goals] max_goal_token_budget`; the agent is told to wrap up); after 50 continuations (`[goals] max_continuations`; `/goal resume` gives it 50 more); when a run fails, three automatic turns in a row make no tool call, or three turns in a row have only failing commands. An interrupt (esc esc or `/stop`) pauses it.
- **Resume and compaction:** the goal is kept with the session, so `uah resume` brings it back: an active goal continues when the next run ends, and a paused one waits for `/goal resume`. Compaction keeps it. `uah sessions show` names the goal's messages (`(goal continuation, automatic)`) instead of printing them.
- **Subagents** never get the goal or the goal tools.
- **Headless:** the agent can set a goal itself with `create_goal`, but only when you ask for one, so `uah exec "Set a goal to make the tests pass, then work on it"` runs a goal without the TUI.

See the [goal design](design/goal.md).

<sub>[↑ Back to top](#using-uah)</sub>

### 3.3 Subagents and agent files

Subagents are child sessions that the agent starts, messages, waits for, and closes with Codex's multi-agent tools; they run in parallel, in the background. Ask for them in your message, such as "use two subagents to review the TUI and the store in parallel". Each shows in the transcript as `AGENT <name>` with what it is doing, and `/agents` lists them. Subagents never start subagents of their own.

An agent file defines a kind of subagent: Markdown with front matter, as Claude Code's (`.claude/agents/*.md` files work as they are), in `~/.uah/agents/`, or in `.uah/agents/` in a trusted project. Codex's TOML role files (`reviewer.toml`) work too.

```markdown
---
name: reviewer
description: Reviews a diff for bugs and missing tests.
tools: Bash, mcp__github
effort: high
approve: [git diff, git log, mcp__github__get_pull_request]
---

Review only; do not edit files. List each finding with its file and line.
```

- **The body** is the subagent's instructions. `tools` limits the tools it is offered (Claude Code's `Edit` and `Write` are `apply_patch`; omit `tools` for all of them). `approve` lists commands and MCP tools it runs without asking, within the session's permission mode: read only stays read only, and a `forbid` rule still wins.
- **Model and effort:** a subagent runs on the parent's provider. Ask for another model or effort ("use a subagent on gpt-6-luna with low effort"), set `model`, `effort`, and `fast: true` in its agent file, or set defaults in `[agents]`. See [agent files](configuration.md#82-agent-role-files).
- **Forking:** ask for a forked subagent ("fork a subagent to write the tests for what we just discussed") to hand it the conversation so far through `fork_context`.
- **Watching:** `/agents <name>`, or alt+← and alt+→ on an empty composer (with a draft they move by a word), shows a subagent's transcript, and what you type goes to it. `uah sessions show subagent-1a2b3c4d` prints a finished one's transcript.
- **Results:** when a subagent finishes or is interrupted, the main agent gets its final answer: at its next model request while it works; when it is idle, the answer starts a turn of its own, unless you stopped the last turn with esc esc, in which case it waits for your next message. The main agent is told to leave a running subagent alone and wait for it, and to message it only with something new from you, an answer to its question, or to stop a clear failure.

<sub>[↑ Back to top](#using-uah)</sub>

### 3.4 Images

Images go to the model with your message, as `[Image #N]` placeholders in the prompt:

1. Copy an image or a screenshot and press ctrl+v (or alt+v), or paste or drop an image file on the terminal, or choose one after `@`. `[Image #1]` appears at the cursor.
2. Write your message around the placeholders and press enter.

- **Remove one** by deleting its placeholder: one backspace at its end removes it all.
- **On Linux**, uah reads the clipboard with `wl-paste` (Wayland) or `xclip` (X11); install one of them.
- **Images larger than 2000 pixels** on a side are scaled down.

See the [images design](design/images.md).

<sub>[↑ Back to top](#using-uah)</sub>

### 3.5 Shell mode

Shell mode runs your own command in the workspace and gives its output to the agent with your next message.

1. Type `!` in the empty composer. The λ becomes `!`, and the footer says `! shell mode`.
2. Type the command, such as `go test ./...`, and press enter. It runs at once, also while the agent works, and its output streams into the transcript with the exit status.
3. Send your next message. The agent gets the command, its exit code, and its output with it, in Codex's `<user_shell_command>` format. The command alone never starts a turn.

- **Leave** with backspace on the empty composer, or esc. Esc esc stops a running command.
- **Limits:** the output the agent sees is cut to 40,000 characters, keeping the start and the end. A command stops after an hour.
- **Sandbox:** the command runs as your own, outside the sandbox and the command rules, as in Codex and Claude Code. `user_shell_sandbox = true` in the [configuration](../README.md#configuration) runs it like the agent's commands instead: in the sandbox of the current permission mode, and refused by a `forbid` rule.

The [shell mode design](design/shell-mode.md) compares Codex and Claude Code.

<sub>[↑ Back to top](#using-uah)</sub>

### 3.6 Web search

On openai and openai-codex, the model can search the web with the provider's hosted `web_search` tool; it is on by default. The transcript shows each search as a `WEB` line, such as `searched: latest Go release` or `opened: https://go.dev/dl/`, and `uah exec` prints the same line.

- **No sandbox or approval:** the search runs on the provider's servers, so the sandbox's network rule does not block it, and it needs no approval, in every permission mode.
- **Turn it off** with `web_search = "disabled"` in the [configuration](../README.md#configuration) or in `/config`.
- **Citation markers** the model writes after a search (private-use characters that read as `citeturn2view0`) are hidden in the TUI, in `uah exec`'s answer and `-o` file, and in `uah sessions show`; uah has no source list to link them to (`internal/citations`).
- **Later turns:** the model sees its past searches, because uah keeps them beside the session and puts them back into its requests. A resumed transcript does not show them.

The [web search design](design/web-search.md) compares it with Codex.

<sub>[↑ Back to top](#using-uah)</sub>

### 3.7 `/diff` and `/review`

`/diff` shows the workspace's git changes in the transcript: staged, unstaged, and untracked files, with binary and large files named but not shown. The agent does not see it.

`/review` runs a read-only reviewer on your changes and lists its findings:

1. Type `/review ` and pick a target in the menu: `uncommitted`, `branch` (then a base branch), or `commit` (then one of the last 100 commits). Or type instructions: `/review check the error handling`.
2. While it works, the `REVIEW` line shows the reviewer's model and effort, its steps, the tokens so far, and the time, with its latest commands under it, and `waiting on: …` for a command that has run a minute or more; ctrl+t shows every step. Esc esc stops it.
3. When it is done, the line under it counts the findings by priority and gives the verdict and the reviewer's confidence. The findings follow, sorted by priority and then confidence, each with its priority, confidence, file and lines, and explanation. A finding under 50% confidence is dim.
4. Send your next message: it takes the findings to the agent, so "fix the P1" works.

- **What the reviewer can do:** run commands in the read-only sandbox, without network, and view images, nothing else; it never asks for approval. The review ends at its first answer, and any command it left running stops.
- **The agent** sees the reviewer as one of its agents, marked as started by you, and leaves it alone unless you ask it to act on it.
- **Settings:** `review_model` picks the reviewer's model (default: the session's). One of the reviewer's commands may run 10 minutes (`review_command_timeout`). `review_time_limit` and `review_token_limit`, off by default, bound the whole review: the reviewer then answers with what it has, and the review says "stopped at the time limit".

`uah review` runs the same review without the TUI. It prints the reviewer's steps on stderr and the review on stdout as the TUI shows it, in color on a terminal, and exits 0 when the reviewer answers, whatever it finds:

```sh
uah review --base main                    # the changes against main
uah review --uncommitted                  # staged, unstaged, and untracked changes
uah review --commit 1a2b3c4               # one commit (--title adds its title to the hint)
uah review "check the error handling"     # custom instructions; - reads them from stdin
uah review --base main --json             # one JSON line: findings, verdict, model, effort, tokens
uah review --base main -o REVIEW.md       # also write the review to a file, in Codex's text
```

The [review design](design/review.md) has Codex's behavior and the decisions.

<sub>[↑ Back to top](#using-uah)</sub>

### 3.8 Compaction and `/context`

uah compacts automatically at 90% of the context window. It first replaces old tool outputs with short stubs, and summarizes only when that does not free enough; on openai and openai-codex the provider compacts. Each summary ends with a ledger of the files changed, the commands still failing, the files read, and the skills loaded, and the last five tool calls stay as they were.

| Command | Does |
| --- | --- |
| `/compact` | Compact now; `/compact keep the failing test names` tells the summary what to focus on |
| `/context` | Show what fills the window (below) |
| `/clear` | Start the agent fresh in the same session: its next request carries nothing from before, while the session keeps its history |
| `/new` | Start a new session |

`/context` shows the last request sent as a 10×10 grid, one cell per percent, with each category's tokens (system prompt, instruction files, skills, tools, MCP tools, your messages, agent messages, and tool calls with their results), the free space, and the auto-compact buffer, then a line per file, skill, and tool. The counts are estimated at 4 bytes a token and scaled to the input tokens the provider reported (`internal/contextusage`).

`uah sessions show` prints what each compaction freed. The summary model, its prompt, and when compaction starts are [configurable](configuration.md#10-compaction).

<sub>[↑ Back to top](#using-uah)</sub>

### 3.9 Background shells

The agent can start a command that keeps running after its call, such as a dev server, a watcher, or a long batch, and go on working beside it.

1. Ask for it, for example "start the dev server and check the login page". The agent calls Bash with `background`, and gets back the shell's ID (`shell-1`), its process group, and its log file.
2. The agent reads the log with `tail` or `grep` while the shell runs. Nothing keeps the turn open: the turn can end while the shell runs.
3. When the shell exits, the agent is told, with its exit code and the end of its output: at once if it is working, otherwise in a turn the exit starts, so the agent takes up the result without a message from you. After you stop a turn with esc esc, an exit waits for your next message instead.

| To | Do |
| --- | --- |
| See the shells | The footer counts the ones running (`1 shell running · /ps`); `/ps` lists each one's state, how long it has run, its process group, command, and log. In the transcript, the call that started one reads `BG shell-1 <command>` |
| Stop one | `/ps stop shell-1`, or `/ps stop all`; the agent is told |
| Stop them all and leave | ctrl+c on the empty composer asks once more while a shell runs, then quits and stops them, with the subagents |

- **Esc esc** interrupts the turn only; the shells keep running, and their exits wait for your next message.
- **The agent's own tools:** `wait_shell` waits up to 4.5 minutes for a shell to exit, and `kill_shell` stops one.
- **Sandbox:** a background shell runs in the sandbox of the permission mode it started in, and asks for approval as any command does.
- **Limits:** 16 running shells per session, and 256 MiB of log each. Shells do not survive quitting uah, or a resume after it.

The [background shells design](design/background-shells.md) has what the sessions showed and how Codex and Claude Code do it.

<sub>[↑ Back to top](#using-uah)</sub>

### 3.10 Scripted tool calls

With MCP servers configured, the agent can call their tools from a script: inside any command it runs, `uah tool` reaches the session's MCP tools. A script can then make many calls, filter or join what they return with `jq` or Python, and pass on only what the agent needs, instead of a model request per call with every result read in full.

```sh
uah tool list                                   # the tools a script may call
uah tool describe mcp__github__search_issues    # one tool's input schema
uah tool call mcp__github__search_issues '{"q":"label:bug"}' | jq -r '.[].number'
```

- **Nothing to set up:** it is on whenever the session has MCP servers. The agent is told about it in Bash's description and in the prepared context, and decides per task; a direct call is still right for one small lookup.
- **Same rules:** a script's call is checked as the agent's own call of the tool would be: the tool policy, PreToolUse hooks, and the tool's `approval_mode`, which asks you. A refusal fails the command with the reason.
- **What you see:** each call shows in the transcript as an MCP call's line marked `· uah tool`, under the command that made it.
- **Turning it off:** `[tools.bridge] enabled = false` in the [configuration](configuration.md), or `UAH_TOOL_BRIDGE=off`.

The [tool bridge design](design/tool-bridge.md) has why it is built on the shell, how it works, and the planned A/B test.

<sub>[↑ Back to top](#using-uah)</sub>

---

## 4. Permissions

What the agent's commands can do without asking, and who answers when one asks.

### 4.1 Permission modes

The permission mode picks the sandbox and who answers an escalation; shift+tab cycles it, and the footer shows it:

| Mode | Commands can | What needs approval |
| --- | --- | --- |
| read only | Read files; write only the session's private `$TMPDIR` | You |
| workspace (default) | Write the workspace | You |
| auto | Write the workspace | The auto-reviewer decides; you are not asked |
| yolo (only with `--yolo`) | Anything your user can: no sandbox | Nothing: every command, patch, and MCP tool runs unasked; only `forbid` rules refuse |

- **A change applies** from the next command, even mid-run. Subagents run in their parent's mode.
- **Session grants:** in workspace and auto mode, a patch or an escalated command that writes into another git worktree of the workspace's repository makes that worktree writable for the rest of the session, without asking, and the transcript says `writable for this session: <path>`. In workspace mode, the prompt for a patch outside the workspace can also allow writes to its directory for the session (`w`). See [session grants](configuration.md#32-session-grants).
- **Resume:** a resumed session keeps its mode, with its model, effort, fast mode, and adaptive effort. Yolo mode is kept only when you give `--yolo` again; without it the session opens in the configured mode.
- **Start in a mode** with `permission_mode` in the [configuration](../README.md#configuration), or for one session with `--sandbox read-only` or `--sandbox workspace-write`.
- **`--yolo`** (or Codex's `--dangerously-bypass-approvals-and-sandbox`) starts the TUI or `uah exec` in yolo mode and adds yolo after auto in the shift+tab cycle. Without the flag, yolo is not offered, and no file can set it. It takes no `--sandbox` or `--ask`. Use it only where something outside uah sandboxes the machine. The footer shows `yolo mode` in the warning color, and the terminal title says `yolo`.

<sub>[↑ Back to top](#using-uah)</sub>

### 4.2 Command rules

A command rule lets a command prefix run outside the sandbox without asking, or forbids it. Three ways to stop being asked for the same command:

1. Answer `s` ("Yes, and don't ask again") when uah asks. It saves a rule for the command's prefix.
2. Switch to auto mode (shift+tab until the footer says `auto mode`): the auto-reviewer then approves or declines each escalation.
3. List prefixes in the configuration:

   ```toml
   [approvals]
   allow = ["go test", "git status"]
   forbid = ["git push --force"]
   ```

The [approvals README](../internal/approval/README.md) gives the order in which rules, the sandbox, the auto-reviewer, hooks, and you decide.

<sub>[↑ Back to top](#using-uah)</sub>

---

## 5. Sessions and history

Sessions are kept on disk under `~/.uah/sessions`, so you can resume, rewind, search, and delete them.

### 5.1 Resume a session

```sh
uah resume              # pick one of this directory's sessions (--all: any directory)
uah resume --last       # this directory's most recent session
uah --session 3f2a      # a session by ID or unique prefix
```

- **On quit**, the TUI prints the session's token usage and the command that continues it, `uah resume <id>` on its own line; on a terminal it uses the TUI's colors.
- **In the TUI**, ctrl+s opens the picker and ctrl+n (or `/new`) starts a new session with a fresh ID. The picker hides sessions from `uah exec` and subagents.
- **Choosing a new session's ID:** `uah --session-id <uuid>` or `uah exec --session-id <uuid>`, as Claude Code's `--session-id`. The ID must be a UUID that no session has used; `--session-id` never resumes. A session that opened and never ran, such as a launch stopped before its first message, has no history, so `--session-id` takes its ID again and `uah resume <id>` resumes it.

<sub>[↑ Back to top](#using-uah)</sub>

### 5.2 Rewind to an earlier message

Rewinding cuts the conversation at one of your messages and puts that message back in the prompt to edit.

1. Press esc twice on an empty prompt while the agent is idle, or type `/rewind`. Your latest message is selected.
2. Esc or ↑ selects an earlier one, ↓ a later one.
3. Enter puts the message back in the prompt, with its images, to edit and send again.

The message and everything after it leave the agent's context and the screen, and a resumed session keeps the cut. The session file keeps the old branch, and `uah sessions show` prints it. Files the agent changed stay changed. See the [rewind design](design/rewind.md).

<sub>[↑ Back to top](#using-uah)</sub>

### 5.3 Prompt history

Each folder has its own prompt history, from this session and earlier ones: the folder is the session's workspace, and `/resume` into a session of another folder shows that folder's prompts.

| Key | Does |
| --- | --- |
| ↑ / ↓, on an empty prompt | The previous and the next prompt |
| ctrl+r | Search: type part of a prompt, and the composer shows the newest match. Ctrl+r or ↑ go to older matches, ctrl+s or ↓ to newer ones. Enter keeps the match to edit, esc puts your draft back |
| ctrl+c on a draft | Clear it; ↑ brings it back |

- **Edit a recalled prompt** and it is yours: the arrows move the cursor again. A `!` command comes back in shell mode; a prompt of this run comes back with its images.
- **The file** is `~/.uah/history.jsonl`, private to you, in Codex's format with each prompt's folder added; slash commands are not kept. Lines without a folder, such as those written before uah kept one, show in no folder.
- **Settings:** `[history] persistence = "none"` stops writing it, and `max_bytes` caps it (8 MiB by default).

See the [prompt history design](design/prompt-history.md).

<sub>[↑ Back to top](#using-uah)</sub>

### 5.4 List, search, and delete sessions

```sh
uah sessions                           # this directory's sessions, newest first (--all: every directory)
uah sessions --search "flaky parser"   # sessions whose prompts or answers contain the words
uah sessions show 3f2a                 # the transcript and the prompt cache line (--json)
uah sessions --json -C ~/src/app --since 2026-09-29T08:00:00Z   # a directory's sessions active since then
uah sessions rm 3f2a                   # delete a session with its runs and subagents (--dry-run, --json)
```

- **`uah sessions rm`** removes the session's files, its tool output, its run records, its rows in the index, and the same for each subagent it started. It refuses while a run holds the session's lock; `--force` removes the session anyway.
- **For programs:** each session's sidecar, `~/.uah/sessions/<id>.uah.json`, has its `workspace`, its `first_prompt` (200 characters), its `last_activity`, and its `last_sequence`, so a program can find a session and see that it changed from that file alone. `last_sequence` is the `Sequence` of the last item in the [session file](../internal/sessionfile/README.md) when the last turn ended.
- **A session another program wrote**, such as a conversation converted from another harness into a [session file](../internal/sessionfile/README.md#history-another-program-wrote), is listed, shown, and resumed like any other, with 0 runs until uah runs it; `--search` finds it once it has a run.
- **A session that never ran** has a sidecar and no session file: `uah sessions` lists it with 0 runs and no first prompt, and it resumes under its ID ([a session that never ran](../internal/sessionfile/README.md#a-session-that-never-ran)).

<sub>[↑ Back to top](#using-uah)</sub>

### 5.5 Select and copy text

Drag over the transcript to select text; double click selects a word and triple click a line. Letting go copies the selection to the clipboard, and a short note in the transcript's corner says how many lines; nothing else on the screen moves.

- **Scrolling while selecting:** hold the mouse on the top row, or below the transcript, to scroll (faster further out); the wheel keeps scrolling during a drag. New output does not move the text under the mouse. Esc or a click clears the selection.
- **What is copied:** the copy leaves out the `λ` and `•` columns and the padding around code, so a code block pastes as code. uah copies with OSC 52, which also works over ssh, and with `pbcopy`, `wl-copy`, or `xclip`.
- **The terminal's own selection:** hold Option (iTerm2, Terminal) or Shift (most others), or set `[tui] mouse = false`.

See the [selection design](design/selection.md).

<sub>[↑ Back to top](#using-uah)</sub>

### 5.6 File links and the terminal title

File paths in the transcript are underlined links: the files a READ line reads, a patch's and `/diff`'s files, a `/review` finding's place, and the files the agent names in its messages that exist in the workspace. A click shows the file in an overlay over the session, scrolled to the line, with that line or range marked.

- **In the overlay:** ↑↓, pgup and pgdn, g and G, and the wheel scroll; `e` opens the file in your editor, `o` with the system; esc closes it.
- **`[tui] file_links`:** `"editor"` opens a click in `$VISUAL` or `$EDITOR` at the line instead (code, cursor, zed, and subl in their own window), `"open"` with the system's default app, and `"off"` draws plain paths.
- **OSC 8:** the paths are also hyperlinks, which the terminal's own cmd+click or ctrl+click opens, also with the mouse off.

The terminal's title shows the session's state and its workspace, to find a pane among many: `uah · api` when idle, `uah · working · api` while the agent works, `uah · approve? · api` while an approval waits, and `uah · answer? · api` while the agent's questions wait. `[tui] title = false` turns it off. See the [file links design](design/file-links.md).

<sub>[↑ Back to top](#using-uah)</sub>

### 5.7 Status for the terminal

The TUI tells the terminal what the session is doing with OSC 7501, the [program status protocol](https://superlogical.com/rex/docs/build/program-status), so a terminal or a tool that hosts terminals can show it in a tab, a session list, or a notification, also through ssh, with nothing to set up. Terminals that do not know it ignore it.

| The session | Status |
| --- | --- |
| At the prompt, or after you interrupt | `idle` |
| The agent works | `working`, with what it waits on: `Thinking`, `Running 2 commands` |
| An approval, the agent's questions, or an expired login waits | `blocked`, with `permission`, `question`, or `auth` |
| The run finished | `done`, with the start of the answer, until you type |
| The run failed | `error`, with the error, until you type |

- **Subagents:** each has a record of its own, `agent/<nickname>`: `working` while it runs, then `done` or `error` until you type.
- **Turning it off:** `[tui] program_status = false` in the [configuration](configuration.md).
- **tmux** drops the reports.

The [program status design](design/program-status.md) has the protocol and how uah sends it.

<sub>[↑ Back to top](#using-uah)</sub>

### 5.8 Token statistics

`uah stats` adds up the tokens of every session, in every workspace: each finished run's, including subagents' and /review's reviewers', and each compaction call's.

```console
$ uah stats
uah stats · last 7 days, Oct 10 – 16

  39.2M tokens · 776 runs · 488 sessions · 91% from the cache
  1.7× the 7 days before (22.9M) · busiest Mon Oct 12, 11.0M

DAY         RUNS  SESSIONS   TOTAL  CACHE
Sat Oct 10    52        14    2.7M    94%  ██▉
Sun Oct 11    21         8  896.7k    95%  ▉
Mon Oct 12   179       138   11.0M    88%  ████████████
…
Fri Oct 16   104        76    4.9M    87%  █████▎        today

  all time    78.2M since Sep 23 · 1,610 runs · 723 sessions
  per day     median 3.0M · mean 3.7M · p90 7.3M, of 21 active days
  streak      18 days · 21 of 24 days active
  average     48.6k a run · 108.2k a session
  models      gpt-6-astra 89% · gpt-6.1-sol 7% · gpt-6-sol 3% · 5 others 0.5%
  workspaces  temporary 33% · app 11% · api 7% · 101 others 49%
```

```sh
uah stats --by week                        # day, week, month, model, workspace, or source
uah stats --since 2026-10-01 --until 2026-10-15   # dates (both included) or RFC 3339 times
uah stats -C ~/src/app --by model          # one workspace
uah stats --wide                           # input, cached, output, and reasoning too
uah stats --heatmap                        # tokens per day: 26 weeks, or the weeks --since and --until touch
uah stats --json                           # exact counts for scripts
```

- **The headline:** the range's tokens, runs, sessions, and the share of input served from the cache; how it compares with the range before it (the same number of days for dates, the same span for RFC 3339 times; with `--since`, or the default 7 days); and its busiest day.
- **The rows:** runs, sessions, total, the cache share, and a bar of the total against the largest row, with today, this week, or this month marked. `--wide` adds input, cached input, output, and reasoning: cached input is part of input and reasoning part of output; total is the input not served from the cache plus the output, as the exit summary counts it.
- **The summary:** all time under the last 7 days; per active day, the median, mean, and 90th percentile; the streak up to today; the tokens per run and per session; the largest models and workspaces (by their last path element, the temporary ones together, such as benchmarks').
- **Days** are local and weeks start on Monday. A run counts on the day it started. Auto-review calls are not recorded yet, so they are missing ([design](design/token-stats.md#gaps)).

<sub>[↑ Back to top](#using-uah)</sub>

---

## 6. Headless: `uah exec`

`uah exec` runs the agent without the TUI: progress on stderr, each answer on stdout, and an exit when the agent is idle. `uah run` is the same command, and `uah exec --help` lists the flags.

### 6.1 Running it

```sh
uah exec "Fix the failing test in pkg/foo"
uah exec --last "Now update the changelog"       # continue this directory's latest session
git diff | uah exec -                             # all of stdin is one message
printf 'first\nsecond\n' | uah exec --stdin       # each line, of any length, is a message; lines queue while the agent works
uah exec --json "..."                             # JSONL events for scripts, with the answer as it arrives (also --stream)
uah exec --ephemeral -o answer.md "..."           # keep no session; write the final answer to a file
```

- **`--ephemeral`** keeps nothing: the session runs in a temporary directory that uah removes at exit, so `sessions/`, `runs/`, and the index do not change and `uah sessions` does not list it. The model cache and the small scripts that start the sandbox stay in the state directory, because sandboxed commands can write the temporary directory, and a script there could be swapped for one that runs outside the sandbox. It starts a new session, so it cannot be used with `--last` or `--session`.
- **`-o`** (`--output-last-message`) writes the last run's answer to the file at exit; with no answer, it writes an empty file and warns on stderr.
- **No one to ask:** commands that need an approval are declined with a reason, and the question tool is not offered, so the agent asks in its answer.
- **No time limit:** a script that needs one wraps the run, as in `timeout 30m uah exec …` with GNU coreutils, which exits 124.

<sub>[↑ Back to top](#using-uah)</sub>

### 6.2 JSON events

`--json` prints one JSON event per line. Plain `uah exec` prints each answer once, when it is complete; `--json` streams it:

| Event | When |
| --- | --- |
| `text_delta`, `reasoning_delta`, `stream_reset` | While the model writes, before the final `assistant_message` |
| `web_search` | When a [web search](#36-web-search) starts and when it ends, with its query or URL |
| `compaction_started`, `compacted` | Around a compaction; `compacted` has its `stats`: the strategy, the tokens before and after, and the summary call's usage |
| `auto_reviewed` | After each auto-review: the command, the verdict, its risk and reason, how long the review took, and the reviewer's tokens |
| `goal_updated`, `goal_continued`, `goal_cleared` | A [goal](#32-goals) changed (the goal, what changed, and who changed it), uah starts a run for it, or it was cleared; the progress lines say each change |
| `error` | A message did not reach the agent, or a run failed |

See the [streaming design](design/streaming.md).

<sub>[↑ Back to top](#using-uah)</sub>

### 6.3 Exit codes

| Code | When |
| --- | --- |
| 0 | The run succeeded |
| 1 | The run failed; or any message did not reach the agent (a run that did not start, or a message a hook blocked), a run ended in an error, or stdin could not be read, unless the last run stopped at the disk limit or was interrupted |
| 2 | A usage error |
| 3 | The disk limit |
| 130 | An interrupt |

After a failure, the error is on stderr (an `error` notice with `--json`), and the work already sent still finishes; `-o` writes an answer only when a later run gives one, never an earlier run's.

<sub>[↑ Back to top](#using-uah)</sub>

### 6.4 Tool policy

`--tools` limits the tools the model may use, and `--deny-tools` takes tools away; both narrow `[tools] allow` and `deny` and can never widen them ([tool policy](configuration.md#4-tool-policy)). The model is offered only those tools, and a call to any other is refused before a hook, an approval, or a job sees it. Subagents get the same limit, and `--tools ""` allows no tools at all.

A read-only query that may use one MCP read tool, with the caller's identity sent to the MCP server from the environment:

```sh
# ~/.uah/config.d/lookup.toml (or a file UAH_EXTRA_CONFIG names):
#   [mcp_servers.crm]
#   url = "https://crm.example.com/mcp"
#   bearer_token_env_var = "CRM_TOKEN"                   # the caller's token
#   env_http_headers = { "X-Caller" = "CRM_CALLER" }      # and identity
CRM_TOKEN=… CRM_CALLER=alice uah exec --ephemeral --json --sandbox read-only \
  --tools mcp__crm__lookup --no-skills "Who owns account 42?"
```

- **Hooks:** under a tool policy, a PreToolUse or PermissionRequest hook that fails blocks its call instead of being ignored ([hooks](../internal/hooks/README.md#output-and-decisions)).
- **Skills and instructions:** `--no-skills` (or `[skills] enabled = false`) leaves skills out of the prompt and the tools; AGENTS.md files still apply unless `--no-instructions` is given too.

<sub>[↑ Back to top](#using-uah)</sub>
