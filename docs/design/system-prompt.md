# The system prompt

Status: built (ledger items 63, 66, and 118). The package READMEs hold the current contract; this record keeps the research and the decisions.

Ledger item 63: uah's default base instructions are Codex's prompt with only the changes uah needs, and Codex's `<environment_context>` block ends the system prompt. Item 66 moved the base from gpt-6-sol's template at Codex rust-v0.156.1 to gpt-6.1-sol's at rust-v0.159.1 (see [The move to gpt-6.1-sol](#the-move-to-gpt-61-sol)).

1. [What the model sees](#what-the-model-sees)
2. [The prompt](#the-prompt)
3. [The move to gpt-6.1-sol](#the-move-to-gpt-61-sol)
4. [The environment context](#the-environment-context)
5. [Subagents](#subagents)
6. [Decisions](#decisions)
7. [Open](#open)

## What the model sees

Checked against Codex rust-v0.156.1 (the prompt: rust-v0.159.1) and unreal-agent v0.1.1.

The runner's context builder (`harness/contextbuilder/builder.go`, `SetSystemPrompt`) sends one `system` message:

1. The runner's preamble (`harness/contextbuilder/prompts/preamble.md`): turns, asynchronous tool calls, the heartbeat, and "ending a turn with nothing running ends the session". Up to unreal-agent v0.5.2 it opened with "You run on Unreal Agent Harness built by Unreal Labs."; uah-core v0.6.0 (ledger item 93) drops that line, which changes the cached prefix once per session.
2. The skill preamble and the `<available_skills>` block, only when the workspace has skills.
3. uah's `SystemPrompt` (`instructions.HostPrompt`): the base instructions, then `# Project instructions` and the AGENTS.md files, then `<environment_context>`.

Codex sends the model's template in the Responses `instructions` field, and sends AGENTS.md, the environment context, the permissions, and the collaboration mode as separate developer or user messages. uah puts all of it in one system message, in the same order.

The base instructions are, in order of precedence: the file that `model_instructions_file` names, else `instructions.DefaultPrompt`. Before this item the default was the runner's host prompt, about 50 tokens ("You are an AI agent running inside an isolated sandbox container"). The default is about 4,400 tokens (about 3,700 before item 66, 4,300 before item 118). The runner sends the same prefix with every request, so it is cached after the first one.

## The prompt

`internal/instructions/default_prompt.md` is `codex_prompt.md` (gpt-6.1-sol's `model_messages.instructions_template` in `codex-rs/models-manager/models.json` at rust-v0.159.1, word for word) with the nine hunks of `default_prompt.diff`. A test applies the diff to Codex's text and requires the default exactly, so the prompt cannot drift from this table. Size: 21,779 bytes before, 19,729 after.

Risk **low** means that the change renames or deletes text about a mechanism that uah does not have. **Medium** means that it changes behavior the model was tuned with.

| # | Diff line | Section | Change | Why | Risk |
| --- | --- | --- | --- | --- | --- |
| 1 | `@@ -1,2` | Opening line | "You are Codex, an agent based on GPT-6" becomes "You are uah, a coding agent in the user's terminal" | uah is not Codex, the runner's preamble names the harness, and uah also runs models that are not GPT on openrouter, fireworks, and ollama | low (a judgment call) |
| 2 | `@@ -22,3` | Autonomy and persistence | "ask the user for clarification while continuing independent work" becomes "... in your final message" | Without an asynchronous question tool, a question can only go in the final message, which ends the run. Otherwise the model could ask in commentary and keep going, and the user would never get a chance to answer | medium |
| 3 | `@@ -28,3` | Personality | "As Codex" becomes "As uah" | Follows hunk 1 | low |
| 4 | `@@ -66,3` | Working with the user | The `request_user_input_async` paragraph is replaced by uah's text of item 63: ask in the `final` channel; ask early only when most of the work depends on the answer, else finish the independent work first; for optional clarification, proceed with a stated assumption. Item 118 adds the blocking `request_user_input` tool: when it is available and the answer is a choice between a few plausible options, ask with it instead, at the end of the independent work or when the agent cannot proceed, never for permission or for files. The guidance on multiple-choice questions and bundling stays word for word | uah has no asynchronous question tool, and does not want one. The 60-second wait, "Elapsed time is not an answer", and "Do NOT ask the user to upload files ... using this tool" depend on it. Codex's blocking tool is offered only where a user answers (the TUI), so the text says "when available" and the prompt stays the same everywhere; with the tool turned off (`[tools.experimental_request_user_input] enabled = false`), `instructions.WithoutQuestionTool` gives back item 63's text ([questions](questions.md)) | medium, the riskiest: it changes when the model stops to ask |
| 5 | `@@ -105,3` | Visualizations | Deletes "Prefer interactive visuals when explaining ..." and the line's trailing space | uah draws Markdown only, with no interactive surface | low |
| 6 | `@@ -109,3` | Visualizations | The Mermaid and inline-visualization sentence becomes Codex's own terminal text, `TERMINAL_VISUALIZATION_INSTRUCTIONS` in `codex-rs/tui/src/terminal_visualization_instructions.rs`, word for word | uah shows a Mermaid block as code. Codex's TUI appends this text behind the `terminal_visualization_instructions` feature | low |
| 7 | `@@ -116,9` | Rules for getting work done | (a) The two `functions.exec` / `Promise.allSettled` bullets become one: "Batch independent searches and reads as parallel tool calls in one response". (b) `exec_command` / `cmd` becomes `Bash` / `command`. (c) `$UAH_HOME` joins `$CODEX_HOME` | (a) Codex runs gpt-6-sol and gpt-6.1-sol in `tool_mode = "code_mode_only"`; uah offers plain function tools, which the runner runs in parallel. (b) The runner's tool is `Bash` with `command`. (c) uah's home variable; `$CODEX_HOME` stays, because uah still reads `~/.codex` | low |
| 8 | `@@ -131,3` | Using skills | "listed in the "## Skills" section" becomes "listed in the `<available_skills>` block at the start of these instructions" | Where the runner puts the list | low |
| 9 | `@@ -149,25` | How to use skills; Apps; Plugins | `skills.list` / `skills.read` becomes "call `SkillUse` with its exact name, or read its `SKILL.md`"; the `skill://` sentence, the `# Apps (Connectors)` and `# Plugins` sections, and the template's closing blank line are deleted | uah has no orchestrator skills, no `codex_apps` MCP server, no `tool_search`, and no plugins. MCP tools keep their `mcp__server__tool` names, which the model sees in the tool list | low |

Kept on purpose, although uah differs:

- **The `commentary` and `final` channels.** The runner keeps each message's Responses `phase` and sends it back with the history, and real sessions under `~/.uah/sessions` already use both channels.
- **"collapsed after the final answer is shown".** Commentary stays visible in uah, but the instruction that goes with it, a self-contained final answer, is still right.
- **Steering, compaction, and the auto-review wording** match uah: ctrl+enter steers, compaction keeps user messages word for word, and auto mode uses Codex's guardian prompt.
- **"Avoid sleep or wait calls longer than 60 seconds".** Runner calls are asynchronous, but the advice costs nothing.
- **File links** `[app.py](/abs/path/app.py:12)`: uah draws the label with the target dim after it. The environment context gives the model the absolute workspace path the links need.

The runner's preamble uses "turn" for one model response and "session" for what uah calls a run; Codex's prompt uses "turn" for everything up to the final answer. Both agree that a final message with nothing running ends the run. A model that ends a response with calls still running may label that message `final_answer`; uah then draws a second answer later in the same run. This has not appeared in recorded sessions.

## The move to gpt-6.1-sol

Item 66 took Codex rust-v0.159.1's template for gpt-6.1-sol, uah's new default model on openai-codex, and applied the same nine edits to it. gpt-6-sol's template is byte for byte the same at rust-v0.156.1 and rust-v0.159.1, so the change is the new model's text:

- **Personality and Writing style** move after "Autonomy and persistence" and grow: warmer wording, connected prose over headings and lists, no needless apologies, and two new sections, "Technical communication" and "Writing PR descriptions". The identity hunk moved with the paragraph (hunk 3, "As Codex, you are ... a lucid communicator").
- **Permissions**: messages to others are allowed when "an explicitly-invoked skill or plugin" asks, and an auto-review rejection is explained "in a short, separate paragraph at the end".
- **Autonomy**: a new paragraph treats "can you ..." and "help me ..." as requests to act.
- **The question tool** paragraph names only `request_user_input_async`, waits 60 seconds instead of 30, and forbids asking for files through the tool. uah replaces it as before (hunk 4), so none of this reaches the model.
- **Testing**: "Broaden or repeat testing only to resolve a concrete remaining risk" and the paragraph on user corrections become one bullet, "Run tests appropriate to the change and complete required checks".
- **Skills**: a skill that makes the model stop must be named, linked, and quoted.
- Typos and trailing spaces: "intermedaite" is fixed, and several lines end with a space.

Every hunk still applies with the same change. Hunks 2 and 3 swapped places, hunk 5 also drops the trailing space of its line, and hunk 9 also drops the closing blank line. No hunk needed new wording. The new text mentions "skill or plugin" twice outside the deleted `# Plugins` section; uah keeps it, since the permission rule reads correctly with skills alone.

A login whose model list does not have gpt-6.1-sol yet runs gpt-6-sol with gpt-6.1-sol's prompt. Codex would send gpt-6-sol's own template; uah keeps one default prompt, and the difference is the writing guidance above.

## The environment context

`instructions.LocalEnvironment` fills the block, and `Environment.String` renders it as Codex's legacy single environment (`codex-rs/core/src/context/world_state/environment.rs`, and `environment_render_tests.rs` for the expected text):

```xml
<environment_context>
  <cwd>/Users/me/code/proj</cwd>
  <shell>zsh</shell>
  <current_date>2026-09-29</current_date>
  <timezone>Europe/Berlin</timezone>
</environment_context>
```

- **cwd**: the session's workspace, absolute.
- **shell**: the base name of the shell `Bash` runs, `$SHELL` when it names an executable file, else the login shell from the user database, else `/bin/sh` (`app.RealShell`, `internal/shellenv`). Codex names its shell type (`zsh`, `bash`, `sh`, `powershell`); the base name is the same for those.
- **current_date** and **timezone**: Codex's `local_time_context` (`core/src/session/turn_context.rs`) takes the local date as `%Y-%m-%d` and the IANA zone from `iana_time_zone`, else the UTC date and `Etc/UTC`. uah reads the zone from `$TZ` (a name, or a path under `zoneinfo/`, with an optional leading `:`), else from the `/etc/localtime` link, and falls back the same way.
- Values are XML-escaped as Codex's `push_xml_escaped_text` does, and an empty value is left out.

Codex's block can also carry `<filesystem>` (the sandbox's permission profile), `<network>`, `<shell_version>` (PowerShell only), and `<subagents>`. uah leaves them out; the `Bash` tool's description already names the sandbox mode and network access.

**Placement.** Codex sends the block as a user message after the AGENTS.md message, when a thread starts, and sends a new block when a value changes, such as the date after midnight (`render_diff`). Appending is cache-friendly for Codex, because the history grows at the end. uah has one system message and no hidden context messages: a user message would appear in the transcript, in `uah sessions`, as a session's first prompt, and in what compaction keeps. So the block goes at the end of the system message, after the AGENTS.md files, which keeps Codex's order.

**When the values are taken.** `app.Setup` takes them once, when the session opens. Every request of the session then sends the same system message, and the prompt cache holds. A session that runs past midnight keeps its first date, where Codex would tell the model the new one. A resume on a later day takes the new date, which changes the system message once; a cache that old has usually expired anyway.

The block follows `model_instructions_file` too, and it stays with `--no-instructions`, as Codex's `include_environment_context` is separate from AGENTS.md. `/context` counts it as the system prompt (`contextusage.splitSystem` cuts it off before splitting the files).

## Subagents

A child gets its parent's system prompt, environment included, byte for byte, so its requests reuse the parent's prompt cache. One note from Codex's `multi_agent.role.subagent` text (`instructions.SubagentNote`) follows the task in its first message, as Codex puts role text in developer instructions rather than in the base instructions:

> When you provide a response in the final channel, that content is immediately delivered back to your parent agent.
> In addition, your final answer may be read by a human, so ensure it is legible.

A role's instructions follow the system prompt. The rest of Codex's role text names Codex's v2 tools (`followup_task`, `send_message`), which uah does not offer. A fork gets no note: its history and system prompt are its parent's, so that its first request reuses the parent's prompt cache (`internal/agents/fork_test.go`). `TestParity_ChildSharesTheParentsSystemPrompt` pins the same prompt and the note.

## Decisions

- **The identity is "uah"** (hunks 1 and 3). Dropping them keeps "Codex"; they have no effect on tools.
- **No `request_user_input_async`** (hunks 2 and 4). The owner does not want the tool, so the model asks in its final message, or, where a user answers, with Codex's blocking `request_user_input` (item 118, [questions](questions.md)), which ends the model's turn until the answers arrive.
- **The environment in the system message**, fixed at session start, as above.
- **`uah prompts init`** writes the default as `system.md`, Codex's template (gpt-6.1-sol's) as `system-codex.md`, and the runner's host prompt as `system-runner.md`, with the last two keys commented out. `model_instructions_file` naming `system-runner.md` gives back the behavior before this item.
- **License.** Codex is Apache-2.0. The default prompt is a modified copy, so `THIRD_PARTY_NOTICES.md` lists it as modified, and `default_prompt.diff` is the notice of what changed. The notice is a Go comment on `DefaultPrompt`, not prompt text, because the model does not need it.

## Open

- A second `final_answer` in one run, when the model waits on running calls: drawing a `final_answer` as the answer only when it ends the run would remove the case.
- Commentary could fold after the answer, which would make "collapsed after the final answer is shown" true.
- When Codex's template changes, apply `default_prompt.diff` to the new `codex_prompt.md`; the test fails until both files and the diff agree.
