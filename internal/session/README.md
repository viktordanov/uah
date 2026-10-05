<!-- memoria:section id="overview" files="session.go loop.go events.go" -->
# Sessions

A session is the long-lived object the TUI and `uah exec` talk to. It owns the settings, a queue of messages, at most one live run, pending approvals, and the hooks, and it merges run events and its own events into one ordered stream.

<!-- memoria:export id="summary" -->
A session owns its settings, a message queue, at most one live run, pending approvals, and its hooks on one goroutine, and merges run events and its own events into one ordered stream. Messages queue while the agent works, a steer reaches the running agent when the engine allows it, and an interrupt keeps the queue. An active goal (`/goal`) keeps the agent working, run after run, until the model marks it complete or a guard stops it.
<!-- /memoria:export -->

1. [The loop](#the-loop)
2. [Messages: queue, steer, interrupt](#messages-queue-steer-interrupt)
3. [Settings and compaction](#settings-and-compaction)
4. [Approvals](#approvals)
5. [Hooks](#hooks)
6. [Goals](#goals)
7. [Files and history](#files-and-history)
8. [Events](#events)
9. [Tests](#tests)
<!-- /memoria:section -->

<!-- memoria:section id="loop" files="session.go loop.go runs.go mcp.go" -->
## The loop

`Open` starts one goroutine, `loop`, that owns all session state. Every public method (`Submit`, `SteerNow`, `SteerQueued`, `Interrupt`, `Withdraw`, `SetSettings`, `Compact`, `Rewind`, `Resolve`) sends a command on the `in` channel and waits for the reply (`call[T]`). Engine callbacks do the same: the run's sink, `Start`'s result, `Wait`'s result, hook results, and approval requests all arrive as messages. So nothing else needs a lock, and `Events()` has exactly one writer.

The loop tracks where the session is in a run:

| State | Means |
| --- | --- |
| `idle` | No run. A message starts one |
| `starting` | `Engine.Start` runs on another goroutine; the loop gets `evStarted` |
| `running` | A run is live; another goroutine waits on `Run.Wait` and sends `evEnded` |
| `stopping` | An interrupt was sent; the run has not ended yet. A second interrupt (one esc in the TUI) kills the run (`engine.Run.Kill`): uagent tears it down at once instead of waiting out the grace period, and abandons an agent that outlives the kill by another grace period, so a stop cannot hang the session |
| `closed` | `Close` finished: SessionEnd hooks ran and `Events()` is closed |

`Close` interrupts a live run, waits for it to end, closes the event stream, and then closes the engine when it is an `io.Closer` (MCP servers and subagents stop with the session).

`Open` shows `Options.Notices` after `SessionOpened`, such as configuration warnings, and reads `Engine.Priority` once for `Session.Priority`, which the TUI's `/fast` reads. The session never checks the engine's name.

An `Options.Interactive` session connects the engine's MCP servers as it opens, when the engine is an `engine.MCPStarter` (`mcp.go`), with no message and no model request: a terminal host that waits for a server's `initialize` before it sends the first prompt can then start uah. A goroutine waits until each server has started or failed, then the loop shows a notice for each one that did not start (an error for a `required` one, whose failure fails every message) and sends `MCPStarted`. Runs, `/clear`, and subagents use these connections. `uah exec` is not interactive: its first run, which starts at once, connects them.

`MCPServers` reports the servers for `/mcp` (`engine.MCPLister`), and `MCP` hands the TUI the engine's `mcp.Manager` (`engine.MCPClient`) for the user's own use of them: prompts as slash commands and resources after "@". A run picks up a server's changed tools, or a server reconnected after a login, when it starts; the session does nothing for that.
<!-- /memoria:section -->

<!-- memoria:section id="messages" files="dispatch.go runs.go inject.go history.go shell.go review.go" -->
## Messages: queue, steer, interrupt

Every message gets an ID and is reported as `InputQueued`, then `InputSent` when it goes to the runner, and `InputDelivered` when the runner echoes it as a `UserMessage`. Messages that never reached the runner are reported as `InputFailed`.

What `dispatch` does with a message depends on the state and on whether it is a steer (`SteerNow`, or `Send` with `SendNow`: ctrl+enter or alt+enter in the TUI) or a plain submit (`Submit`: tab in the TUI). Enter while the agent works is `SendAfterTool`, below:

| State | Submit (`Submit`) | Steer (`SteerNow`) |
| --- | --- | --- |
| `idle` | Starts a run with the queue, then this message | The same |
| `running` | Queues it; the queue starts the next run | With `LiveInput`, sends it into the run. Otherwise queues it and interrupts; a new run starts with the queue |
| `starting` | Queues it | With `LiveInput`, holds it and sends it once the run has started. Otherwise queues it and interrupts |
| `stopping` | Queues it | Queues it; a new run starts after the stop |

`Send` with `SendAfterTool` (enter while the agent works) queues the message and marks it (`InputQueued.AfterTool`, so the TUI labels it). `sendAfterTool` steers the marked messages into the run, in order, when no model response is under way (a `core.TurnStarted` without its `core.ModelResponded`) and every `core.ToolCalled` has its `core.ToolFinished`: at once when that holds as the message arrives, else at the `ToolFinished` that ends the last running call. So the message never cuts off a response and rides the request after the tool calls' output. Until then it is an ordinary queued message: `Withdraw` takes it back, `SteerQueued` sends it, and when the run ends first it goes out with the next run (the marks are dropped then).

`SteerQueued` (enter or ctrl+enter on an empty composer) steers every queued message, in order, each keeping its ID: `dispatch` handles each as a steer, and messages still waiting for their hooks go as steers once the hooks allow them. While idle, with the queue a user interrupt kept, the messages start a run together. It reports how many messages it took, and does nothing when none are queued.

When a run ends, messages sent into it that it never read go back to the front of the queue. After a user interrupt (esc esc, `/stop`) the queue stays and the session goes idle; otherwise the queue starts the next run at once. `Withdraw` takes a message back while it is queued or waiting for its hooks.

The queue survives a restart. After each message the loop handles, `saveQueue` compares the unsent messages (the queue, including the ones held for after the tool call, the steers waiting for a run to start, and those waiting for their hooks, in that order) with what the sidecar keeps, and rewrites its `queued` field when they differ; so a sent or withdrawn message leaves the file as it leaves the queue. A close keeps them: messages sent into the run it stops that the run never read go back to the queue first. When a session opens with a sidecar that has `queued`, `restoreQueue` queues the texts again under new IDs, reports each as `InputQueued` (without `AfterTool`: there is no run to hold them for) and then `Idle`, and sends nothing. `Open` reports them before it returns, when its caller cannot yet read `Events`, so it makes `Events` that much longer than its usual 4,096 events (`queuedRoom`): a kept queue of any length opens, and the caller can send a message before it reads them, as `uah exec` sends its prompt; they go out with the next message, or with `SteerQueued`, as a queue an interrupt kept. A new session has a new ID and so no sidecar to read; a forked subagent gets its own sidecar, never its parent's. Only the text is kept, so a pasted image comes back through its tag line.

A message may carry images pasted in the TUI as tag lines at its end (`internal/images`: `<uah-image label="[Image #1]" ref="<sha256>.png" …/>`, the image in `<state>/images`). The session treats them as text: they queue, steer, reach the hooks and the run records, and resume with the message. The embedded engine turns them into image input for the model; `Info.FirstPrompt` and the TUI show the text without them. See the [images design](../../docs/design/images.md).

`Inject` gives the agent a message without a turn of its own, as Codex's `inject_no_new_turn`. During a run it goes into the run as a developer message, which asks for no response of its own and so cancels no model request (a user message would): it rides the run's next request. Until the runner records it (its `core.DeveloperMessage` echo) it is kept in `injected`, and a run that ends first holds it again for the next run, since the runner's inbox lives in memory. Otherwise it is held and goes out before the next run's messages, and it never starts a run. It skips the queue and the hooks. A subagent's `<subagent_notification>` reaches its parent this way (`engine.Options.Inject`), so a parent that works while its children run hears at once that one finished. `Inject` waits for the session's loop and reports whether the message went into the live run, and returns a withdraw that takes it back if it is still held, which the subagent manager uses when `wait_agent` returns the same status. Until v1.9.5 it was always held, so a parent heard of a child's end only by polling `wait_agent` ([ledger](../../docs/ledger.md) 140).

`RunShell(ctx, command)` runs a command the user typed (the TUI's `!` shell mode) with `Options.Shell`, a `usershell.Runner` that `internal/app` builds, outside the engine. It runs at once, in any state, also while a run is live, as Codex's user shell commands do. `ShellStarted`, `ShellOutput`, and `ShellFinished` report it. The session's interrupt stops it, and so does `Close`. Its record, Codex's `<user_shell_command>` message with the command, exit code, duration, and output cut to 40,000 characters, is held as `Inject` holds a message, with the command's ID: it goes before the next run's messages and never starts a run. A failed, stopped, or refused command is recorded too. By default the command runs outside the sandbox and the command rules, as in Codex; `user_shell_sandbox = true` runs it in the sandbox of the current permission mode and lets a `forbidden` rule refuse it. The [shell mode design](../../docs/design/shell-mode.md) has the research and the decisions.

`Review(ctx, target)` runs the TUI's `/review` and `uah review` as Codex runs its review thread. It asks the engine's `Reviewer`, `agents.Manager`, for the reviewer's settings (`ReviewSettings`), builds Codex's prompt for the target (`internal/codereview`), and hands both to the reviewer, which runs a fresh read-only session beside this one and returns its first answer, the tokens it used, and the limit that stopped it, if one did (`ReviewAnswer`; see [internal/agents](../agents/README.md#code-reviews)). One review runs at a time, in any state; the session's interrupt stops it, and so does `Close`. `ReviewStarted` (with the reviewer's model and effort), `ReviewActivity` (the reviewer's tool events, its failed commands' output, and its model responses), and `ReviewFinished` (the parsed findings, or interrupted, or the error, the tokens, and `Limit`: `ReviewTimeLimit` or `ReviewTokenLimit` when one stopped it) report it. Codex's hand-over message, a `<user_action>` with the findings or the interrupted form, is held as `Inject` holds a message: it goes before the next run's messages and never starts a run. An engine without subagents returns `ErrNoReview`. The [review design](../../docs/design/review.md) has the research and the decisions. The session picks the reviewer's session ID (`ReviewRequest.ReviewerID`). A run that starts while a review runs begins with a developer message, `reviewRunningNote`: the review is the user's, its reviewer is the agent with that ID, to leave alone unless the user asks and to reach with the agent tools if they do, and its findings come with the user's next message; it is added after the messages are marked sent, so it is never queued again as the user's.
<!-- /memoria:section -->

<!-- memoria:section id="settings" files="settings.go dispatch.go compact.go saved.go rewind.go grants.go" -->
## Settings and compaction

`Settings` are what every run sends: provider, model, effort, service tier, adaptive effort (`AdaptiveEffort`: off, 1-step, or 2-steps; `AdaptiveSteps` maps it to the effort levels a follow-up goes down, and `FollowUpEffort` to the effort it goes at, for the TUI's notice), the permission mode, workspace, the host prompt, and how many times a model request is sent before the run fails (`MaxAttempts`, from `request_max_attempts`; a subagent inherits its parent's through `WithRequest`). They carry no timeout, so a run, and a subagent's run, has no wall-clock limit: it goes on until it ends, is interrupted, or hits the disk limit. The permission mode (`approval.Mode`: read only, workspace, auto, or yolo; `Options.Yolo`, from `--yolo`, is the only way to yolo, and `Open` and `SetSettings` refuse it otherwise) goes to the engine as `engine.Options.Mode`; `WithMode` sets it and keeps `Sandbox`, its sandbox mode for display, in step. `SetSettings` stores them and, while a run is live, tries `SetEffort`, `SetModel`, `SetServiceTier`, `SetAdaptiveEffort`, and `SetMode` on it, each field that changed even when one before it failed, so a refused fast mode does not keep a new adaptive effort from the run. `SettingsChanged.Applied` says `live` when all changed fields reached the run, and `next_run` otherwise. The loop also stores the new settings whole in `current`, an atomic pointer, before the run hears of them; each run's `engine.Options.Settings` (`liveSettings`) reads it from any goroutine, so a subagent the run spawns after a change starts with the model, effort, service tier, adaptive effort, and permission mode of that change, all from one change, even while the run is still starting.

The session keeps its provider, model, effort, fast mode, adaptive effort, and permission mode in its sidecar (`Saved`): when it opens and after each change. On resume, `ApplySidecar` puts them in the session's `Info` in place of its newest run's provider, model, and effort, and `internal/app` restores them ahead of the configuration; a flag still wins. A session whose sidecar has no settings (from before uah kept them) resumes with its newest run's, and one whose settings lack adaptive effort takes the configured `adaptive_effort`. A subagent's sidecar keeps its own settings. `resume_agent` restores them, all but the permission mode, which is its parent's ([agents](../agents/README.md#persistence-and-resume)). A session under a tool policy (`Options.Tools`, restricted) records it in the sidecar's `tools` field when it opens; `ApplySidecar` puts it in `Info.Tools`, and `internal/app` narrows a resumed session's policy with it, so resuming without the flags never widens its tools ([tool policy](../../docs/configuration.md#tool-policy)).

A session's grants (`sandbox.Grants`, `grants.go`) are the directories it made writable for the rest of the session ([session grants](../../docs/configuration.md#session-grants)). A root session makes its own when it opens and gives them to each run (`engine.Options.Grants`); a subagent's session shares its parent's (`Options.Grants`). The engine adds a grant from any goroutine. The root session hears it on its loop (`evGrant`), shows `Notice` `writable for this session: <path> (<reason>)`, and appends a worktree grant to its sidecar's `grants` field; a grant the user approved is not kept, since a resume would not restore it. On resume, the root session gets back each grant of its sidecar that `Grants.Valid` still accepts, with a notice for each, drops the others with a warning, and rewrites the field when it dropped one. `Open` makes room in `Events` for these notices, as for the kept queue.

`Compact` marks a compaction as pending; `CompactWith(focus)` adds what the summary should focus on (`/compact <focus>`). While a run is live, the engine compacts before its next model request (`Run.Compact(focus)`); while idle, `engine.Options.Compact` and `CompactFocus` ask the next run to compact first. The pending flag clears when the engine reports a manual `CompactionStarted`. `Clear` (`/clear`) works the same way with `Run.Clear` and `engine.Options.Clear`: the model's next request starts fresh in the same session, and the session drops its [goal](#goals), as Codex's `/clear` starts a thread without one.

`Rewind(messageID)` goes back to before a message, as Codex's backtrack: the message and everything after it leave the agent's context, and the next message continues from there. It needs an idle session with nothing queued or waiting for its hooks (`ErrRewindBusy`) and an engine that implements `engine.Rewinder` (`ErrNoRewind` otherwise, as for a subagent's session). The engine records the cut next to the session file, which keeps every item, and returns `engine.Rewound`, which the session emits, and the texts that went to the agent in the same batch before the message (a notification, a shell command's record, an earlier queued message): the session holds them again, ahead of anything held since, so they go with the next message. A subagent the cut branch started keeps running, and its notification still reaches the agent with a later message. `Load` adds each saved rewind as `engine.Rewound` to the run before it. See the [rewind design](../../docs/design/rewind.md).
<!-- /memoria:section -->

<!-- memoria:section id="approvals" files="approvals.go questions.go" -->
## Approvals

The session gives each run an `approval.Ask` (`askFunc`), which the engine calls when a command or an MCP call needs approval. It chooses, in order:

1. `Options.Ask`, when set. A subagent asks through its parent's session this way.
2. PermissionRequest hooks, when configured. The prompt reaches them as a tool call: `tool_name` is `Bash` with the command, or the `mcp__` name with its arguments. "allow" approves and "deny" (or exit 2) declines. A hook that decides neither passes the prompt on.
3. The user, when the session is interactive (the TUI). The ask hands the prompt to the loop, which emits `ApprovalRequested` and waits for `Resolve` with the same ID. A patch's prompt can carry `GrantRoot`, a directory the user may allow writes to for the session (`approval.ApproveGrant`).
4. Otherwise nil or a decline: no one can answer in `uah exec`.

The agent waits while an approval is open. Several can be open at once, since the engine decides the calls of one model response together; each has its own ID and answer. An interrupt, the end of the run, or `Close` declines every pending approval of the run (`ApprovalResolved` with `decline`), so a waiting run can always stop. An ask whose context ends withdraws its prompt as declined, or as approved when the context ends with `approval.ErrNowAllowed`: a "don't ask again" on another prompt allowed it meanwhile. `engine.Options.AskAnytime` asks outside a run: a subagent's approval shows in its parent's session even while the parent is idle, and stays open until it is answered, its context ends, or the session closes. The rules and the auto-reviewer run before this ask; see [the permission pipeline](../approval/README.md).

The agent's questions (`request_user_input`, `questions.go`) go the same way, without hooks or rules: an interactive session gives each run `engine.Options.AskUser` (`askUserFunc`; nil otherwise, so no one is asked in `uah exec`). The ask hands the questions to the loop, which emits `QuestionsAsked` and waits for `AnswerQuestions` with the same ID and the answers in Codex's encoding, with no time limit. An interrupt, the end of the run, or `Close` cancels them (`QuestionsAnswered` with `Canceled`), as it declines approvals, and an ask whose context ends withdraws them. See the [questions design](../../docs/design/questions.md).
<!-- /memoria:section -->

<!-- memoria:section id="hooks" files="hooks.go dispatch.go runs.go" -->
## Hooks

The session runs the hooks of its events; `internal/hooks` runs the commands. A single worker goroutine runs hook jobs one at a time, in order, and posts each decision back to the loop, so a slow hook never blocks the loop.

| Event | Where the session runs it |
| --- | --- |
| `SessionStart` | In `Open`, with `source` startup or resume. Its context is added to the first message |
| `UserPromptSubmit` | Before each message is dispatched. While hooks run, the message waits in `checking`; later messages wait behind it, so order is kept. A block reports `InputFailed` |
| `PostToolUse` | After each `ToolFinished`; it only observes. A call the tool policy refused never ran, so it fires none: one whose error ends with the engine's `toolpolicy.Refused`, or a built-in tool the session's policy (`Options.Tools`) does not allow. An MCP tool's identity is the engine's to judge, so for one only the refusal counts |
| `Stop` | When a run ends with nothing queued. A block with a reason sends the reason as the next message, at most 5 times in a row. A new message cancels a pending Stop decision. An active [goal](#goals) continues only after the Stop hooks let the run end |
| `SessionEnd` | In `Close`, with at most a second per hook |
| `PermissionRequest` | In the approval ask, above |

PreToolUse and PreCompact hooks run in the engine, on the coordinator's goroutine. Each hook run is reported as `HookRan`, once with outcome `running` as it starts and once with its result. The hook contract and trust are in [internal/hooks](../hooks/README.md).
<!-- /memoria:section -->

<!-- memoria:section id="goals" files="goal.go runs.go loop.go" -->
## Goals

The session keeps Codex's `/goal` (`goal.go`; the [goal package](../goal/README.md) holds the texts and the [design record](../../docs/design/goal.md) the decisions): one goal, in the sidecar's `goal` field, restored when the session opens (`GoalUpdated` with `GoalRestored`, after `SessionOpened`). `Options.Goals` gives the `[goals]` settings; a session with a `Parent` (a subagent's, `/review`'s reviewer) has no goal and refuses every call with `ErrGoalsOff`, as `[features] goals = false` does.

| Method | Does |
| --- | --- |
| `SetGoal(objective)` | A new active goal, with `[goals] max_goal_token_budget` as its budget and `max_continuations` as its cap; refused while an unfinished goal exists. While idle it starts a run at once |
| `EditGoal(objective)` | The new objective, the usage kept; a finished goal becomes active again. A live goal run gets Codex's `objective_updated` message |
| `PauseGoal`, `ResumeGoal` | The status; a resume clears the continuation count and the guards' counts, and starts a run while idle. A complete goal, or one over its token budget, does not resume |
| `ClearGoal` | Drops the goal; `/clear` (`Clear`) drops it too |
| `Goal` | The goal, with the live run's time counted |

Each change the user makes is held as Codex's `user_goal` record and goes to the agent with the next run, as `Inject` holds a message.

**Continuing.** When a run ends (`onEnded`) and the session would report `Idle`, after the queue, the messages waiting for their hooks, and the Stop hooks, `goIdle` asks `continueGoal`: with the goal active, it counts a continuation, emits `GoalContinued`, and starts a run whose message is Codex's continuation (`goal.Continuation`) instead of reporting `Idle`. A user's stop (esc esc, `/stop`) never continues. A run that starts while the goal is active is a goal run (`noteGoalRun`), so a message the user sends during a goal is work on it, and the goal continues after it.

**Accounting** (`onGoalRunEvent`, `endGoalRun`). Each `ModelResponded` of a goal run adds its tokens (`goal.TokenDelta`) and emits `GoalUpdated` (`GoalUsage`); each run's end adds its time. The guards, each with its `Reason`:

| Guard | Status |
| --- | --- |
| The tokens reach the budget | `budget_limited`; the live run gets Codex's budget message |
| A goal is out of continuations when it would continue | `budget_limited`, and a warning notice |
| The run ends with an error, at the disk limit, or past a timeout | `blocked` |
| Three automatic runs in a row made no tool call but the goal tools | `blocked` |
| Three goal runs in a row where a `Bash` command failed and no tool call succeeded | `blocked` |
| `Interrupt` while the goal is active and a run is live or starting | `paused` (`By` user) |

**Steering a live run** (`steerGoal`): the budget message and the edited objective go to the run as developer messages (`core.RoleDeveloper`), which ask for no response and so cancel no model request; each rides the run's next request. They are not marked sent: a run that ends first leaves the message in the history.

**The tools.** Each run gets `engine.Options.Goal` (`goalTool`), which the engine calls for `get_goal`, `create_goal`, and `update_goal`; the call reaches the loop as `cmdGoalTool` and gets Codex's result or refusal (`onGoalTool`). A goal the model creates makes the live run a goal run, with the default budget unless the call names one; `update_goal` sets `complete` from any status and `blocked` or `paused` only from `active`, so a budget limit stays.

Events: `GoalUpdated` (the goal, the `Change`: set, edited, status, usage, or restored, and `By`: user, model, or uah), `GoalContinued` (a run the session started for the goal), and `GoalCleared`.
<!-- /memoria:section -->

<!-- memoria:section id="files" files="sidecar.go lookup.go unused.go remove.go inuse.go tempdir.go history.go agentwatch.go patches.go cachestats.go" -->
## Files and history

The runner's session files and uagent's run records are the source of truth; the [state storage record](../../docs/design/state.md) lists every file and why the index is only a cache.

- **Sidecar.** A new session writes `sessions/<id>.uah.json` with its `source` (`tui`, `run`, or `subagent`), its creation time, and, for a subagent, its `parent`. The first writer wins (`O_EXCL`). Every later change rewrites the file whole (a temporary file, then a rename, `updateSidecar`): its `settings` whenever they change (see [Settings and compaction](#settings-and-compaction)), and the fields that find a session from this file alone (`lookup.go`):

  | Field | Written |
  | --- | --- |
  | `workspace` | The absolute workspace, when the session opens and at the end of each run |
  | `first_prompt` | The first message, cut to 200 characters (`FirstPromptMax`), when a new session's first run starts. It leaves out the injected messages that go before it, and shows pasted images as their placeholders; a session that starts with `/goal` records the objective |
  | `last_activity` | The `RecordedAt` of the session file's last item, when the session opens and at the end of each run |
  | `last_sequence` | The `Sequence` of that item, at the same times. It does not change while a run is live, so it changes only when the session does |
  | `queued` | The texts of the unsent messages, in order, whenever they change (`saveQueue`); absent when none. See [Messages](#messages-queue-steer-interrupt) |
  | `goal` | The session's goal, whenever it changes; absent when none. See [Goals](#goals) |
  | `tools` | The tool policy (`allow`, `deny`), when the session opens under one; absent otherwise. A resume narrows its policy with it. See [Settings and compaction](#settings-and-compaction) |

  A sidecar from before uah kept these fields gets them when the session resumes: `first_prompt` from its runs (`Options.FirstPrompt`, which `internal/app` sets), and the rest from the session file. `ApplySidecar` puts `last_sequence` in `Info.LastSequence`, and `last_activity` in `Info.LastActivity` when it is later than the runs'. `ActiveSince` keeps the sessions active after a time, for `uah sessions --since`. The [session file](../sessionfile/README.md) documents the items. `Interactive` drops `run` and `subagent` sessions from the resume picker, as Codex hides `codex exec` sessions, and `Tree` lists subagents under their parents.
- **A session that never ran.** A session writes its sidecar when it opens, and the runner writes the session file when the first run starts, so a session stopped before its first message has only the sidecar (`unused.go`). `Unused` lists these sessions from their sidecars: no runs, no first prompt, `last_sequence` 0, and the creation time as `Started` and `LastActivity`; `WithUnused` adds them to a list of sessions with runs. `internal/app`'s `FindSession` and `uah sessions` use it, so `uah resume <id>` finds such a session and resumes it under its ID; the resume picker and `--last` list sessions with runs only. A resumed session without a first message records its first one. `Used` reports whether a session has history (an item in its session file, or a run), which `--session-id` checks: an ID without history is taken again, and one with history is refused, so a session file with items is never replaced. The [session file](../sessionfile/README.md#a-session-that-never-ran) states this as part of the format.
- **Removing.** `PlanRemoval` lists what deleting a session removes, touching nothing: its run records in `runs/`, its tool output and its private temporary directory in `sessions/operations/<id>/` (`TempDir` is `<id>/tmp`, the `$TMPDIR` of its commands), its files in `sessions/` (`.session.jsonl`, `.uah.json`, `.compaction.jsonl`, `.rewind.jsonl`, `.websearch.jsonl`, `.effortupdates.json`, `.agent.json`, `.forktmp`, and `.lock` last), and the same for every subagent under it, found by the `parent` in their sidecars. `Removal.Lock` takes each existing session lock as a run does and refuses one a run holds (`harness.ErrSessionBusy`) unless forced; `Remove` deletes the paths. `uah sessions rm` then drops the index rows (`store.Forget`).
- **A /review's reviewer.** Its sidecar has `review: true` (`Options.Review`), so `resume_agent` does not reopen it ([agents](../agents/README.md#code-reviews)). `InUse(sessionsDir, id)` reports whether a run holds a session's lock now, by taking uagent's lock and letting it go; `resume_agent` checks it before it opens anything.
- **Subagent IDs.** `NewSubagentID` is `subagent-<uuid>`; `ShortID` prints `subagent-` and 8 characters of the UUID (8 characters for other sessions), a prefix that resumes the session. Older subagents have plain UUIDs; the sidecar's `parent` identifies them.
- **Watching a subagent.** `WatchAgent(ref)` follows one of the session's subagents, by ID or nickname, through the engine's `Subagents()` when it implements `AgentWatcher`: its earlier runs, its events so far, the ones that follow, a way to message it, and a way to send its queue now. The TUI's agent view uses it; [internal/agents](../agents/README.md#watching-an-agent) implements it.
- **History.** `Sessions` folds run records into one `Info` per session, reading only summaries and the first request. A run's tokens come from its `summary.json` (`SummaryTokens`): uagent v0.7.0's run records carry the summary's metadata but not its stats. `Load` reads every run of a session in start order with its events, with each run's tokens from its summary, which is how the TUI rebuilds a resumed transcript; each compaction saved in the compaction log is added to the run it happened in, in time order, with its stats, so reloaded transcripts, `uah sessions show`, and `uah exec --json` show it. Each saved rewind follows the run before it as `engine.Rewound`, so a reloaded transcript ends where the session went back to. Likewise each applied `apply_patch` call's diff, read from its completed job in the run's events file (`patches.go`), follows the call as `engine.PatchApplied`, each failed command's output and MCP call's result, from the same file, as `engine.ToolOutput`, and each answered `request_user_input` call's questions and answers as `engine.QuestionsAnswered`. Each events file is read once, for the events and these together, through one 1 MB buffer for all of the session's runs. `InDir` matches a session's workspace the way Codex does: absolute, cleaned, and with symlinks resolved.
- **Prompt cache.** `CacheRequests` reads a loaded session's model requests for the [cache accounting](../usage/README.md#session-prompt-cache): each response's tokens and times, the run's model, the effort from the request's `model_attempt` line in the run's `stderr.log` (its `request_effort`, which keys the cache and an effort update leaves at the session's base, else its `effort`; else the run's latest settings), and whether a compaction or rewind came before it. `CacheStats(stateDir, id)` loads the session and attributes its misses; the TUI's `/usage` and `/status` call it, and `uah sessions show` and agentbench's `-cache-sessions` use the same two functions.

Listing and search go through the rebuildable SQLite index in [internal/store](../store/README.md), which falls back to `Sessions` when the index cannot be used.
<!-- /memoria:section -->

<!-- memoria:section id="events" files="events.go approvals.go questions.go shell.go session.go runs.go review.go mcp.go" -->
## Events

`Events()` carries the runner's events (from uagent's `core`), the engine's events, and these session events:

| Event | Means |
| --- | --- |
| `SessionOpened` | Always first: the ID, whether it was resumed, the engine, and the settings |
| `InstructionsLoaded` | The instruction files in the host prompt |
| `InputQueued`, `InputSent`, `InputDelivered`, `InputFailed`, `InputWithdrawn` | A message's way to the runner |
| `SettingsChanged` | New settings, and whether they applied live |
| `ApprovalRequested`, `ApprovalResolved` | An approval waiting for `Resolve`, and its answer |
| `QuestionsAsked`, `QuestionsAnswered` | The agent's questions waiting for `AnswerQuestions`, and the answers, or `Canceled` |
| `HookRan` | A hook's outcome (`running` as it starts), command, and duration |
| `Notice` | Text for the user, with a level |
| `ShellStarted`, `ShellOutput`, `ShellFinished` | A command the user typed (`RunShell`): its start, its output as it arrives, and its result with the record the agent gets |
| `ReviewStarted`, `ReviewActivity`, `ReviewFinished` | A `/review` (`Review`): what it looks at, the reviewer's tool events, its failed commands' output, and its model responses (their tokens), and its findings or how it ended, with the limit that stopped it |
| `MCPStarted` | An interactive session connected its MCP servers: each one's state, after the notices for those that did not start |
| `GoalUpdated`, `GoalContinued`, `GoalCleared` | The [goal](#goals): a change and its cause, a run the session started for it, and its end |
| `Idle` | The session has nothing to do |

`uah exec --json` writes them as JSONL, and the TUI reduces them into its state.

`Options.Stream` asks the engine for the model's text as it arrives (`engine.Options.Stream`): `engine.TextDelta`, `ReasoningDelta`, and `StreamReset` join the stream before the runner's final message. The TUI and `uah exec --json` set it; plain `uah exec` and subagents do not.
<!-- /memoria:section -->

<!-- memoria:section id="tests" files="session_test.go queue_test.go steerqueued_test.go hooks_test.go runner_test.go compact_test.go history_test.go sidecar_test.go saved_test.go shell_test.go rewind_test.go cachestats_test.go grants_test.go" -->
## Tests

`session_test.go` and `hooks_test.go` drive a session with a scripted fake engine, so each state transition can be held open: queueing while running, steering while starting, interrupts that keep the queue, a second interrupt that kills a run slow to stop, messages the run never read, withdrawal, live settings (a failed one not stopping the rest), failures, and every hook event. `steerqueued_test.go` pins `SteerQueued`: the queue reaches a live run in order with its IDs and image tags, waits for a run that is starting, restarts a run without live input, starts a run from a queue an interrupt kept, and turns a message waiting for its hooks into a steer. `TestSession_RunHasNoDeadline` pins that the engine gets no timeout and a context with no deadline, and `internal/app/deadline_test.go` that a run on the embedded engine reaches uagent's harness with none. `saved_test.go` pins the settings kept in the sidecar and a live mode change, `grants_test.go` the grants: given to each run, kept in the sidecar as they are added, restored on resume when they still hold and dropped when not, and shared without a sidecar entry by a session given its parent's, and `queue_test.go` the queue kept there: saved as it changes, a withdrawn or sent message removed, kept through a close, queued again in order and unsent on resume, and never in a new session; a kept queue of 5,000 messages opens and takes the next message before anything reads `Events`; `internal/tui/bubble/keepqueue_test.go` quits the TUI with a queued message, resumes, sees `↳ queued:`, and sends it with enter on the empty composer (fakellm). `runner_test.go` runs a session on uagent's fake runner through `harnesstest.RunnerEngine`, which takes nothing live: a steer that restarts the run, and what the session does for any engine (the host prompt, the session-level hooks, and the saved settings and when they apply). Resuming with the saved settings is tested end to end in `internal/app/resume_test.go`, and the sidecar's lookup fields in `internal/app/lookup_test.go`: on the embedded engine, `last_sequence` stays while a turn runs and follows the session file when it ends, and an older sidecar gets the fields on resume. `rewind_test.go` pins `Rewind` on the fake engine: refused without the engine or while a run is live, and the held texts going first with the next message; `internal/engine/embedded/rewind_test.go` pins the cut on the real engine. `shell_test.go` pins `RunShell`: no run starts, the record goes first with the next message (on the fake engine and on the fake runner), a command during a live run is not sent into it, and an interrupt stops it; `internal/app/usershell_test.go` runs it on the embedded engine with `testing/fakellm` (the next request carries Codex's format) and with `user_shell_sandbox` (a `forbid` rule refuses, the mode picks the sandbox), and `internal/usershell` tests a write outside the workspace failing in workspace mode. The goal loop runs end to end on the embedded engine with `testing/fakellm` in `internal/app/goal_test.go`: a goal continued until the model calls `update_goal` complete, each request extending the one before; the token budget, the continuation cap, three runs without a tool call, and a failed run stopping it; an interrupt pausing it, a restart keeping it, `/goal resume` continuing it and `/clear` dropping it; a goal the model creates; an edit during a run; and `[features] goals = false`. `cachestats_test.go` reads cache requests from run events and a `stderr.log`: the effort of each request's successful turn attempt, the settings' effort without one, a failed response left out, and a compaction marking the next request; with effort updates, the request's effort as the key, so a switch by update is no effort miss.
<!-- /memoria:section -->
