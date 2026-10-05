<!-- memoria:section id="overview" files="design/harness.md design/tui.md design/implementation.md design/state.md design/sandbox-research.md design/sandbox.md design/subagents.md design/compaction.md design/mcp.md design/usage.md design/images.md configuration.md ledger.md documentation/architecture.md documentation/memoria.md context-preparation.md design/shell-mode.md design/streaming.md design/markdown.md design/rewind.md design/selection.md design/codex-auth.md design/hosting.md design/editor.md design/system-prompt.md design/web-search.md design/review.md design/prompt-history.md design/tool-calls.md design/keys.md design/questions.md design/goal.md" -->
# Documentation

<!-- memoria:export id="summary" -->
The configuration reference, the context preparation guide, design records for the harness, the TUI, state storage, sandboxing, compaction, MCP, subagents, pasted images, streaming, Markdown rendering, going back to an earlier message, selecting text with the mouse, editing the prompt in an editor, the system prompt, web search, `/diff` and `/review`, goals (`/goal`), prompt history and the composer's height, how tool calls read in the transcript, keeping the ChatGPT login fresh, and running uah as a terminal host backend, plus the architecture rules and documentation procedure for uah.
<!-- /memoria:export -->

Where to start:

1. The [root README](../README.md): what uah does and how to use it, and a link to every package README.
2. The [architecture rules](documentation/architecture.md): the packages, what may import what, and the rules every change follows.
3. The package README of the code you change: its current contract.
4. The [configuration reference](configuration.md).

The design records below keep the research and the decisions behind each feature. Each has a `Status:` line; the ones marked historical describe a design that was replaced, and name what replaced it.

Design:

1. [Harness design](design/harness.md) (historical): what the runner provided, what the harness added, the two engines it planned, and the scope accepted then.
2. [TUI design](design/tui.md) (historical): the framework choice, architecture, screens, keys, and commands as first designed, and the move from Bubble Tea to uah's own terminal layer with its measurements.
3. [Implementation spec](design/implementation.md) (historical): the first plan of packages, files, types, and milestones; the [architecture rules](documentation/architecture.md) have today's packages.
4. [State storage](design/state.md): what is stored where, and the plan for a rebuildable SQLite index.
5. [Sandboxing and approvals: research](design/sandbox-research.md) (historical): how Codex sandboxes and approves commands, and the options for uah.
6. [Sandboxing and approvals: plan](design/sandbox.md): the decisions (Codex's defaults), how a command runs, the packages, the phases, the 1.8.4 hardening (the sandboxing scripts kept read-only, patches that never follow a symlink put in after the check, forbid rules first for every patch), and the auto-reviewer: what Codex's does, uah's review conversation with deltas and read-only commands, and its measurements.
7. [Subagents](design/subagents.md): how Codex and Claude Code run subagents, and the plan for uah.
8. [Compaction](design/compaction.md): how Codex compacts, locally and remotely, what the runner supports, the design, the offline evaluation over recorded sessions and its numbers, the state ledger, elision, the kept calls and the summary prompt, remote compaction with its probes, and the open decisions.
9. [MCP](design/mcp.md): how Codex runs MCP servers, how their tools run as the runner's remote jobs, resources, prompts, restarts, tool list changes, and logins in a running session, OAuth, the validation findings, and the open decisions.
10. [Subscription usage](design/usage.md): how Codex reads the ChatGPT plan's rate limits, what uah can read with the same login, and the design uah built in [internal/usage](../internal/usage/README.md), which `uah usage`, `/status`, the footer, and `uah doctor` read.
11. [Pasting images](design/images.md): how Codex and Claude Code paste images, what the runner and uagent carry, and the design with its open decisions.
12. [Shell mode](design/shell-mode.md): how Codex and Claude Code run a `!` command the user types, and how uah runs it and adds it to the conversation.
13. [Streaming the answer](design/streaming.md): how Codex streams the answer, why the runner needs no change, and how the embedded engine tees each turn request's stream into the TUI.
14. [Markdown rendering](design/markdown.md): how Codex draws and streams Markdown, the parser, incremental rendering by blocks, tables that fit the width, cached highlighting, the look chosen for code, tables, quotes, and headings, and the benchmarks that gate it.
15. [Going back to an earlier message](design/rewind.md): Codex's backtrack and Claude Code's rewind, and how uah cuts the context at an earlier message while the session file keeps the old branch.
16. [Selecting and copying text](design/selection.md): what Codex and other terminal programs do with the mouse, and how uah selects transcript text, keeps it on its text while the transcript moves, and copies it on release.
17. [Keeping the ChatGPT login fresh](design/codex-auth.md): when and how Codex refreshes its token in `auth.json`, and how uah does the same with Codex writing the same file.
18. [Integration with the terminal host](design/hosting.md): what the terminal host needs from a harness, and the changes that let it run uah inside the mechanisms it keeps for every harness.
19. [Editing the prompt in an editor](design/editor.md): what Claude Code's and Codex's ctrl+g do, and how uah runs `$VISUAL` or `$EDITOR` on a draft file the sandbox cannot reach, with the terminal released, and keeps its images.
20. [The system prompt](design/system-prompt.md): Codex's prompt for gpt-6.1-sol with the nine changes uah needs, each with its reason, and Codex's `<environment_context>` at the end of the system message.
21. [Web search](design/web-search.md): how Codex offers the hosted `web_search` tool, what the runner sends and drops, how uah offers it, shows each search, and puts the dropped searches back into later requests by insertion, the probes on openai-codex, and the limits.
22. [`/diff` and `/review`](design/review.md): how Codex collects and shows the git diff and runs a code review in a separate thread, and how uah shows the diff and runs a read-only reviewer, from the TUI or `uah review`, whose steps the TUI shows as it works and whose findings, sorted with their confidence, reach the agent.
23. [Prompt history and a taller composer](design/prompt-history.md): how Codex stores prompts in `history.jsonl`, recalls them with ↑ and ↓, and searches them with ctrl+r, what Claude Code does, and how uah does the same with Codex's file format plus each prompt's workspace, shows each folder only its own prompts as Claude Code does, resolves the key conflicts, and grows the composer to half the window.
24. [Tool calls in the transcript](design/tool-calls.md): the owner's pick from the gallery (R6 with spacing b), the port of Codex's command classifier, where the error lines and MCP results come from, and how an approval finds its call.
25. [Send keys](design/keys.md): how Codex and Claude Code bind send-now and queue, what the terminal reports about ctrl+enter and shift+enter (tmux measured), and why uah binds enter to send before the next model request and tab to queue in every terminal, as Codex does.
26. [Agent tuning](design/agent-tuning.md): every agent-benchmark measurement of uah against Codex, the experiments (freeform `apply_patch`, async prompts, wake policies and their preamble, a primed first turn, effort by turn, an automatic check after edits), the full suite against Codex, adaptive effort's rules (Lean mode while measured), escalation on failure, and the prompt cache, parallel approvals, network commands escalated up front, what adaptive effort costs in chats of several messages (the per-effort prompt cache), what was decided, and the release-note summary.
27. [Adaptive effort costs](design/adaptive-effort-costs.md): effort updates, which keep one prompt cache across effort switches, what keeping one prompt cache per effort costs, what lower effort saves in usage and time, a three-term model that matches the benchmark within about 2 points, projections by session length, usage on a subscription, how certain each claim is, and how often real sessions' pauses outlast the cache.
28. [The agent's questions](design/questions.md): Codex's blocking `request_user_input` (its schema, where it is offered, the TUI overlay, the answer's encoding, `codex exec`'s refusal) and Claude Code's AskUserQuestion, and how uah offers Codex's tool where a user answers, waits for the answer as for an approval, and draws the picker above the composer.
29. [Goals](design/goal.md): Codex's `/goal` (how the goal is stored and shown to the model, when it continues, who decides it is done, its budget and guards, the commands and the footer, compaction, subagents) and the loops of Claude Code's ralph-loop and oh-my-opencode, and how uah keeps the goal in the session with Codex's tools and texts, plus a cap on continuations and a stricter no-progress guard.

Reference:

1. [Configuration](configuration.md): every configuration key with its type, default, flag, and merge rule; the home `~/.uah` and its files, the configuration layers, the precedence, and complete examples.
2. [Context preparation](context-preparation.md): the message every new session starts with, its blocks, the module format and when a module applies, placeholders, where modules come from, the library, security, the commands, recipes, and troubleshooting.

Work:

1. [Ledger](ledger.md): the definitive list of work in progress, its scope, lanes, and log.

Maintenance:

1. [Architecture](documentation/architecture.md): the package roles and rules every change follows.
2. [Writing guidance](documentation/writing.md): voice, README shape, and what belongs where.
3. [Section IDs](documentation/sections.md): the IDs that map README sections to source files.
4. [Memoria procedure](documentation/memoria.md): which README covers a file, how to review and acknowledge documentation after a change, and what to do when the documentation rules change.
<!-- /memoria:section -->
