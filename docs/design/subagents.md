# Subagents: research and plan

Status: planned 2026-09-24, built the same day, then validated and hardened (see [As built](#as-built) and [Validation](#validation)), then extended in a second round (see [Round 2](#round-2)) and a third (see [Round 3](#round-3)). Ledger item 12. Codex facts are from openai/codex at rust-v0.156.1 (`C/` is `codex-rs/`); Claude Code facts are from its public documentation.

1. [How Codex does it](#how-codex-does-it)
2. [How Claude Code does it](#how-claude-code-does-it)
3. [What uah has to build on](#what-uah-has-to-build-on)
4. [Plan](#plan)
5. [Open decisions](#open-decisions)
6. [As built](#as-built)
7. [Validation](#validation)
8. [Round 2](#round-2)
9. [Round 3](#round-3)
10. [Round 4: leaving agents alone](#round-4-leaving-agents-alone)

## How Codex does it

- **Tools.** v1: `spawn_agent`, `send_input`, `wait_agent`, `close_agent`, `resume_agent` (`C/core/src/tools/handlers/multi_agents/`), taking `target` (`targets` for `wait_agent`, `id` for `resume_agent`) and returning `{agent_id, nickname}`, `{submission_id}`, `{status, timed_out}`, `{previous_status}`, and `{status}`. A status is `AgentStatus` in JSON: `"pending_init"`, `"running"`, `"interrupted"`, `"shutdown"`, `"not_found"`, `{"completed": message}`, or `{"errored": message}`; `interrupted` is not final (`C/core/src/agent/status.rs`). v2 (behind `features.multi_agent_v2`): `spawn_agent` with a required `task_name` and `message`, `send_message`, `followup_task`, `wait`, `list_agents`, `interrupt_agent` (`multi_agents_v2/`). `spawn_agent` returns the agent's ID and a nickname; the parent keeps working and calls `wait` only when it needs the result.
- **Children are threads.** A spawned agent is another conversation thread of the same session manager, inheriting the parent's model, sandbox, and approval policy unless the call or the role overrides model and reasoning effort.
- **Configuration.** `[agents]` in `config.toml` (`C/config/src/config_toml.rs:695`): `enabled`, `max_concurrent_threads_per_session` (alias `max_threads`), `max_depth` (v1 nesting), `default_subagent_model`, `default_subagent_reasoning_effort`.
- **Roles.** `C/agent-roles/`: role files with `name`, `description`, `nickname_candidates`, and any config keys as a layer over the parent's config (model, effort, instructions). The role list and descriptions go into the `spawn_agent` tool description.
- **Interrupts and completion.** Interrupting a thread (`Op::Interrupt`) aborts only that thread's turn (`abort_all_tasks`); its children keep running, and `send_input` with `interrupt: true` interrupts one child. When a child reaches a final status, a watcher injects a `<subagent_notification>` fragment into the parent's history without starting a turn (`C/core/src/agent/control.rs`, `maybe_start_completion_watcher`).
- **The prompt restrains spawning.** The tool description says: do not spawn unless the user or AGENTS.md/skills ask for delegation; research requests alone do not count; keep critical-path work local; give subagents disjoint write sets; call `wait` sparingly (`multi_agents_spec.rs:674`).

## How Claude Code does it

- **One tool.** `Task` (now "Agent") starts a subagent with a prompt and a `subagent_type`; the subagent runs in its own context window and returns one final report as the tool result. Several can run in parallel; subagents cannot start their own subagents.
- **Definitions.** Markdown files with YAML front matter in `~/.claude/agents/` and `.claude/agents/`: `name`, `description` (when to use it), optional `tools` (an allow-list) and `model`; the body is the system prompt.
- **Background runs.** A subagent can run in the background, and the parent is notified when it finishes.

## What uah has to build on

- **Sessions and engines.** A uah session owns a runner session; the embedded engine can run several coordinators in one process, each with its own session ID, inbox, and store (the runner's store is per session ID).
- **Asynchronous tools.** A spawned agent must not block the parent's coordinator. MCP (ledger item 5) introduces the mechanism for tools that run outside the coordinator; subagent tools use the same one.
- **Approvals and the sandbox** (items 2 and 3) apply to children unchanged; a child's approval prompt appears in the parent's TUI, labelled with the child's nickname.
- **Hooks** (M6) fire for children too; `SubagentStop` is added then.

## Plan

Follow Codex's v1 tool set, which covers Claude Code's single tool as its simplest use:

| Tool | Does |
| --- | --- |
| `spawn_agent(message, agent_type?, model?, reasoning_effort?)` | Starts a child session in the parent's workspace with the parent's sandbox and approvals; returns `{id, nickname}` at once |
| `send_input(id, message)` | Sends a follow-up message to a running or idle child (live on the embedded engine) |
| `wait(ids, timeout?)` | Returns when any listed child finishes or the timeout passes, with each finished child's final answer |
| `close_agent(id)` | Stops a child |

- **Children** are ordinary uah sessions (`session.Open` with a new ID and a sidecar `source: "subagent"` plus the parent's ID), so they are resumable, listed under the parent in `uah sessions`, and hidden from the resume picker.
- **Configuration** mirrors Codex: `[agents] enabled, max_concurrent_threads_per_session (default 6; Codex's is 4), max_depth (default 1: children cannot spawn), default_subagent_model, default_subagent_reasoning_effort`.
- **Roles** use Codex's role files, loaded from `~/.config/uagent/agents/*.toml` and trusted `.uagent/agents/*.toml`. The spawn tool description lists them, followed by Codex's restraint guidance, adapted.
- **TUI.** A child's progress is one line under the spawning tool call ("• agent reviewer: running 0:42"); `/agents` lists children; ctrl+t details show each child's tool lines.
- **Tests.** `fakellm` scripts for parent and child (one server, requests routed by the session ID in the prompt-cache key): spawn → wait → answer, a child's approval surfacing in the parent, the concurrency limit, depth 1.

Build it after items 2–5 merge, because it depends on asynchronous tools and approvals.

## Open decisions

Defaults taken; change them here before building.

1. **Workspace sharing.** Children share the parent's workspace (Codex v1). Codex's "forked workspace" guidance for code-edit subtasks is not built; git worktrees per child could come later.
2. **Claude Code's Markdown agent definitions** were not read at first, for one format; [Round 3](#round-3) reads them beside Codex's role files.
3. **v2 tools** (`task_name`, `followup_task`, `list_agents`, `interrupt_agent`) are left out until Codex makes v2 the default.

## As built

Built 2026-09-24 on unreal-agent-runner v0.1.1, and hardened the same day (see [Validation](#validation)). The code and its lifecycle are described in [internal/agents/README.md](../../internal/agents/README.md).

- **The seam.** `engine.Subagents` has four methods: `Attach` records a parent's run and returns the tools to offer it, `ToolNames` lists every name a call may use, `Call` runs one call, and `Interrupt` stops a parent's children. The embedded engine offers what `Attach` returns, resolves `ToolNames` even when nothing is offered (so a session with past calls resumes), and runs each call as a remote job, `uah.agent` v1, next to `uah.mcp_call` in the run's `LocalOperationManager`; it knows no tool name, schema, or result. A call runs on its own goroutine, so `wait_agent` never holds up the coordinator. A job that had started before a restart fails instead of running twice.
- **Tools** are Codex's v1 set with its names, parameters, results, and status encoding. Left out: `items` (structured input) and `fork_context` (a child starting from the parent's history), which the runner cannot do without changes. `wait` (uah's first name for `wait_agent`) still resolves but is not offered.
- **Children are the root agent** in every way except what makes them children. A child opens with `session.Open` on the parent's engine (wrapped so closing a child leaves the shared engine running) and the session options `app.Setup` returned for the root session, so it gets the same instructions and skills, sandbox, rules, approvals and auto-review, hooks, MCP servers, compaction, context meter, output limits, index, and sidecar. Its settings are the parent run's (`Settings.WithRequest` inverts the run's request) with its service tier. The only differences: its session ID and sidecar (`source: "subagent"`, `parent`), the spawn tools gated by `max_depth`, approvals through the parent, the model, effort, and instructions a role, the call, or `[agents]` defaults set, and a hook runner of its own. `sessions/<id>.agent.json` keeps the nickname, role, and the spawn call's model and effort for `resume_agent`.
- **Status.** A child is `running` from the moment a message is sent until its session is idle with every sent message accepted; it is then `completed` with the last run's answer, `interrupted`, or `errored`. `close_agent` and closing the parent make it `shutdown`; an unknown ID is `not_found`; a resumed child is `pending_init` until it gets a message. `wait_agent` returns every listed child already in a final status; `interrupted` is final here (see Validation).
- **Limits.** `max_concurrent_threads_per_session` counts open children in the whole tree under the root session; finished children count until closed, as in Codex. Depth comes from the live children and then the sidecars, so a resumed child keeps its depth. `max_depth = 0` or `enabled = false` offers no tools and still answers past calls.
- **Approvals.** A child's session asks through the parent session's `engine.Options.AskAnytime`, with `agent <nickname>:` before the justification. Unlike a run's own prompts, such a prompt stays open after the parent's run ends and can be asked while the parent is idle; it ends when answered, when the child is interrupted or closed, or when the parent session closes. The child's own run applies the auto-reviewer first, with the child's own transcript.
- **Interrupts.** Interrupting the parent's run stops the live runs of its children and their descendants; they stay open as `interrupted`, and `send_input` starts them again. `send_input` with `interrupt: true` interrupts one child and hands it the message at once.
- **Resume.** `resume_agent(id)` reopens a closed child, or a child of an earlier process, when its sidecar names the calling session as its parent; it keeps its history, nickname, role, and model. `send_input` to such a child says to resume it first.
- **Hooks.** `SubagentStop` runs when a child completes, with Claude Code's payload (`agent_id`, `agent_type`, `agent_transcript_path`, `last_assistant_message`, and the parent's `session_id`); a block with a reason sends the reason to the child as its next message (at most 5 times in a row). Children run every other hook as the root session does; PermissionRequest runs in the parent session, where their approvals go.
- **Progress.** `engine.AgentUpdated` (state) and `engine.AgentActivity` (a child's tool events) go into the parent session's stream through `engine.Options.Notify`, so a child that finishes after the parent's run ended still updates its line. The TUI draws one line per child (`KindAgent`) with its latest 30 tool calls under it in the detailed view (ctrl+t); `/agents` lists the children; `uah run` prints `agent <nickname>: <state>`, and the tool calls with `--verbose`.
- **Roles** load from `~/.config/uagent/agents` and a trusted workspace's `.uagent/agents` (recursive `*.toml`, later directories win). The subset read: `name`, `description`, `nickname_candidates`, `model`, `model_reasoning_effort`, `developer_instructions`, with Codex's validation; other keys produce a warning notice.

## Validation

A review on 2026-09-24 against Codex rust-v0.156.1. Before any change, every `internal/agents` test passed 20 times under `-race`. Each finding below is fixed with a test unless it says otherwise.

| # | Severity | Finding | Resolution |
| --- | --- | --- | --- |
| 1 | High | A child's approval was declined when the parent's run ended, and any child approval while the parent was idle was declined: the session accepted prompts only during a run. | Fixed: `engine.Options.AskAnytime`. Tests: `TestAgents_ChildApprovalOutlivesTheParentsRun`, `TestSession_AskAnytimeOutlivesTheRun`, `TestSession_AskAnytimeEndsWithItsContext`. |
| 2 | High | `close_agent` on a child waiting for approval hung until the harness killed the child's run: the prompt lived in the parent's session, and closing the child did not end it. | Fixed: a child's prompts end when it is interrupted or closed. Test: `TestAgents_CloseEndsAPendingApproval`. |
| 3 | Medium | The tools differed from Codex's v1: `wait` for `wait_agent`, `id`/`ids` for `target`/`targets`, `{id}` for `{agent_id}`, `{state, message}` for `AgentStatus`, `{id, status: "sent"}` for `{submission_id}`, `not_found` without an error from `close_agent`, `timeout_ms` ≤ 0 accepted, no `interrupt` on `send_input`, no `resume_agent`. | Fixed. Tests: `TestCall_Errors`, `TestStatus_JSON`, `TestTools_Offered`, and the end-to-end tests. |
| 4 | Medium | A long final answer pushed the `wait_agent` result over the runner's 40,000-character cap on a remote job's result; the runner cut the JSON in the middle and could drop other children's statuses. | Fixed: the messages share a 36,000-character budget, keeping each one's head and tail. Test: `TestAgents_LongAnswerKeepsTheResultValid`. |
| 5 | Medium | Progress updates could reach the parent out of order (the state was read under the lock and sent after it, from two goroutines), leaving a finished child drawn as running. | Fixed: one lock covers reading and sending an update. No deterministic test; the order holds by construction, and the tests pass 20 times under `-race`. |
| 6 | Medium | The auto-reviewer's transcript was one per engine: a child's messages joined the parent's, and the child's reviewer took over the parent's circuit-breaker reset. | Fixed: one transcript per session. Test: `TestTranscript_PerSession`. |
| 7 | Medium | Interrupting the parent left its children running, with no way for the user to stop them short of closing the session. | Fixed (a deliberate difference from Codex, whose TUI can interrupt each child): the parent's interrupt stops its children's live runs. Test: `TestAgents_InterruptStopsChildren`. |
| 8 | Low | A spawn racing the parent's Close could open a session nothing closed; a child whose first message failed stayed open and counted toward the limit; a spawn that finished after the parent's run stopped left the child running. | Fixed: the child is closed in each case. No deterministic test for the races. |
| 9 | Low | Children ran PreToolUse hooks (on the engine) but not PostToolUse hooks (in the session). | Fixed. Test: `TestAgents_SubagentStopHook`. |
| 10 | Low | `send_input` and `wait` could not reach children of an earlier process. | Fixed: `resume_agent`. Tests: `TestAgents_ResumeAcrossProcesses`, `TestAgents_ResumeOnlyOwnChildren`. |
| 11 | High | A child was built in a parallel, trimmed-down way: its settings were rebuilt from the parent's request and the configured base, so a fast mode switched on in the session did not reach it; it ran only PostToolUse of the session's hooks; its engine handle hid the MCP servers. | Fixed: a child opens with the root session's options from `app.Setup` and the parent run's settings, with only the differences listed in As built. Tests: `TestSetup_SubagentParity` (through `app.Setup`: the child's model request has the root's system prompt, model, effort, service tier, and tools, and Stop hooks run for both), `TestParity_ChildOptions`, `TestSettings_WithRequestInvertsRequest`. |
| 12 | Low | A child's end of work was found by counting queued messages; a message the session queued itself (a Stop hook's continuation) threw the count off, so an `Idle` from before a later message could end that message's work early. | Fixed: messages are tracked by ID. The interleaving has no deterministic test; the tests pass 20 times under `-race`. |
| 13 | — | Closing the parent closes every child, and job goroutines end with the run. | Verified: `TestAgents_CloseParentClosesChildren`; every test passes 20 times under `-race`. |

Differences from Codex, kept on purpose:

1. **Interrupts reach children.** Codex's interrupt stops only the parent's turn. uah has no per-child view to stop a child from, so the parent's interrupt stops its children too; they stay open.
2. **`interrupted` is final for `wait_agent`.** In Codex an interrupted agent is not final, so waiting on it runs to the timeout; in uah nothing but the parent's `send_input` starts it again, so `wait_agent` reports it at once.
3. **No completion notification.** Codex injects a `<subagent_notification>` into the parent's history when a child finishes. The runner's inbox (v0.1.1) takes only user messages, which would show as the user's input and start a turn; `wait_agent` returns a finished child at once instead, and the TUI line shows it.

Open:

1. **Stopping a child while the parent is idle.** The user can interrupt only a live parent run; an idle parent's children run until they finish, the model closes them, or the session closes.
2. **v2 tools** and `items` are not built (see Open decisions). `fork_context` is built in [Round 2](#round-2).

## Round 2

Built 2026-09-24 on unreal-agent-runner v0.1.1, against Codex rust-v0.156.1. Five requests: subagent session IDs, `fork_context`, per-subagent model, effort, and fast mode, a view of a subagent in the TUI, and evidence for parallel calls and for waiting without polling. A bug report added a sixth: a failed subagent must say why.

### Codex facts mirrored

| Fact | Codex source | uah |
| --- | --- | --- |
| `spawn_agent` has `fork_context: bool` (default false): "True forks the current thread history into the new agent; false or omitted starts with only the initial prompt." `agent_type` says "Omit to inherit the parent agent type with a full-history fork; otherwise, `default` is used." | `C/core/src/tools/handlers/multi_agents_spec.rs:18,608`; `multi_agents/spawn.rs:231` | Same parameter, description, and default (`prompt.go`) |
| A full-history fork with `agent_type` is refused: "Full-history forked agents inherit the parent agent type; omit agent_type, or spawn without a full-history fork." | `C/core/src/agent/child_config.rs` (`reject_full_fork_agent_type_override`) | Same message (`ops.go`) |
| A fork keeps the parent's settings: the role layer is skipped; the call's `model` and `reasoning_effort` and the `[agents]` defaults still apply | `child_config.rs` (`prepare_agent_spawn_config`) | Same (`spawnRole`, `childOptions`) |
| V1 hides the agent tools from a thread at the depth limit, and the handlers refuse a spawn or resume there: "Agent depth limit reached. Solve the task yourself." | `C/core/src/tools/spec_plan.rs:646`; `multi_agents/spawn.rs:73`; `multi_agents/resume_agent.rs:58` | Same, except a forked child keeps its parent's tools (below) |
| Every thread of a tree uses the root session's ID as its prompt cache key and, on the ChatGPT backend, its `session-id` header | `C/core/src/client.rs:561,577`; `session/session.rs:647`; `agent/control.rs:156` | `engine.Forker.SetCacheKey` gives every child its root's ID |
| A role file's `service_tier` is read (`fast` means `priority`) | `C/core/src/agent/role.rs:44,202` | Read (`roles.go`) |
| There is no `default_subagent_service_tier`: `[agents]` has only `default_subagent_model` and `default_subagent_reasoning_effort` | `C/config/src/config_toml.rs:695` | Not added |
| An unknown `model` fails at `spawn_agent`: "Unknown model `x` for spawn_agent. Available models: …", naming up to 5 listed models of the catalog; the call's model, else `default_subagent_model`, is checked | `child_config.rs` (`find_spawn_agent_model_name`, `MAX_SPAWN_AGENT_MODEL_OVERRIDES`); `C/models-manager/models.json` | Same message, on the openai-codex provider, against the bundled catalog (`models.go`) |
| The TUI's `/subagents` switches the view to another thread of the session; a V1 child takes the user's input directly (only V2 children are "parent owned") | `C/tui/src/slash_command.rs:139`; `app/session_lifecycle.rs:170`; `app/agent_navigation.rs` | `/agents <name>`: the child's live transcript, and messages go to it |

### As built

- **Session IDs.** A child's ID is `subagent-<uuid>` (`session.NewSubagentID`). The runner's store accepts ASCII letters, digits, and dashes (`localfile.validateSessionID`); uagent's session lock and run records take any ID; the index keys by the ID. `session.ShortID` prints `subagent-1a2b3c4d`, which `uah resume`, `uah sessions show`, and completion find as a prefix. Lists used to print an ID's first 8 characters, which would be `subagent` for every child. Older children keep plain UUIDs; the sidecar's `parent` identifies them. Test: `TestAgents_SpawnWaitAnswer` (the store, lock, run records, and `FindSession` with the short ID), `TestShortID`.
- **`fork_context`.** See [the agents README](../../internal/agents/README.md#forking). The embedded engine copies the parent's session items before the turn that made the spawn call into the child's session in one write, as the runner store's append methods write them, marks unfinished operations canceled, copies the compactions recorded until then, and puts the child's first messages in the store before its coordinator restores it.
- **Per-subagent model, effort, and fast mode.** `spawn_agent`'s `model` and `reasoning_effort`, a role's `model`, `model_reasoning_effort`, and `service_tier`, and the `[agents]` defaults. A child runs on the parent's provider with its own model: the embedded engine builds each run's client from the run's request, and `/context` and the compaction window read the session's model. `/context` for a child failed before (its engine handle hid the context reporter); fixed. Test: `TestAgents_ModelEffortAndFastPerChild`.
- **Failures.** A child whose run failed reports the cause in one line: the provider's JSON message when the runner error carries one. It reaches `wait_agent`'s `{"errored": …}`, `engine.AgentUpdated.Message` (the TUI's `/agents` and `Item.Agent`), and `uah run`'s `agent Ada: failed: …`. Before, the parent saw only "the run ended with status error". Test: `TestAgents_FailureReachesTheParent`, `TestReadable`.
- **The agent view.** `/agents <name>` shows a child's live transcript in the TUI, from a second `state.State` fed by `Session.WatchAgent`; see [the TUI README](../../internal/tui/README.md#the-agent-view). Tests: `TestAgents_Watch`, `TestReduce_AgentView`, `TestScreen_AgentView`.
- **`AgentUpdated` carries the whole picture**: the spawn call's ID and message, the model, effort, and whether the child was forked, so a renderer can draw `SPAWN Ada · gpt-6-luna low · Summarize…` and `WAIT Ada` from the latest update of each agent ID.

### Evidence

- **The fork's prompt prefix.** `TestFork_ChildStartsWithTheParentsRequest` scripts a parent that runs a command and then spawns with `fork_context`. The child's first request has the parent's system prompt, the same tools in the same order with the same JSON, the same prompt cache key, and starts with every input item of the parent's request that made the spawn call, byte for byte, the command's call and result included, followed by the child's message. `TestFork_KeepsTheParentsCompaction` does the same after a `/compact`: the child's request starts with the parent's compacted request. The child keeps the spawn tools at the depth limit, and its own spawn gets Codex's depth message.
- **Parallel calls.** `TestAgents_ParallelCalls`: three `spawn_agent` calls in one turn start three children that all ask the model while every answer is held, and of two `wait_agent` calls in one turn, the second returns (its child answered) while the first still blocks; the parent's next request has both results, since a turn's results wait for all of its calls ([the wake policy](../../internal/engine/README.md#the-wake-policy)). Each call is a remote job on its own goroutine (`agentJobs.AddRemoteJob`).
- **No polling.** `wait_agent` sleeps on `m.changed`, a channel closed and replaced at every status change, with a timer only for its timeout. A child's end of work comes from its session's `Idle` event, read by the `watch` goroutine that ranges over the session's event channel; SubagentStop hooks, approvals, and the TUI's agent view are driven by events and contexts too. No code in `internal/agents`, `internal/session`, or the embedded engine sleeps or ticks (checked with a search for `time.Sleep`, tickers, and `time.After`); the TUI's 100 ms tick runs only while something moves on screen, now also the viewed agent.

### Differences from Codex v1

1. **Interrupts reach children**, **`interrupted` is final**, and **no completion notification**: as in [Validation](#validation).
2. **A fork keeps the whole history.** Codex's fork keeps only system, developer, and user messages and final answers, and drops reasoning, tool calls, and their results (`C/core/src/agent/control/spawn.rs:72`, `keep_forked_rollout_item`), so its prompt prefix ends at the parent's first tool call. uah keeps every item, for the longest prefix.
3. **A forked child keeps the spawn tools** at the depth limit and refuses the calls, so its tools match its parent's. Codex hides them, which changes the tool list. Its first message, after the copied prefix, ends with a line saying that spawn_agent and resume_agent fail there: without it, 3 of 3 forks in a study spent their first turn on a refused spawn (one: about 53k tokens).
4. **A role's `service_tier` applies.** Codex reads it and then sets every child's tier to the root's (`child_config.rs`, `apply_spawn_agent_service_tier`; `C/core/tests/suite/subagent_service_tier.rs` spawns a `service_tier = "priority"` role under a root without a tier and expects no tier). uah honors the role, as asked; `"flex"` is ignored.
5. **The model check** uses the session's `models.Manager.Validate`: the provider's live model list (cached for 300 s, as Codex caches its catalog), on every provider that has one, with Codex's message and a did-you-mean suggestion. Only a live or cached list rejects a model; the bundled fallback never does. The effort is not checked against the model's levels, as Codex does.
6. **The view.** `/agents <name>` instead of Codex's `/subagents` picker with alt+← and alt+→; the view has no entry for the main agent (esc returns), and a grandchild cannot be viewed from the root.
7. **`items`** (structured input) is not built.
8. **The subagent note is a message, not system prompt text.** It follows the task in a child's first message (one message, since the runner may ask the model as soon as the first of two arrives), so a child's system prompt is its parent's, byte for byte; with it in the system prompt, a child's first requests were 32% cached against the parent's 94%. Codex keeps the base instructions verbatim too, and puts the role text in developer instructions, which uah's runner has no message for.
9. **Tool text about the cache.** `fork_context` says a fork keeps the cache only with the parent's model and effort (a fork from high to low effort got 7% cached, 97% at the same effort), and `wait_agent` says it returns as soon as an agent finishes, so a long timeout costs nothing (14 of 23 waits in one session polled at 10 s). The bounds stay Codex's.
10. **A child's result reaches the parent once.** A `wait_agent` that returns a child's final status withdraws its `<subagent_notification>` if it is still held for the parent's next message (`Inject` returns the withdraw); one session sent about 15 KB (3.8k tokens) twice before this.

### Open items

1. **The runner's `Store.Fork`** (v0.1.1) drops the operation snapshots of inherited tool calls ("TODO: Preserve status snapshots in forked history without making inherited operations dispatchable"), so uah replays the items itself, with an operation record of each copied operation's last status so that none is dispatchable. Once the runner keeps them, `Fork` can use it.
2. **A tool call still running** when the parent made the spawn call is canceled for the child, so the child's request differs from the parent's at that item.
3. **The fork's first run** is marked in memory: a process that exits between the spawn and the child's first run (which follows at once) leaves a child that asks the model without its message when resumed.
4. **Stopping a child from its view.** esc returns; the view has no interrupt of its own yet.

## Round 3

Built 2026-09-24 against Codex rust-v0.156.1 and Claude Code's documentation of the same day. Ledger items 31 (custom agents as Markdown; subagents never start subagents) and 32 (a real probe of the fork's prompt cache).

### Facts mirrored

| Fact | Source | uah |
| --- | --- | --- |
| Subagent files are Markdown with YAML front matter in `.claude/agents/` (project) and `~/.claude/agents/` (user), searched with subfolders; the project wins on a name clash | [code.claude.com/docs/en/sub-agents](https://code.claude.com/docs/en/sub-agents), "File locations and precedence" | `~/.config/uagent/agents/` and a trusted `.uagent/agents/`, recursive; the project wins |
| `name` and `description` are required; the body is the system prompt | the same page, "YAML frontmatter fields" | Required; the body is `developer_instructions` |
| `tools` is a comma-separated string or a YAML list; omitted inherits every tool; `mcp__<server>` or `mcp__<server>__*` names a server's tools | the same page, `tools` | Both forms; omitted is every tool; the same patterns |
| `model` is `sonnet`, `opus`, `haiku`, `fable`, a full ID, or `inherit` | the same page, `model` | `inherit` and the aliases are the parent's model (the aliases with a warning); a full ID is used on the parent's provider |
| `effort` is `low` to `max` | the same page, `effort` | Read, beside Codex's `model_reasoning_effort` |
| Unknown fields are ignored without an error | the same page | Ignored with a warning, as uah treats TOML roles |
| `tools` restricts but does not pre-approve; permission prompts still apply by the subagent's `permissionMode`, and a subagent never gets `bypassPermissions` when the parent does not have it | the same page, "permissionMode" | `tools` restricts; `approve` is uah's own key for pre-approval, and it never widens the parent's mode; `permissionMode` is not read |
| By default a subagent may spawn subagents up to three levels down; at the limit the Agent tool is withheld from every subagent except a fork, which keeps the main conversation's exact tool pool | the same page, "Subagent spawning" | Depth is fixed at 1; a fork keeps its parent's tools and its spawns are refused |
| Role files are TOML: `name`, `description`, `nickname_candidates`, and a config layer; `developer_instructions` is required; unknown top-level keys are an error (`deny_unknown_fields`) | `C/agent-roles/src/agent_role_config.rs:20-28,134-157` | Unchanged TOML loading, unknown keys warn |
| Role files are found recursively as `*.toml` under the layer's `agents/` directory | `C/agent-roles/src/discovery.rs:7-40` | `*.toml` and `*.md` |
| A name declared twice in one layer is a warning, and the first is kept | `C/agent-roles/src/loader.rs:60-90,317` | A warning; the Markdown file is kept over a TOML file, else the later file |
| "Roles may customize the child or reduce its capabilities, but never replace the parent session's authority": a role can only turn features off | `C/core/src/agent/role.rs:1-4,91-104` | `tools` only narrows, and `approve` answers prompts within the mode |
| At the depth limit V1 hides the agent tools and refuses spawn and resume with "Agent depth limit reached. Solve the task yourself." | `C/core/src/tools/spec_plan.rs:646`; `multi_agents/spawn.rs:73` | The same message |

### As built

- **The format and loading.** See [the agents README](../../internal/agents/README.md#markdown-agents). `LoadRoles` reads both formats; the front matter is parsed with `go.yaml.in/yaml/v3`.
- **Tools.** A role's `tools` becomes an `engine.Scope` for the child's session. The embedded engine adds the built-in tools it leaves out to the run's `DisallowedTools`, which the registry already honors, and drops the MCP tools it leaves out, so the child's model request lists only the allowed tools (`TestScope_Tools`).
- **Pre-approval.** A role's `approve` prefixes are prefix rules of the approver's kind, consulted where the approver would ask, before the auto-reviewer; MCP names make the MCP gate treat the tool as `approval_mode = "approve"`. They are not `allow` rules, which run a command outside the sandbox without asking. A `forbid` rule and `approval_policy = "never"` decide first, and in read only mode an escalation is still asked (`TestScope_PreApproval`, `TestScope_PreApprovalKeepsReadOnly`).
- **Depth.** Fixed at 1. `max_depth` above 1 is clamped to 1 with a notice rather than refused, so a configuration written for Codex still starts; `max_depth = 0` still turns subagents off (`TestDepth_ChildrenNeverSpawn`, `TestSetup_MaxDepthIsOne`).

### Validation: the fork's prompt cache on openai-codex

Three small probes with `uah run --stdin --stream` on openai-codex, `gpt-6-luna` at low effort, in an empty temporary workspace with an empty configuration and its own state directory. The parent answered two or three one-word turns, then spawned a child with `fork_context: true` and waited for it. The numbers are `llm.Usage` from each run's `events.jsonl` (input tokens / cached input tokens); the provider's per-item attribution (`Usage.Raw.attribution`) shows which items were cached. The second and third probes left 10 to 12 seconds between the parent's turns.

| Probe | Parent's request that made the spawn call | Child's first request | Child's second request | Parent's last request |
| --- | --- | --- | --- | --- |
| 1 (turns back to back) | 4,653 / 3,584 | 4,665 / 3,584 | 4,741 / 3,584 | 4,835 / 4,608 |
| 2 (12 s between turns) | 4,655 / 3,584 | 4,674 / 3,584 | 4,756 / 3,584 | 4,847 / 4,608 |
| 3 (as 2, one HTTP transport for every run) | 4,655 / 3,584 | 4,674 / 3,584 | none | 4,838 / 4,608 |

- **The prefix is the parent's.** The child's first request is the parent's request plus the child's message: the attribution lists the same items with the same token counts (the 2,113 tokens of tools, the 2,455-token system message, then the conversation), 12 to 19 tokens longer. `TestFork_ChildStartsWithTheParentsRequest` pins the same byte for byte with fakellm, and a capture of two real request bodies from separate runs (a local server as the base URL) showed the system message and the tools identical across runs.
- **The cache reuse is partial.** The child's first request had 3,584 of 4,665 tokens cached (77%): the tools and the first 1,471 tokens of the system message. That is exactly what every request that starts a run got, the parent's own second and third turns included, whose prefix had been sent 10 seconds earlier in the same session with the same prompt cache key and `session-id` header. Only a request later in the same run went further (4,608, the whole system message and the earlier turns), and then only once that prefix had been sent at least twice. The child's second request, 5 seconds after its first, got 3,584 too.
- **Why.** Not found in uah. The child's `prompt_cache_key` and `session-id` header are the root session's, as in Codex (`C/core/src/client.rs:561-586`: a subagent's header is the tree's `session_id`, the root's). Sharing one HTTP transport across runs (probe 3) changed nothing. Codex differs in transport: it keeps a websocket per session across turns (`client.rs:617`) and replays the backend's `x-codex-turn-state` sticky-routing token within a turn (`C/core/src/client.rs:278-298`), which the runner's HTTP client cannot. The likely cause is backend routing between requests that do not share such state; it limits the parent's own turns as much as the fork.
- **Conclusion.** The fork keeps the parent's prefix exactly, so it never does worse than the parent's own next turn; on openai-codex today that means the common prefix (about 3.5k tokens here) is cached and the parent's history is not. The cost of a fork's first request is its uncached history.

Open:

1. **Cross-run cache on openai-codex.** Requests that start a run get only the common prefix cached. Following Codex's transport (a websocket kept across turns, `x-codex-turn-state`) would need the runner's client; a probe with a longer history (tens of thousands of tokens) would show whether the backend caches more beyond some size.
2. **`permissionMode`, `disallowedTools`, `skills`, `mcpServers`, `hooks`, `maxTurns`, `isolation`, and `color`** from Claude Code's front matter are not read. `permissionMode` could only make a child stricter; the others need features uah's children do not have.
3. **Read, Grep, and Glob** have no tools of their own in uah; an agent limited to them gets no tools and a warning.

## Round 4: leaving agents alone

2026-10-06, ledger item 140, against Codex rust-v0.160.1. The owner: "our harness micromanages sub-agents a lot". Measured first, changed second, then an A/B run.

### What the sessions showed

The owner's `~/.uah/sessions` had 8 parent sessions that spawned subagents (22 children, not counting `/review` reviewers).

- **Short polls.** 46 `wait_agent` calls, 21 of them timed out. None used the default; 22 asked for 10 s or less, and one parent waited 10 s thirteen times in 9 minutes.
- **Noise messages.** Of 36 `send_input` calls, 22 were noise: 14 asked for status or told the agent to hurry, wrap up, or "return your final report now", 2 asked again for a result the harness had dropped, 2 corrected the parent's own earlier message, and 4 critiqued drafts the parent had read from the agent's files mid-run. 8 passed on the parent's own findings, and 6 were useful: a failure fixed or a new task for a finished agent. Both interrupts were noise. The owner wrote "stop micromanaging your subagents btw" into one of these sessions.
- **Cost.** 194 parent model requests ran while a child worked. 60 of them only waited or messaged, and those read 5.4M input tokens.
- **File-hunting.** One parent listed and read its agents' output files eight times while they ran, then interrupted them because it "has read it fully".

### Causes

1. **The notification came late.** `Session.Inject` held a child's `<subagent_notification>` for the parent's next run. So a parent that worked while its children ran heard of their end only by calling `wait_agent`. The notes were measured at 85 s to 17 minutes late. In one session six arrived together with the user's next message, after the parent had asked two finished agents for their reports. Codex puts the note into the active turn (`inject_no_new_turn` tries `inject_if_running` first, `core/src/session/inject.rs:170-188`). uah had held it to avoid cancelling a model request, but a developer message reaches the run without cancelling one.
2. **The guidance said not to wait.** Codex's "After you delegate" text was kept as it is: "Call wait_agent very sparingly", "do meaningful non-overlapping work immediately", and "Do not repeatedly wait by reflex". Together with cause 1, keeping busy meant duplicating the agent's work, reading its files, or messaging it.
3. **The wait's defaults.** A wait lasted 30 s by default and at least 10 s, as in Codex. A timeout returned `{"status":{},"timed_out":true}`, which did not say that the agents were fine.
4. **A dropped answer.** A message to a working child starts another run after the current one, and the child's status kept only the last run's answer.
5. **`send_input`** offered "interrupt=true to redirect work immediately".

The parent model sees no status line while its children run; the TUI's agent lines are the user's. The `/review` note ("leave it alone unless the user asks") stays as it was.

### As built

- The notification goes into the parent's live run as a developer message (`Session.Inject`), and is held only when the parent is idle. A wait that returns a status the parent was already told about says so, and does not repeat the answer.
- A child's completed status keeps every answer since it was last idle.
- `wait_agent` waits 4.5 minutes without `timeout_ms`, and that is also the most it allows. This stays just under the engine's 5-minute wake hold, which would otherwise wake the parent only to say that the wait still runs. The least is one minute. A timeout adds a note: the agents are still working, nothing is wrong, wait again.
- The descriptions: do your own part, then call `wait_agent` once with every agent's ID. A finished agent's answer comes on its own. Message a running agent only to pass on news from the user, to answer its question, or to stop a clear failure. Do not read its files. `send_input` says the same, and keeps `interrupt` for a failure.

### A/B

The control (A) is v1.9.5. B is this round without the last change, and C is this round as built, with a wait that does not repeat an answer the parent was already told about. All runs used gpt-6.1-sol at high effort in auto mode, with the owner's environment and adaptive effort at 2 steps (`-owner-env -uah-env UAH_ADAPTIVE_EFFORT=2-steps`). The tasks are the three new `go-subagents-*` tasks, which ask for subagents, and `go-branch-review`, where the model spawns some on its own. `go-subagent-audit` (4 runs per arm) never spawned: its prompt does not ask for subagents. A and C ran 8 times per task in the same window, 64 runs ([raw](../../tools/agentbench/history/2026-10-06-subagentcalm.jsonl), with B's 36 and A's first 36). Totals are sums over the runs, and wall time is the sum of per-task medians.

| | A: v1.9.5 | C: as built | Change |
| --- | ---: | ---: | ---: |
| Passed | 31/32 | 31/32 | |
| Wall | 1177 s | 1193 s | +1% |
| Input tokens | 12.57M | 11.19M | −11% |
| Output tokens | 138k | 133k | −4% |
| Estimated cost | $5.14 | $4.83 | −6% |
| Waits (timed out) | 90 (39) | 68 (1) | |
| Messages and interrupts to a working agent | 2 | 1 | |
| Parent requests while agents worked (only waits or messages) | 140 (71) | 95 (53) | −32% |
| Their input tokens | 3.02M | 1.97M | −35% |
| Notifications the parent got | 0 | 44 | |

- **No more polling.** v1.9.5's parents waited 10 s again and again: 39 of 90 waits timed out. C's parents waited once per finished agent. The one timed-out wait asked for 60 s. Over all runs, B and C timed out 1 of 142 waits, and A 72 of 184.
- **Fewer interventions.** A's parents read the agents' work in progress and messaged them while they worked ("Please add exact reported regression … before final"), or interrupted them ("Finish now with static findings; no more tools needed"). C's one intervention was an interrupt. The parent had mixed up two agents' packages: it asked a finished agent to redo its package, then stopped it once another agent had supplied that diagnosis.
- **The same pass rate and wall time.** Both arms missed one `go-branch-review` each, the task's known weak spot. Per task, the wall time moved by −9% to +12%, within the spread of 8 runs. B's 36 runs, in an earlier window, gave the same picture against A's 36: 36/36 against 35/36, wall time level (1603 s against 1606 s), input −10%, and 0 timed-out waits against 33.
- **One answer, once.** In B, 21 of 41 waits returned a status whose notification the parent had just received, 8.3 KB in all; in C those waits point at the notification.

Kept: C. The A/B had no session as long as the owner's (a few minutes per agent), where the late notification did the most harm, so the gain there should be larger than these short tasks show.

