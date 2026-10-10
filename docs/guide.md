# Using uah

Task guides for uah, by what you are doing. The [README](../README.md) has the install, the first run, and the keys; the [configuration reference](configuration.md) has every key.

1. Work with the agent: [Model and effort](#model-and-effort), [Permission modes](#permission-modes), [Command rules](#command-rules), [Answer the agent's questions](#answer-the-agents-questions), [Goals](#goals), [Subagents and agent files](#subagents-and-agent-files), [Images](#images), [Shell mode](#shell-mode), [Web search](#web-search), [Review your changes](#review-your-changes), [Compaction and `/clear`](#compaction-and-clear)
2. Sessions and the transcript: [Resume a session](#resume-a-session), [Go back to an earlier message](#go-back-to-an-earlier-message), [Reuse an earlier prompt](#reuse-an-earlier-prompt), [Search old sessions](#search-old-sessions), [Select and copy text](#select-and-copy-text), [Open files from the transcript](#open-files-from-the-transcript)
3. Scripts and CI: [Headless mode](#headless-mode)
4. Set up and extend: [AGENTS.md and skills](#agentsmd-and-skills), [MCP setup](#mcp-setup), [Hook setup](#hook-setup), [Custom prompts](#custom-prompts), [The `/config` panel](#the-config-panel), [Inspect the configuration](#inspect-the-configuration), [Usage limits](#usage-limits), [A lost connection](#a-lost-connection), [The shell commands run in](#the-shell-commands-run-in)

---

## Model and effort

- By default: gpt-6.1-sol on openai-codex when your login's model list has it, else gpt-6-sol (OpenAI rolls new models out by account); on openai, gpt-6.1-sol when listed, else gpt-6-astra. Effort `high`; the levels are low, medium, high, xhigh, max, and ultra, where the model accepts them.
- For this session: type `/model` in the TUI and pick a model, then one of its efforts (its default is preselected; esc goes back to the models). `/model gpt-6-luna` asks only for the effort, and `/model gpt-6-luna low` sets both at once. `/effort low`, or alt+, and alt+., change only the effort. It applies from the next model request, even mid-run.
- At start: `uah -m gpt-6-luna -e medium`, and `--fast` for priority processing.
- For every session: `model` and `effort` in the [configuration](../README.md#configuration).
- Adaptive effort: the model thinks less on the turns that only follow tool results. `1-step` goes one effort level below yours on those turns, `2-steps` two, never below low; the first request and every turn with your message stay at your effort. `off` is the default. On the agent benchmark at high effort, at the same pass rate, it cut wall time by 18–35% and cost by 16–25%, more at 2 steps. Set it with alt+e, `/adaptive [off|1-step|2-steps]`, or the `/config` row (from the next model request, even mid-run); the footer then shows `high↓` or `high⇊`, and `high→low` while a lowered follow-up is out, `--adaptive-effort` at start, or `adaptive_effort` for new sessions. A session keeps its own value, as it keeps its effort.
- On gpt-6.1-sol and the other gpt-6 models, uah changes the effort as Codex does, with an update in the conversation rather than in the request, so the prompt cache survives adaptive effort's switches and `/effort`. A backend that rejects the update gets the request again without it, and the session then changes the effort per request, with a one-line notice.
- See what the provider offers: `uah models` (`--json`, `--refresh`), `/model ` then tab in the TUI, or tab after `-m`. In the TUI, `/model` refuses a model the provider does not list, with the nearest names ("gpt-luna-6 is not available on openai-codex; did you mean gpt-6-luna?"), and `uah doctor` warns about one; `-m` and the configuration are not checked: the model goes to the provider as written.

## Permission modes

Press shift+tab in the TUI. It cycles three modes, and the footer shows the current one:

| Mode | Commands can | What needs approval |
| --- | --- | --- |
| read only | Read files; write only the session's private `$TMPDIR` | You |
| workspace (default) | Write the workspace | You |
| auto | Write the workspace | The auto-reviewer decides; you are not asked |
| yolo (only with `--yolo`) | Anything your user can: no sandbox | Nothing: every command, patch, and MCP tool runs unasked; only `forbid` rules refuse |

- A change applies from the next command, even mid-run.
- In workspace and auto mode, a patch or an escalated command that writes into another git worktree of the workspace's repository makes that worktree writable for the rest of the session, without asking, and the transcript says `writable for this session: <path>`. In workspace mode, the prompt for a patch outside the workspace can also allow writes to its directory for the session (`w`). See [session grants](configuration.md#session-grants).
- A resumed session keeps its mode, with its model, effort, fast mode, and adaptive effort. Yolo mode is kept only when you give `--yolo` again; without it the session opens in the configured mode.
- To start in a mode, set `permission_mode` in the [configuration](../README.md#configuration). `--sandbox read-only` or `--sandbox workspace-write` also picks a mode for one session.
- `--yolo` (Codex's `--dangerously-bypass-approvals-and-sandbox`, which uah also accepts) starts the TUI or `uah exec` in yolo mode, and adds yolo after auto in the shift+tab cycle. Without the flag, yolo is not offered, and no file can set it. It takes no `--sandbox` or `--ask`. Use it only where something outside uah sandboxes the machine. The footer shows `yolo mode` in the warning color, and the terminal title says `yolo`. Subagents run in their parent's mode.

## Command rules

1. When uah asks, answer `s` ("Yes, and don't ask again"). It saves a rule for the command's prefix.
2. Or press shift+tab until the footer says `auto mode`: the auto-reviewer then approves or declines each escalation, and you are not asked. See [Switch the permission mode](#permission-modes).
3. Or list prefixes in the configuration:

   ```toml
   [approvals]
   allow = ["go test", "git status"]
   forbid = ["git push --force"]
   ```

The [approvals README](../internal/approval/README.md) gives the order in which rules, the sandbox, the auto-reviewer, hooks, and you decide.

## Answer the agent's questions

When the agent needs a decision with a few plausible answers, it stops and asks with Codex's `request_user_input` tool: one to three questions above the prompt, each with its options and what each one means, the recommended one first, and `Type your own answer`. When the options are things to compare, such as three versions of a handler, the highlighted option's preview shows beside the list (under it on a narrow terminal). The agent waits for you, with no time limit.

- ↑ and ↓ choose an option; a number picks it at once. Other letters do nothing, so nothing is answered by accident.
- `n` adds a note to the highlighted option: type it, enter keeps it (✎), esc drops it. The note goes with the answer.
- `Type your own answer`, the last row, takes your own words in the prompt.
- Enter answers the question and shows the next one; on the last, it sends the answers, and the agent goes on with them. Tab and shift+tab go between the questions.
- Esc interrupts the agent instead, and keeps what you typed, so you can send a message of your own.

The transcript shows the call as `ASK`, and your answers under it. Only the TUI offers the tool: `uah exec` has no one to answer, so the agent asks in its final answer there, and a script replies with `uah exec --last`. To turn it off, as for a terminal that cannot show the picker, set `enabled = false` under `[tools.experimental_request_user_input]` (Codex's key), in the user file or a layer such as `UAH_EXTRA_CONFIG`, or `UAH_REQUEST_USER_INPUT=off`; the agent then asks in its final message, as before. Approvals use the same framed panel. Subagents ask their parent instead. See the [questions design](design/questions.md).

## Goals

`/goal <objective>` sets a goal and starts work on it; uah then keeps the agent working, run after run, until the agent marks the goal complete, as Codex's `/goal` does. Each continuation is Codex's continuation message, which tells the agent to keep the full objective, work from the current state, and mark the goal complete only after it checks every requirement against evidence. One run holds up to 16 goal turns, so its Stop hooks run once for them; the next continuation starts a new run (`[goals] turns_per_run`; 0 or 1 starts a run for each, as Codex does). The transcript marks each `↻ continuing the goal automatically (3 of 50)`, and the footer shows `Pursuing goal (12m)`, or the tokens against the budget.

| Command | Does |
| --- | --- |
| `/goal`, `/goal status` | Show the goal: its status, objective, time, tokens, and continuations |
| `/goal <objective>` | Set a goal and start on it; an unfinished goal must be cleared or edited first |
| `/goal edit <objective>` | Change the objective and keep the usage; `/goal edit` alone puts the objective in the prompt to change |
| `/goal pause`, `/goal resume` | Stop continuing, and go on |
| `/goal clear` | Drop the goal; `/clear` drops it too |

The goal stops by itself when:

- the agent calls `update_goal` with `complete` (the footer says `Goal achieved`), or with `blocked` after the same blocker for three runs;
- the token budget is used: `[goals] max_goal_token_budget` sets it, and the agent is told to wrap up;
- uah has continued it 50 times (`[goals] max_continuations`; `/goal resume` gives it 50 more);
- a run fails, three automatic turns in a row make no tool call, or three turns in a row have only failing commands;
- you interrupt (esc esc or `/stop`), which pauses it.

The goal is kept with the session, so `uah resume` brings it back: an active goal continues when the next run ends, and a paused one waits for `/goal resume`. Compaction keeps it. Subagents never get it, nor the goal tools. `uah sessions show` names the goal's messages (`(goal continuation, automatic)`) instead of printing them. The agent can also set a goal itself with Codex's `create_goal`, but only when you ask for one, so `uah exec "Set a goal to make the tests pass, then work on it"` runs a goal headless. See the [design](design/goal.md).

## Subagents and agent files

Ask for it, for example "use two subagents to review the TUI and the store in parallel". The agent starts them with Codex's tools; each shows in the transcript as `AGENT <name>` with what it is doing, and `/agents` lists them. Subagents never start subagents of their own. To define a kind of subagent, add a Markdown file with front matter, as for Claude Code (`.claude/agents/*.md` files work as they are), to `~/.uah/agents/`, or to `.uah/agents/` in a trusted project:

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

The body is the subagent's instructions. `tools` limits the tools it is offered (Claude Code's `Edit` and `Write` are `apply_patch`; omit `tools` for all of them). `approve` lists commands and MCP tools it runs without asking, within the session's permission mode: read only stays read only, and a `forbid` rule still wins. Codex's TOML role files (`reviewer.toml`) work too. See [agent files](configuration.md#subagents).

A subagent runs on the parent's provider. Ask for another model or effort ("use a subagent on gpt-6-luna with low effort"), set `model`, `effort`, and `fast: true` in its agent file, or set defaults in `[agents]`; see [agent files](configuration.md#subagents). Ask for a forked subagent ("fork a subagent to write the tests for what we just discussed") to hand it the conversation so far through `fork_context`.

To watch a subagent, type `/agents <name>` or press alt+← and alt+→: the TUI shows its transcript, and what you type goes to it. When a subagent finishes or is interrupted, the main agent is told with its final answer, as in Codex: at its next model request while it works, else with your next message. The main agent is told to leave a running subagent alone and wait for it, and to message it only with something new from you, an answer to its question, or to stop a clear failure. `uah sessions show subagent-1a2b3c4d` prints a finished one's transcript.

## Images

1. Copy an image, or a screenshot, and press ctrl+v (or alt+v) in the TUI on macOS or Linux. `[Image #1]` appears at the cursor.
2. Or paste or drop an image file on the terminal, or choose one after `@`. The path becomes `[Image #N]`.
3. Write your message around the placeholders and press enter. The images go to the model with the message.

To remove an image, delete its placeholder: one backspace at its end removes it all. On Linux, uah reads the clipboard with `wl-paste` (Wayland) or `xclip` (X11); install one of them. Images larger than 2000 pixels on a side are scaled down. See the [images design](design/images.md).

## Shell mode

1. Type `!` in the empty composer. The λ becomes `!`, and the footer says `! shell mode`.
2. Type the command, such as `go test ./...`, and press enter. It runs in the workspace at once, also while the agent works, and its output streams into the transcript with the exit status.
3. Send your next message. The agent gets the command, its exit code, and its output with it, in Codex's `<user_shell_command>` format. The command alone never starts a turn.

- Backspace on the empty composer, or esc, leaves shell mode. Esc esc stops a running command.
- The output the agent sees is cut to 40,000 characters, keeping the start and the end. A command stops after an hour.
- The command runs as your own, outside the sandbox and the command rules, as in Codex and Claude Code. `user_shell_sandbox = true` in the [configuration](../README.md#configuration) runs it like the agent's commands instead: in the sandbox of the current permission mode, and refused by a `forbid` rule.

The [shell mode design](design/shell-mode.md) compares Codex and Claude Code.

## Web search

On openai and openai-codex, the model can search the web with the provider's hosted `web_search` tool, as in Codex. It is on by default. The transcript shows each search as a `WEB` line, such as `searched: latest Go release` or `opened: https://go.dev/dl/`, and `uah exec` prints the same line.

- The search runs on the provider's servers, so the sandbox's network rule does not block it, and it needs no approval, in every permission mode.
- To turn it off, set `web_search = "disabled"` in the [configuration](../README.md#configuration) or in `/config`.
- The citation markers the model writes after a search (private-use characters that read as `citeturn2view0`) are hidden in the TUI, in `uah exec`'s answer and `-o` file, and in `uah sessions show`; uah has no source list to link them to (`internal/citations`).
- The model sees its past searches on later turns, as in Codex: uah keeps them beside the session and puts them back into its requests. A resumed transcript does not show them.

The [web search design](design/web-search.md) compares it with Codex.

## Review your changes

`/diff` shows the workspace's git changes in the transcript: staged, unstaged, and untracked files, with binary and large files named but not shown. The agent does not see it.

`/review` has a reviewer look at your changes and list findings, as Codex's `/review` does:

1. Type `/review ` and pick a target in the menu: `uncommitted`, `branch` (then a base branch), or `commit` (then one of the last 100 commits). Or type instructions: `/review check the error handling`.
2. The reviewer runs beside the session, read-only: it can run commands in the read-only sandbox, without network, and view images, nothing else, and it never asks for approval. The review ends at its first answer, and any command it left running stops. Esc esc stops it. It is yours: the agent sees it as one of its agents, marked as started by you, and leaves it alone unless you ask it to act on it.
3. While it works, the `REVIEW` line shows the reviewer's model and effort, its steps, the tokens so far, and the time, with its latest commands under it, and `waiting on: …` for a command that has run a minute or more; ctrl+t shows every step.
4. When it is done, the line under it counts the findings by priority and gives the verdict and the reviewer's confidence. The findings follow, sorted by priority and then confidence, each with its priority, confidence, file and lines, and explanation. A finding under 50% confidence is dim. Your next message takes the findings to the agent, as in Codex, so "fix the P1" works.

`uah review` runs the same review without the TUI, as `codex review` does. It prints the reviewer's steps on stderr and the review on stdout as the TUI shows it: the counts by priority, the verdict, and the confidence, then the findings sorted, in color on a terminal. It exits 0 when the reviewer answers, whatever it finds:

```sh
uah review --base main                    # the changes against main
uah review --uncommitted                  # staged, unstaged, and untracked changes
uah review --commit 1a2b3c4               # one commit (--title adds its title to the hint)
uah review "check the error handling"     # custom instructions; - reads them from stdin
uah review --base main --json             # one JSON line: findings, verdict, model, effort, tokens
uah review --base main -o REVIEW.md       # also write the review to a file, in Codex's text
```

`review_model` in the [configuration](../README.md#configuration) picks the reviewer's model (default: the session's). One of the reviewer's commands may run 10 minutes (`review_command_timeout`). `review_time_limit` and `review_token_limit`, off by default, bound the whole review: the reviewer then answers with what it has, and the review says "stopped at the time limit". The [review design](design/review.md) has Codex's behavior and the decisions.

## Compaction and `/clear`

uah compacts automatically at 90% of the context window. It first replaces old tool outputs with short stubs, and summarizes only when that does not free enough; on openai and openai-codex the provider compacts, as in Codex. Each summary ends with a ledger of the files changed, the commands still failing, the files read, and the skills loaded, and the last five tool calls stay as they were. `/compact` compacts now, and `/compact keep the failing test names` tells the summary what to focus on. `/context` shows what fills the window, and `uah sessions show` prints what each compaction freed. The summary model, its prompt, and when compaction starts are [configurable](configuration.md#compaction). `/clear` starts the agent fresh in the same session: its next request carries nothing from before, while the session keeps its history. `/new` starts a new session.

`/context` shows what fills the window, as Claude Code's does: a 10×10 grid, one cell per percent, with each category's tokens (system prompt, instruction files, skills, tools, MCP tools, your messages, agent messages, and tool calls with their results), the free space, and the auto-compact buffer, then a line per file, skill, and tool. It breaks down the last request sent, estimated at 4 bytes a token and scaled to the input tokens the provider reported (`internal/contextusage`).

---

## Resume a session

```sh
uah resume              # pick one of this directory's sessions (--all: any directory)
uah resume --last       # this directory's most recent session
uah --session 3f2a      # a session by ID or unique prefix
```

To choose a new session's ID, as Claude Code's `--session-id` does, run `uah --session-id <uuid>` or `uah exec --session-id <uuid>`. The ID must be a UUID that no session has used; `--session-id` never resumes. A session that opened and never ran, such as a launch stopped before its first message, has no history, so `--session-id` takes its ID again and `uah resume <id>` resumes it. A new session in the TUI (ctrl+n or `/new`) still gets a fresh ID.

When you quit the TUI, it prints the session's token usage and the command that continues it, `uah resume <id>` on its own line, as Codex does; on a terminal it uses the TUI's colors. In the TUI, ctrl+s opens the picker and ctrl+n starts a new session. The picker hides sessions from `uah exec` and subagents, as Codex hides `codex exec` sessions.

## Go back to an earlier message

Press esc twice on an empty prompt while the agent is idle, or type `/rewind`: your latest message is selected. Esc or ↑ selects an earlier one and ↓ a later one; enter puts the message back in the prompt, with its images, to edit and send again. The message and everything after it leave the agent's context and the screen, as Codex's backtrack does, and a resumed session keeps the cut. The session file keeps the old branch, and `uah sessions show` prints it. Files the agent changed stay changed. See the [rewind design](design/rewind.md).

## Reuse an earlier prompt

On an empty prompt, ↑ brings back your previous prompt in this folder, from this session or an earlier one, and ↓ goes forward again. Each folder has its own prompts, as in Claude Code: the folder is the session's workspace, and `/resume` into a session of another folder shows that folder's prompts. Edit a recalled prompt and it is yours: the arrows move the cursor again. A `!` command comes back in shell mode; a prompt of this run comes back with its images. Ctrl+c on a draft clears it, and ↑ brings it back.

Ctrl+r searches this folder's prompts: type part of a prompt, and the composer shows the newest match. Ctrl+r or ↑ go to older matches, ctrl+s or ↓ to newer ones. Enter keeps the match to edit, and esc puts your draft back.

Prompts are kept in one file, `~/.uah/history.jsonl`, private to you, in Codex's format with each prompt's folder added; slash commands are not. Lines without a folder, such as those written before uah kept one, show in no folder. `[history] persistence = "none"` stops writing it, and `max_bytes` caps it (8 MiB by default). See the [prompt history design](design/prompt-history.md).

## Search old sessions

```sh
uah sessions                           # this directory's sessions, newest first (--all: every directory)
uah sessions --search "flaky parser"   # sessions whose prompts or answers contain the words
uah sessions show 3f2a                 # the transcript and the prompt cache line (--json)
uah sessions --json -C ~/src/app --since 2026-09-29T08:00:00Z   # a directory's sessions active since then
uah sessions rm 3f2a                   # delete a session with its runs and subagents (--dry-run, --json)
```

`uah sessions rm` removes the session's files, its tool output, its run records, its rows in the index, and the same for each subagent it started. It refuses while a run holds the session's lock; `--force` removes the session anyway.

Each session's sidecar, `~/.uah/sessions/<id>.uah.json`, also has its `workspace`, its `first_prompt` (200 characters), its `last_activity`, and its `last_sequence`, so a program can find a session and see that it changed from that file alone. `last_sequence` is the `Sequence` of the last item in the [session file](../internal/sessionfile/README.md) when the last turn ended. A session that never ran has a sidecar and no session file: `uah sessions` lists it with 0 runs and no first prompt, and it resumes under its ID ([a session that never ran](../internal/sessionfile/README.md#a-session-that-never-ran)).

## Select and copy text

Drag over the transcript to select text; double click selects a word and triple click a line. Letting go copies the selection to the clipboard, and a short note in the transcript's corner says how many lines; nothing else on the screen moves. Hold the mouse on the top row, or below the transcript, to scroll while you select (faster further out), and the wheel keeps scrolling during a drag. While you select, new output does not move the text under the mouse. Esc or a click clears the selection. The copy leaves out the `λ` and `•` columns and the padding around code, so a code block pastes as code. uah copies with OSC 52, which also works over ssh, and with `pbcopy`, `wl-copy`, or `xclip`. To use the terminal's own selection, hold Option (iTerm2, Terminal) or Shift (most others), or set `[tui] mouse = false`. See the [selection design](design/selection.md).

## Open files from the transcript

File paths in the transcript are underlined links: the files a READ line reads, a patch's and `/diff`'s files, a `/review` finding's place, and the files the agent names in its messages that exist in the workspace. A click shows the file in an overlay over the session, scrolled to the line, with that line or range marked; ↑↓, pgup and pgdn, g and G, and the wheel scroll it, `e` opens it in your editor, `o` with the system, and esc closes it. `[tui] file_links = "editor"` opens a click in `$VISUAL` or `$EDITOR` at the line instead (code, cursor, zed, and subl in their own window), `"open"` with the system's default app, and `"off"` draws plain paths. The paths are also OSC 8 hyperlinks, which the terminal's own cmd+click or ctrl+click opens, also with the mouse off. See the [file links design](design/file-links.md).

The terminal's title shows the session's state and its workspace: `uah · api` when idle, `uah · working · api` while the agent works, and `uah · approve? · api` while an approval waits, `uah · answer? · api` while the agent's questions wait, which helps to find a pane among many. `[tui] title = false` turns it off.

---

## Headless mode

`uah exec` prints progress on stderr and each answer on stdout, and exits when the agent is idle. It is named as `codex exec`; `uah run` is the same command.

```sh
uah exec "Fix the failing test in pkg/foo"
uah exec --last "Now update the changelog"       # continue this directory's latest session
git diff | uah exec -                             # all of stdin is one message
printf 'first\nsecond\n' | uah exec --stdin       # each line, of any length, is a message; lines queue while the agent works
uah exec --json "..."                             # JSONL events for scripts, with the answer as it arrives (also --stream)
uah exec --ephemeral -o answer.md "..."           # keep no session; write the final answer to a file
```

`--ephemeral` keeps nothing: the session runs in a temporary directory that uah removes at exit, so `sessions/`, `runs/`, and the index do not change and `uah sessions` does not list it. The model cache and the small scripts that start the sandbox stay in the state directory: sandboxed commands can write the temporary directory, and a script there could be swapped for one that runs outside the sandbox. It starts a new session, so it cannot be used with `--last` or `--session`. `-o` (`--output-last-message`) writes the last run's answer to the file at exit; with no answer, it writes an empty file and warns on stderr, as Codex does.

The TUI shows the answer as the model writes it, and `--json` adds `text_delta`, `reasoning_delta`, and `stream_reset` events before the final `assistant_message`. A [web search](#web-search) is a `web_search` event when it starts and when it ends, with its query or URL. A compaction is `compaction_started`, then `compacted` with its `stats`: the strategy, the tokens before and after, and the summary call's usage. Each auto-review is `auto_reviewed`: the command, the verdict, its risk and reason, how long the review took, and the reviewer's tokens. A [goal](#goals) adds `goal_updated` (the goal, what changed, and who changed it), `goal_continued` before each run uah starts for it, and `goal_cleared`; the progress lines say each change. Plain `uah exec` prints each answer once, when it is complete. See the [streaming design](design/streaming.md).

It exits 0 when the run succeeds, 1 when it fails, 2 on a usage error, 3 at the disk limit, and 130 on an interrupt. It also exits 1 when any message did not reach the agent (a run that did not start, or a message a hook blocked), when a run ended in an error, or when stdin could not be read, unless the last run stopped at the disk limit (3) or was interrupted (130); the error is on stderr, or an `error` notice with `--json`, and the work already sent still finishes. After such a failure, `-o` writes an answer only when a later run gives one, never an earlier run's. A run has no time limit; a script that needs one wraps it, as in `timeout 30m uah exec …` with GNU coreutils, which exits 124. Nobody can answer an approval headless, so commands that need one are declined with a reason. Nor can anyone answer the agent's questions, so `uah exec` does not offer the question tool, and the agent asks in its answer. `uah exec --help` lists the flags.

`--tools` limits the tools the model may use, and `--deny-tools` takes tools away; both narrow `[tools] allow` and `deny` and can never widen them ([tool policy](configuration.md#tool-policy)). The model is offered only those tools, and a call to any other is refused before a hook, an approval, or a job sees it. Subagents get the same limit. `--tools ""` allows no tools at all. A quick, read-only query that may use one MCP read tool, with the caller's identity sent to the MCP server from the environment:

```sh
# ~/.uah/config.d/lookup.toml (or a file UAH_EXTRA_CONFIG names):
#   [mcp_servers.crm]
#   url = "https://crm.example.com/mcp"
#   bearer_token_env_var = "CRM_TOKEN"                   # the caller's token
#   env_http_headers = { "X-Caller" = "CRM_CALLER" }      # and identity
CRM_TOKEN=… CRM_CALLER=alice uah exec --ephemeral --json --sandbox read-only \
  --tools mcp__crm__lookup --no-skills "Who owns account 42?"
```

Under a tool policy, a PreToolUse or PermissionRequest hook that fails blocks its call instead of being ignored ([hooks](../internal/hooks/README.md#output-and-decisions)). `--no-skills` (or `[skills] enabled = false`) leaves skills out of the prompt and the tools; AGENTS.md files still apply unless `--no-instructions` is given too.

---

## AGENTS.md and skills

Put them in `AGENTS.md` at the repository root or in any directory below it, as for Codex. To read `CLAUDE.md` too, set `project_doc_fallback_filenames = ["CLAUDE.md"]`. Skills go in `.agents/skills/<name>/SKILL.md`, or `~/.uah/skills/<name>/SKILL.md` for every workspace. `/context` shows how much of the context window they take.

uah ships one skill of its own, `uah-customization`: it explains context preparation and uah's other customization points (prompts, hooks, skills, and the configuration layers), so you can ask the agent how uah works or how to extend it. uah writes it to `~/.uah/skills/.system/` and rewrites it on update; a skill of the same name in any skill folder replaces it.

## MCP setup

```sh
uah mcp add docs -- npx -y @example/docs-mcp            # a stdio server (--env KEY=VALUE)
uah mcp add linear --url https://mcp.linear.app/mcp     # an HTTP server
uah mcp login linear                                     # OAuth in the browser, if the server asks for it
uah mcp list                                             # every server, its status, and its auth
```

`uah mcp add` writes Codex's `[mcp_servers.<name>]` format into your user file, so a Codex configuration copies over. `/mcp` in the TUI shows each server and its tools; `/new` picks up a server added while the TUI runs, and a `uah mcp login` in another terminal takes effect at your next message.

In the TUI, a server's prompts are commands (`/mcp__docs__review main.go`, quoted arguments kept together), and its resources come after `@` with your files: `@docs:docs://index` adds the resource to the message. The agent can list and read resources itself, with Codex's `list_mcp_resources` and `read_mcp_resource` tools.

To stop a server's tools from asking for approval:

```sh
uah mcp add docs --approve -- npx -y @example/docs-mcp   # its tools never ask
uah mcp approve docs search --mode approve              # one tool never asks
uah mcp approve docs --mode prompt                      # every other tool always asks
uah mcp approve docs                                    # print the current modes
```

When the TUI asks about an MCP call, answer `a` ("Yes, and don't ask again for this tool"). It saves `approval_mode = "approve"` for the tool, and the session stops asking at once.

How servers behave once they are set up:

- Servers connect when a TUI session opens, before any message (`uah exec` connects them on its first run), and stop with the session; `/clear` keeps them, and `/new` and `/resume` reconnect them once. One that fails to start is shown then and left out; with `required = true`, every message fails until the server is fixed and `/new` connects it.
- A server that stops restarts after 1, 2, 4, 8, and 16 s, at most 5 times in a row, and keeps its tools meanwhile, so the prompt cache holds; a call made while it restarts waits for it. A server that changes its tool list has the new list from your next message, never in the middle of a turn.
- A call runs in the background, so the agent keeps working. A tool asks for approval by its `approval_mode`, as in Codex.
- OAuth tokens are kept in the OS keyring (or a 0600 file without one) and refreshed as they expire. A server that needs a login shows "needs login" in `/mcp` and `uah doctor`, and reconnects at the next message once `uah mcp login` stored a new login.

The [MCP README](../internal/mcp/README.md) has the rest, and the [MCP design](design/mcp.md) the decisions.

## Hook setup

Add a hook, for example a notification when the agent is idle:

```toml
[[hooks.Stop]]
command = "osascript -e 'display notification \"uah is idle\"'"
```

Hooks in a project's `.uah/config.toml` run only after `uah hooks trust`; hooks in the user file and the layers (`~/.uah/config.d/*.toml`, `UAH_EXTRA_CONFIG`) run as written. `uah hooks` lists them all and whether each runs.

## Custom prompts

```sh
uah prompts init           # writes compact.md, system.md, system-codex.md, system-runner.md, and review.md to ~/.uah/prompts
uah prompts show system    # prints a built-in prompt: compact, system, system-codex, system-runner, or review
uah prompts show context/environment/fish   # prints a context module
uah prompts status         # lists your prompt files, context overrides, and context modules
uah context --show         # lists the context modules for this workspace and prints the context a new session gets
```

`uah prompts init` starts from the built-in compaction prompt, uah's default system prompt (`system.md`), and the auto-review policy. It prints the lines to add to your user file: `experimental_compact_prompt_file`, `model_instructions_file`, and `[review] policy_file`; a prompt file takes effect only when its key names it. The default system prompt is Codex's prompt for gpt-6.1-sol with uah's tool names ([the changes](configuration.md#codexs-prompt)). The command also writes Codex's unmodified prompt as `system-codex.md` and the runner's short host prompt as `system-runner.md`, and prints their `model_instructions_file` lines commented out, so each is used only when you choose it. AGENTS.md files and the [environment context](configuration.md#the-environment-context) still follow the system prompt. Edit the files; each new session reads them. It overwrites existing files only with `--force`. To undo, delete the files and the lines.

The [prepared context](../README.md#context-preparation) is made of Markdown modules too. `uah prompts init` writes the built-in modules to `~/.uah/prompts/context.defaults/`, a reference that uah never reads. To change one, copy it to the same path under `~/.uah/prompts/context/` and edit the copy. A file there replaces the built-in for as long as it exists, so uah updates to that module stop reaching you: copy only what you change. `uah prompts status` lists your overrides and flags copies identical to the built-in, and `uah prompts prune` deletes those. Modules of your own go in `~/.uah/prompts/context.d/`; see the [guide](context-preparation.md).

## The `/config` panel

Type `/config` in the TUI. It lists the basic settings (compaction, the model and effort, fast mode, adaptive effort, the permission mode, the number of open subagents, web search, the details view, the mouse, and file links) with each value and its source. ↑↓ choose, enter or space changes, esc closes. Each change is saved to your user file, keeping its comments, and applies to the running session where it can; compaction settings apply from the next session. A flag, a configuration layer, or a trusted project file that sets the same key still wins, and `/config` says so.

## Inspect the configuration

`uah config` shows each setting's value and where it came from. `uah doctor` checks that everything works.

## Usage limits

With the default provider, `openai-codex`, uah shows how much of your ChatGPT plan's usage is left, as Codex does:

```sh
uah usage          # pro plan (openai-codex)
                   # weekly  [███████████████░░░░░]  22% used · 78% left · resets 15:44 on 26 Sep
uah usage --json   # the same for scripts
```

- In the TUI, `/usage` shows the same, `/status` shows a row per window, and the footer shows the tightest one beside the context meter (`weekly 78% left · 64% context left`).
- A notice warns once when a window passes 75, 90, and 95% used. When a run stops at the limit, a notice says when to try again.
- `uah doctor` warns from 90% used.
- `/usage`, `/status`, and `uah sessions show` also show the session's prompt cache: how much input the cache served, and the input it missed by cause, such as `prompt cache 86% · missed 119k: effort switches 72k, cold start 9k, other 38k · ≈20% of usage (API-price estimate)`. See [session prompt cache](../internal/usage/README.md#session-prompt-cache).

Windows are named by their length (5h, daily, weekly), because a plan can have only a weekly window. uah reads the usage when you ask and after each run, never on a timer. Other providers have no usage to show.

## A lost connection

When a model request fails because the connection dropped, it timed out, or the provider answered 429 or a server error, the runner's client sends it again: after 2 s, then 4, 8, and 16 s, then every 30 s. uah allows 10 attempts, about 3 minutes, so a Wi-Fi switch or a short outage does not end the run. While a request waits, the TUI's working line says so:

```text
λ Reconnecting, attempt 3 of 10 (retrying in 8s • esc to interrupt)
```

When every attempt loses the connection, the run fails with "gave up after 10 attempts because the connection to the model was lost". Change the limit with `request_max_attempts` in the [configuration](../README.md#configuration), `--max-attempts`, or `UAH_LLM_MAX_ATTEMPTS`.

## The shell commands run in

Commands run in `$SHELL` when it names an executable file (a bare name such as `fish` is looked up on `PATH`), else in the user's login shell from the user database (as Codex reads it, `internal/shellenv`) unless that is a service account's `nologin` or `false`, else in `/bin/sh`. A service that starts uah without the user's environment can leave out `SHELL`, the locale, and the user's tool directories on `PATH`; the environment block then says which shell uah picked, that the locale is not UTF-8, and which tool directories exist but are not on `PATH`, and a session opens with a warning, in the TUI and in `uah exec`, when `SHELL` was unset or unusable. uah changes neither `PATH` nor the locale.
