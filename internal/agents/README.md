<!-- memoria:section id="overview" files="manager.go tools.go" -->
# Subagents

<!-- memoria:export id="summary" -->
Subagents are child sessions that a session's agent starts, messages, waits for, and closes through Codex's v1 multi-agent tools. `internal/agents` implements them behind the `engine.Subagents` seam: the embedded engine offers the tools and runs their calls in the background, and the package owns the tools, the children's lifecycle, approvals through the parent, limits, hooks, and resume.
<!-- /memoria:export -->

This README describes how the package works for someone changing it. The root README describes the feature for users, and [the design record](../../docs/design/subagents.md) keeps the research, the decisions, and the validation findings.

1. [The seam](#the-seam)
2. [Parity with the root session](#parity-with-the-root-session)
3. [A child's lifecycle](#a-childs-lifecycle)
4. [The tools](#the-tools)
5. [Approvals](#approvals)
6. [Events and hooks](#events-and-hooks)
7. [Forking](#forking)
8. [Watching an agent](#watching-an-agent)
9. [Code reviews](#code-reviews)
10. [Persistence and resume](#persistence-and-resume)
11. [Limits](#limits)
12. [Roles](#roles)
13. [Markdown agents](#markdown-agents)
14. [Extending](#extending)
<!-- /memoria:section -->

<!-- memoria:section id="seam" files="manager.go tools.go" -->
## The seam

`engine.Subagents` (in `internal/engine/subagents.go`) is the only contract between the engine and this package. `Manager` implements it:

- `Attach` runs at the start of each of a session's runs on the embedded engine. It records the run as a parent (its request, how to ask its user, and how to add events to its stream) and returns the tools to offer, or none when the session is too deep to spawn.
- `ToolNames` lists every name a call may use, offered or not. The engine resolves these names in every run, so a session with past calls resumes where the tools are not offered. The package-level `ToolNames` gives the same list without a manager, for the compaction evaluation, which opens recorded sessions with no subagents.
- `Call` runs one tool call (`engine.AgentCall`: the parent's ID, the model's call ID, the tool, and its JSON arguments) and returns the JSON result for the model. The engine runs each call as a remote job (`uah.agent` v1) on its own goroutine, so a long `wait_agent` never holds up the parent's coordinator. Its result still waits with the turn's other results, as every tool's does ([the wake policy](../engine/README.md#the-wake-policy)): the model wakes when the turn's calls have finished, or after 5 minutes with the wait still running. The call's context ends when the coordinator cancels the call or the run stops.
- `Interrupt` runs when the user interrupts the parent's run.

The engine knows no tool name, schema, or result, and the session knows children only as sessions with `source: "subagent"` and a `parent` in their sidecar. An engine that also implements `engine.Forker` (the embedded engine) copies a parent's history into a child for `fork_context` and gives every child its tree's prompt cache key (see [Forking](#forking)). One that implements `engine.Scoper` narrows a child's tools and pre-approves its actions as its role says (see [Markdown agents](#markdown-agents)). `app.Setup` builds the manager for the embedded engine, even when subagents are off, and binds it with `Bind` to that engine and to the session options it returns, the ones the root session opens with. `Close` closes every child; the engine calls it when a session on it closes, and the manager stays usable for the engine's next session.
<!-- /memoria:section -->

<!-- memoria:section id="parity" files="manager.go ops.go" -->
## Parity with the root session

A subagent is the root agent in every way except what makes it a child. `childOptions` starts from the options `app.Setup` returned for the root session and opens the child with `session.Open` on the same engine, so the child gets the same instructions and skills, sandbox, rules, approvals policy and auto-review, hooks, MCP servers, compaction, context meter, tool output limits, index, and sidecar. Its settings are the parent's when it spawns: `Settings.WithRequest` inverts the request the parent's run started with, for the provider, workspace, and system prompt, and the model, effort, service tier, adaptive effort, and permission mode come from `AgentParent.Settings`, the parent session's settings now (`engine.LiveSettings`). The session stores them whole whenever they change, before the live run hears of the change, so a `/model`, `/effort`, `/fast`, `/adaptive`, or mode change made during the run reaches every child spawned after it, all fields from one change; a stricter mode chosen during the run holds for new children. The child's sidecar keeps the child's own settings. `TestAgents_SpawnAfterALiveChange` changes all five while the parent's model request waits, and the child spawned after starts with them.

The only differences:

1. its session ID, `subagent-<uuid>`, and its sidecar's `source: "subagent"` and `parent`;
2. the spawn tools and `request_user_input` (the agent's questions to the user), never offered to a child, except to a forked child, which keeps its parent's tools and whose spawns and questions are refused (the engine asks `Manager.Forked`); a child asks its parent in its final answer instead;
3. approvals, asked through the parent session;
4. the model, effort, service tier, and instructions a role, the spawn call, or `[agents]` defaults set, and a role's tools and pre-approvals;
5. Codex's note that its final answer goes back to the parent agent (`instructions.SubagentNote`, from Codex's subagent role text), never in a fork. It is not in the system prompt: `firstNote` adds it to the first message, after the task, once, so the child's system prompt stays the parent's, byte for byte, which keeps the provider's prompt cache. It is not a message of its own, because the runner asks the model as soon as the first of two messages arrives (it waits only 1 ms for more), and that request would be canceled. A child spawned while the note was still in the system prompt has it in none of its messages: when `resume_agent` opens it, `hasNote` finds that in its session file and `noteDue` adds the note to its next message, once, never to a fork's. A role's instructions still follow the system prompt;
6. a hook runner of its own with the same hooks, so hook results stay with the child's session, which fires the subagent hooks only (see [Events and hooks](#events-and-hooks));
7. its engine handle, which does not close the shared engine when the child closes;
8. its prompt cache key, the root session's ID, as Codex keys every agent of a tree;
9. no streaming: `Stream` is off, since neither the parent nor the agent view shows a child's text as it arrives;
10. the session's grants, shared with the parent (`AgentParent.Grants`), not made anew: a directory either makes writable is writable for both, and only the root session keeps them in its sidecar ([session grants](../../docs/configuration.md#session-grants)). `TestAgents_ChildSharesTheParentsGrants` pins it: the parent's patch grants a worktree, and the child's sandboxed command writes there.

`TestSetup_SubagentParity` (in `internal/app`), `TestParity_ChildOptions`, and `TestParity_ChildSharesTheParentsSystemPrompt` (the same system prompt in the child's request, the note with its task) pin this: a child's model request has the root's system prompt, model, effort, service tier, and tools with the same schemas, less the spawn tools (and `request_user_input`, which the TUI's root has), and its options equal the root's but for the differences above. A capability added to the root session reaches children without a change here.
<!-- /memoria:section -->

<!-- memoria:section id="lifecycle" files="child.go ops.go status.go cause.go" -->
## A child's lifecycle

A child is a `session.Session` on the parent's engine. The engine is wrapped so that closing a child leaves the shared MCP servers running. The manager's lock guards every child's fields.

1. **Start.** `spawn` checks the depth and, on the openai-codex provider, the model (see [The tools](#the-tools)). `start` checks the limit, registers the child with the ID `subagent-<uuid>`, sets its prompt cache key, and opens its session with `childOptions` (see [Parity](#parity-with-the-root-session)). The spawn call, the role, and the configured defaults override the model and effort in that order (a fork takes only the spawn call's, see [Forking](#forking)), a role's `service_tier` sets fast mode, and a role's `developer_instructions` follow the host prompt. With `fork_context`, the engine copies the parent's history into the child's session now (see [Forking](#forking)). `SubagentStart` hooks run next, and then the first message goes out. A goroutine, `watch`, then follows the session's events until it closes.
2. **Running.** `submit` sends a message and marks the child `running`. The session queues the message while a run is live, so a child reads a second message after its current run.
3. **Finished.** When the session reports `Idle` and has queued every message sent (tracked by message ID, so an `Idle` from before a message, or a message the session added itself, such as a Stop hook's, does not count), the child is `completed` with the answers of every run since it was last idle, `interrupted`, or `errored`. A message the parent sends while the child works starts another run after the current one, and the status keeps both answers (`answers`): with only the last, a parent got the answer to its follow-up and asked again for the task's. An errored child's message is the cause in one line (`cause.go`): the provider's message from its JSON error body when the run's `RunnerError` has one, such as `The 'gpt-luna-6' model is not supported when using Codex with a ChatGPT account.`, else the error itself. With `SubagentStop` hooks, a completed child first runs them (see [Events and hooks](#events-and-hooks)).
4. **Closed.** `close_agent`, closing the parent, or `Close` closes the child's session and its open descendants. The watcher then reports `shutdown`.

A status encodes as Codex's `AgentStatus`: a string (`pending_init`, `running`, `interrupted`, `shutdown`, `not_found`) or `{"completed": message}` and `{"errored": message}`. Every status but `pending_init` and `running` is final. Unlike in Codex, `interrupted` is final, because only the parent's `send_input` starts such a child again.

Interrupting the parent's run calls `Interrupt`, which stops the live runs of the parent's children and their descendants. They stay open as `interrupted`. An interrupt that comes while a `spawn_agent` call is still running ends that call instead: the call fails, and the child it started is closed (`shutdown`), because the parent never received the child's ID. The child already shows as `running` in that time. An interrupt that comes while a message is on its way to an idle child, from `send_input` or the agent view, stops the run that message starts once the send returns (`settle`), since the child's session had no run to stop yet.
<!-- /memoria:section -->

<!-- memoria:section id="tools" files="tools.go prompt.go ops.go status.go" -->
## The tools

The tools are Codex's v1 set (rust-v0.156.1), with its parameters, results, and error messages. `prompt.go` holds the descriptions and schemas, adapted from Codex under the Apache License 2.0.

| Tool | Arguments | Result |
| --- | --- | --- |
| `spawn_agent` | `message`, `agent_type?`, `fork_context?`, `model?`, `reasoning_effort?` | `{agent_id, nickname}` at once |
| `send_input` | `target`, `message`, `interrupt?` | `{submission_id}` |
| `wait_agent` | `targets`, `timeout_ms?` (default and most 4.5 min, at least 1 min) | `{status: {id: status}, timed_out, note?}` |
| `close_agent` | `target` | `{previous_status}` |
| `resume_agent` | `id` | `{status}` |

- **spawn_agent** starts a child with its first message and returns while the child works. With `fork_context`, the child starts from a copy of the parent's history (see [Forking](#forking)); a fork keeps the parent's agent type, so `agent_type` with `fork_context` is refused with Codex's message. At the depth limit it returns Codex's `Agent depth limit reached. Solve the task yourself.` A `model` (or `default_subagent_model`) the provider does not offer is refused with Codex's `Unknown model ... Available models: ...` and a did-you-mean suggestion. `Config.Validate` does the check (`models.go`); `app.Setup` sets it to [internal/models](../models/README.md)'s `Validate`, which uses the provider's live list, so it works on every provider that lists its models.
- **send_input** queues the message. With `interrupt`, it stops the child's live run first and hands the child the message at once.
- **wait_agent** returns as soon as any listed child is in a final status, with every listed child that is final by then. An unknown ID is final as `not_found`. Without `timeout_ms` it waits 4.5 minutes, the most it allows: just under the engine's 5-minute wake hold, which would otherwise wake the parent only to say that its wait still runs. The least is a minute (Codex: 30 s by default, 10 s to 1 h). A wait that times out adds a `note`: the agents are still working, nothing is wrong, wait again. The final messages share a budget of 36,000 characters, keeping each one's head and tail, so the result stays valid JSON under the runner's 40,000-character cap on a remote job's result.
- **close_agent** closes the child and its descendants and returns the status it had before.
- **resume_agent** reopens a closed child (see [Persistence and resume](#persistence-and-resume)).

The descriptions differ from Codex's where the owner's sessions showed parents micromanaging their agents ([the design record](../../docs/design/subagents.md#round-4-leaving-agents-alone)): the guidance after delegating, `send_input`, and `wait_agent` say to do the parent's own part, then wait; that a finished agent's answer comes on its own; and to message a running agent only with new information from the user, an answer to its question, or to stop a clear failure, never for status or to hurry it. Codex's "Call wait_agent very sparingly", "do meaningful non-overlapping work immediately", and "Use interrupt=true to redirect work immediately" are gone.

`items` (structured input) is left out: the runner's inbox takes plain text.
<!-- /memoria:section -->

<!-- memoria:section id="approvals" files="ask.go" -->
## Approvals

A child's session has no user of its own. Its `session.Options.Ask` asks through the parent session's `engine.Options.AskAnytime`, with `agent <nickname>:` before the justification. The child's own run applies the auto-reviewer first when it is on (auto mode, or `approvals_reviewer = "auto_review"`), on the child's own transcript; what is left to the user reaches the parent session and its PermissionRequest hooks.

Such a prompt stays open after the parent's run ends, and a child can ask while the parent is idle. It ends when the user answers, when the child is interrupted or closed (each child has a context for its prompts that these cancel), or when the parent session closes. With no one to ask, as in `uah run`, the child is declined with a reason. A role's `approve` list answers first, before the child's auto-reviewer (see [Markdown agents](#markdown-agents)).
<!-- /memoria:section -->

<!-- memoria:section id="events" files="child.go stop.go" -->
## Events and hooks

The manager adds two engine events to the parent session's stream through the parent's `Emit`, which is `engine.Options.Notify`, so they arrive after the parent's run ended too. They go through the parent's outbox (`outbox.go`): deliveries run in order on a short-lived goroutine, so a busy parent holds up only its own updates, never a child's event loop:

- `engine.AgentUpdated`: the child's state, whenever it changes. One lock covers reading the state and sending it, so updates arrive in order. Each update is the whole picture: `ID`, `Nickname`, `Role`, `State`, `Message` (why an errored child failed), `Started`, the spawn call's `CallID` and message (`Task`), the child's `Model` and `Effort`, and `Forked`. The TUI keeps the latest one in its `KindAgent` item (`Item.Agent`), so a view can map an agent ID to its nickname and a spawn call to its agent.
- `engine.AgentActivity`: each of the child's tool events (`core.ToolCalled`, `core.ToolStarted`, `core.ToolFinished`), for the TUI's detailed view.

Waiters sleep on a channel that is closed and replaced at every change. A spawn that fails before its first message removes the child's sidecar and agent record (`discard`), so no child that never ran is offered for resume. Depth and the tree root come from one walk (`lineage`), which remembers each parent it read from a sidecar.

**Notifications.** When a child reaches a final status (completed, errored, or interrupted), the parent's agent is told with Codex's v1 message, a user-role `<subagent_notification>` holding `{"agent_path": <child ID>, "status": <status>}` (Codex `core/src/agent/control.rs`, `SubagentNotification`). It goes through `AgentParent.Inject`, which is `Session.Inject`, as Codex's `inject_no_new_turn`: into the parent's live run as a developer message, which rides the run's next model request and cancels none (a user message would cancel the request under way), so a parent that works while its children run learns at once that one finished; with no run, it waits and goes out with the parent's next message, starting no run. Until v1.9.5 it always waited for the next message, so a parent heard of a child's end only by polling `wait_agent`, and its notes arrived minutes late, together, after it had asked the children for their results. Once per message the child was sent (`completionNote`). Two cases send none, because the parent learns the status anyway: a child the parent closed itself, and a child a pending `wait_agent` covers (`child.waiters`); Codex sends it in both, so a waiting parent there sees the status twice. A third takes it back: `Inject` returns a withdraw, kept on the child (`hold`), and a `wait_agent` that returns the child's final status uses it (`unhold`), so a note still held for the parent's next message does not repeat what the wait returned. A note that went into the live run cannot be taken back, so `Inject` also says whether it did (`live`), the child keeps that status's message count (`told`), and a wait that returns the same status gives `(its final answer is in the <subagent_notification> you already got)` in place of the answer (`reported`): in the A/B runs, half the waits returned a status their parent had just been told. `TestAgents_NotifyTheParentWhenAChildEnds` pins that the note goes when no wait returns the status, `TestAgents_NotifyTheLiveRun` that it reaches a working parent's next request in the same run, and `TestAgents_AWaitAfterTheNotificationKeepsTheAnswerOnce` that a wait after it does not repeat the answer.

Hooks come from `Config.Hooks`, the session's runner:

- **SubagentStop** runs when a child completes, with Claude Code's payload: the parent's `session_id` and `transcript_path`, and the child's `agent_id`, `agent_type` (its role, or `default`), `agent_transcript_path`, and `last_assistant_message`. A block with a reason sends the reason to the child as its next message, at most 5 times in a row (`stop_hook_active` is true after the first). The child stays `running` while the hooks run; a message from the parent meanwhile makes their decision moot.
- **SubagentStart** runs when `spawn_agent` has opened a child, before its first message, with the same payload less `last_assistant_message`. It only observes. The spawn waits for it, so it comes before any hook of the child's own. A resumed child does not run it.
- **Session hooks** (SessionStart, SessionEnd, UserPromptSubmit, and Stop, `hooks.RootOnly`) never run for a child, as in Claude Code. `Bind` gives the runner the manager's parent lookup (`hooks.Runner.SetParents`, the same walk `lineage` uses), and the runner skips these events for any session with a parent.
- **Tool hooks and PreCompact** run for a child as for the root session: PreToolUse and PreCompact on the engine, PostToolUse in the child's session, which has its own runner (`hooks.Runner.Clone`), and PermissionRequest in the parent session, where the child's approvals go. Their payloads keep the child's `session_id` and add `agent_id` (the same ID) and `parent_session_id`, so a hook can tell a child's call from the root's.
<!-- /memoria:section -->

<!-- memoria:section id="fork" files="ops.go manager.go record.go" -->
## Forking

`spawn_agent` with `fork_context: true` starts the child from a copy of the parent's history, as Codex's v1 tool does. The purpose is the provider's prompt cache: work that needs the current context starts without exploring again, and its first request shares the longest possible prefix with the parent's.

1. **The copy.** `engine.Forker.Fork(parent, child, callID)` finds the parent's model response that made the spawn call, and copies every item of the parent's session before that turn into the child's new session, through the runner's session store (`AppendInput`, `AppendTurn`, `AppendModelResponse`, `AppendToolCallStatus`). These items are the input of the parent's request that made the call. The runner's own `Store.Fork` (v0.1.1) is not used: it drops the operation snapshots of inherited tool calls, so their results would be missing.
2. **History only.** The child never runs the parent's work again. The runner's store resumes every operation whose stored state has not ended, and that state is the one the call's first status recorded (`ready`), kept current in the parent's file by operation records the copy leaves out; so each copied operation also gets an operation record with the last status the parent's items show (`completed`, `failed`), without its state, which the item already has. A copied tool call whose operation had not ended is recorded as canceled. Before this, every command and patch of the parent ran again in the workspace when a fork started, and the parent's saved command outputs were emptied.
3. **Compaction.** The parent's compactions recorded before that response are copied to `sessions/<child>.compaction.jsonl`, so the child's request is compacted as the parent's was.
4. **The first run.** The engine puts the child's first messages and its effort in the store before the coordinator restores the session, because restoring counts the copied inputs as undelivered and asks the model at once. The inbox then drops the messages as already seen.
5. **Its own `$TMPDIR`.** Each session has its own scratch directory (`session.TempDir`), and the fork's sandbox grants the fork's, not the parent's. The copied history holds the parent's prepared context, which names the parent's directory as this session's. `Fork` notes a fork whose copied developer messages name it (`namesPath`), and the fork's first run starts with a developer message of its own (`forkNote`, `prepare.go`): its `$TMPDIR`, with the absolute path, is its private scratch directory, and the parent's path above is the parent's. The message goes after the copied items, so the request keeps the parent's prefix and its cache; leaving the parent's prepared context out of the copy, or rewriting it, would break the prefix at its first item. `Fork` writes the parent's path to `sessions/<fork>.forktmp`, and the engine removes the file once the store has recorded the message with the run's, so a first run that fails before that, a close, and a `resume_agent` in another process still get the note, and later runs do not repeat it; deleting the session deletes the file. A fork whose parent had no sandbox facts, or an engine without a sandbox, gets none.
6. **The same prefix.** The child's system prompt is the parent's, byte for byte (a fork has no role instructions of its own and gets no subagent note), and its tools are the parent's in the same order: a forked child is offered the spawn tools even at the depth limit, and its spawn and resume calls are refused there, as Codex refuses them. So that it does not spend its first turn on a refused spawn, its first message, after the copied items, ends with `You are a forked agent at the agent depth limit: spawn_agent and resume_agent fail here. Solve the task yourself.` (`firstNote`); it goes after the shared prefix, so the cache is kept. A fork reuses the cache only with the parent's model and effort, and the `fork_context` description says so: it tells the model to omit `model` and `reasoning_effort`, and a fork without them gets the parent's, never the role's or `default_subagent_model` and `default_subagent_reasoning_effort` (`childOptions`; only a model the call names is checked against the provider's list). Codex v0.156.1 applies the configured defaults to forks too (`apply_requested_spawn_agent_model_overrides`), which loses the cache it forks for; uah keeps the promise its tool description makes. Every child uses the root session's ID as its prompt cache key (the `prompt_cache_key` field, and the `session-id` header on openai-codex), as Codex keys all agents of a tree by the root session.

`TestFork_DoesNotRunTheParentsWorkAgain` forks a parent that ran a command, applied a patch, and has a command still running, and checks that each ran once. `TestFork_ChildStartsWithTheParentsRequest` and `TestFork_KeepsTheParentsCompaction` check with fakellm that the child's first request has the parent's system prompt, tools, and cache key, and starts with every input item of the parent's request, tool calls and results included, followed by the child's message with the depth note. `TestFork_KeepsTheQuestionTool` offers the root `request_user_input`: the fork gets the same tools and its question is refused with Codex's root-only message, and a plain child is not offered it. `TestFork_InheritsTheParentsSettings` configures subagent defaults: a plain child gets them and a fork the parent's model and effort. `TestFork_HasItsOwnTempDir` forks a read-only parent: the fork's first request keeps the parent's items, then the developer message names the fork's `$TMPDIR`, and the fork writes there in the sandbox.
<!-- /memoria:section -->

<!-- memoria:section id="watch" files="watch.go" -->
## Watching an agent

`Manager` implements `session.AgentWatcher`, and `Session.WatchAgent(ref)` reaches it through the embedded engine's `Subagents()`. A watch finds the parent's child by ID or nickname and returns:

- `History`: the child's runs from before this process, from its run records;
- `Events`: its session's events since it opened in this process, which the watcher logs (the newest 20,000, trimmed in steps of a quarter; dropped when the child closes, whose runs stay on disk);
- `Next`: the events that follow, on a channel of 4,096. A view that falls a whole queue behind is closed rather than holding up the child; the TUI opens it again;
- `Send`: a message to the child through `submit`, as `send_input` sends it, so the child's status and `wait_agent` see it; its `session.When` says when it reaches the child's live run (`Session.Send`: now for ctrl+enter, after the next tool call for enter, after the run for tab);
- `SteerQueued`: the child's queued messages now, in order (`steerQueued`, over `Session.SteerQueued`), for enter or ctrl+enter on the view's empty composer. A child a user interrupt left with its queue is `running` again;
- `Interrupt`: stops the child's current work and its own children's (`interruptTree`, which the parent's `Interrupt` also uses for each child); the child stays open;
- `Stop`: the end of the watch.

The TUI's `/agents <name>` view is built on it (see the TUI README).
<!-- /memoria:section -->

<!-- memoria:section id="review" files="review.go reviewrun.go reviewagent.go" -->
## Code reviews

`Manager` also implements `session.Reviewer`, which runs the TUI's `/review` and `uah review` as Codex runs its review thread (`review.go`; the [review design](../../docs/design/review.md) has the research). `ReviewSettings(parent)` gives the reviewer's settings, which the session reports with `ReviewStarted` and passes back in `ReviewRequest.Settings`: the parent's in read only mode, with Codex's review rubric in place of the host prompt, followed by the parent's `<environment_context>` (no default prompt, subagent note, or AGENTS.md), and `Config.ReviewModel` (`review_model`), else the parent's model; the effort and adaptive effort are the parent's. `Review(ctx, req)` opens a fresh session on the parent's engine with the process's options and those settings, as a child does, but:

- its scope offers `Bash` and `ViewImage` only and sets `engine.Scope.NeverAsk`, so no `apply_patch`, no MCP tools, no hosted web search, and every action that would ask for approval is declined before the auto-reviewer, as Codex's `approval_policy = never`; with `CommandTimeout` from `Config.ReviewLimits.Command` (`review_command_timeout`), a command that runs longer is stopped;
- it has no stream, no shell, no one to ask, and no first prompt of the process's (a resumed root session's would land in its sidecar); its hooks are its own runner's, and its parent is recorded in `parentIDs`, so it is never offered the agent tools and the root-only hooks skip it;
- its sidecar has `"review": true` (`session.Options.Review`), and the manager keeps its ID in `reviews`.

It sends the prompt and follows the session (`reviewrun.go`): the reviewer's tool events, its failed commands' output (`engine.ToolOutput`), and its model responses go to `ReviewRequest.Activity`. The review ends at the reviewer's first answer (`engine.Answered`: a response with a message and no tool call); when a call it started still runs, its run is interrupted, which cancels the call's operation and kills its process group, so no heartbeat or late result wakes it again. `Review` returns that answer with the tokens the runs used (`session.ReviewAnswer`) once the session goes idle. When ctx ends first, it interrupts the run and waits for it. `Config.ReviewLimits` bound the review: when the time (`review_time_limit`) or the responses' tokens (`review_token_limit`), both off by default, run out before an answer, the run is interrupted, the scope becomes `NoTools`, and one last message asks for the answer with what the reviewer has; that turn lasts at most 3 minutes (or the time limit, when shorter), and `ReviewAnswer.Limit` says which limit it was. It closes the session either way.

While it runs, the reviewer is one of the parent's agents (`reviewagent.go`, the owner's decision; Codex's review thread is out of the main agent's reach): `addReviewer` registers it under the session ID `session.ReviewRequest.ReviewerID` with role `review`, the nickname Reviewer, and a task that says it was started by the user and is to be left alone unless the user asks, and `AgentUpdated` lists it. Its events reach the child through `awaitReview` (`reviewAgent.observe`), not `watch`. `send_input` goes into its live run (`SendNow`), since the review ends with that run and a message after it would never arrive; a send to it is reserved under the lock before its interrupt, and an idle session with a message on its way is not the review's end (`reviewAgent.end`; when the reservation ends, `released` wakes the review to decide again once the events already sent are read), so the reviewer's answer to it is the review's; once the review decides to end, `send_input` says the reviewer is closed. Only `endReviewer` gives it a final status: an interrupted run or a limit's stop leaves it running. `wait_agent` returns the review's answer (`endReviewer` gives the child its final status and closes it), and `close_agent` stops the review (`stopReviewer`), which ends as interrupted (`ReviewAnswer.Interrupted`). It takes no place in the limit, sends no `<subagent_notification>` (the review's hand-over does), runs no SubagentStop hook, and `Interrupt(parent)` leaves it to the session, which stops its review itself. `send_input` and `close_agent` say in their descriptions that a review's agent is the user's. `resume_agent` refuses a reviewer whose review is not running here (`errReviewer`: its sidecar says `review`, or this process started it). Its sidecar names the parent, so `uah sessions` lists it under the parent. `TestReview_ReadOnlySubagent` checks the request (the rubric and the environment context, the model, the two tools), a write that fails in the read-only sandbox, a declined escalation, the reviewer's tool events, failed command output, and model responses reaching `ReviewActivity`, and the findings reaching the parent's next request; `TestReview_NoWebSearch` that the reviewer's requests have no web search tool and no recorded search while the parent's have both; `TestReview_Usage` that `ReviewStarted` names the reviewer's model and effort and that the review's tokens reach `ReviewFinished`, `uah sessions`, and the loaded run; `TestReview_InstructionFilesLeftOut` that the reviewer's prepared context names the instruction files as left out of its system prompt, while the parent's says they are in it. `reviewlimits_test.go` covers ledger item 139: `TestReview_EndsAtTheFirstAnswer` (an answer while a command runs: the command is killed and no third request goes out), `TestReview_TimeLimit`, `TestReview_TimeLimitWithoutAnswer`, and `TestReview_TokenLimit` (one last request without tools, then the end), `TestReview_CommandTimeout`, `TestReview_StopsItsOwnCommand` and `TestReview_CannotStopOthers` (in the read-only sandbox), `TestReview_TheReviewerIsAnAgent` (`send_input` reaches the reviewer's run, its answer is the review's, `wait_agent` returns it), `TestReview_CloseAgentStopsTheReview`, `TestReview_ResumeAFinishedReviewer`, `TestAgents_ResumeInUse`, and `TestReview_MainAgentKnows`.
<!-- /memoria:section -->

<!-- memoria:section id="resume" files="record.go ops.go" -->
## Persistence and resume

A child is an ordinary session: its runner files, run records, and sidecar (`source: "subagent"`, `parent`) persist, `uah sessions` lists it under its parent, and the resume picker hides it. Its ID is `subagent-<uuid>`: the runner's session store, uagent's session lock, and the run records accept ASCII letters, digits, and dashes. `uah sessions` prints `subagent-` and the first 8 characters of the UUID, a prefix that `uah resume` and `uah sessions show` find. Children from before the prefix have plain UUIDs; their sidecar's `parent` identifies them, as it identifies every child. The manager also writes `sessions/<id>.agent.json` with the nickname, the role, the spawn call's ID, message, model, and effort, and whether the child was forked.

The manager knows only the children of the current process. `resume_agent(id)` reopens a closed child, or a child of an earlier process, when its sidecar names the calling session as its parent, is not a `/review`'s reviewer (one that runs is already an agent; one that ended is not resumed), and no run holds the session (`session.InUse`, uagent's session lock): a busy session is refused before anything is registered or opened, since its run would fail to start and a message sent to it would never arrive. The child keeps its history, nickname, and role, and reports `pending_init` until it gets a message. Its settings are the ones its sidecar saved (`restore`): the model (when it is on the parent's provider), effort, fast mode (when the engine serves it), and adaptive effort it last used, as a resumed root session gets its own, not the parent's settings now and not the role's or the defaults again. Its permission mode is the parent's now, as Codex gives a resumed agent its parent turn's approval policy and sandbox: the parent's user decides what a child may do, and a child's sidecar would otherwise bring back a mode looser than the parent's. A child whose sidecar has no settings (from before uah saved them) starts as a new child does. `TestAgents_ResumeKeepsTheChildsSettings` changes the parent's model, effort, fast mode, and mode before resuming a plain child and a fork. `send_input` and `close_agent` to such a child say to resume it first; `wait_agent` reports it as `not_found`, as Codex does. A call that had started before a restart fails instead of running twice.
<!-- /memoria:section -->

<!-- memoria:section id="limits" files="manager.go ops.go" -->
## Limits

- **Concurrency.** `MaxThreads` (`max_concurrent_threads_per_session`, default 4) counts the open children in the whole tree under the root session, checked under the lock so parallel spawns keep it. Finished children count until closed, as in Codex.
- **Depth.** Subagents never start subagents: `MaxDepth` is at most 1, a rule rather than a setting. `New` clamps a higher value, and `app.Setup` does too, with a notice, for `[agents] max_depth` above 1. It is compared with a session's depth, which comes from the live children and then the sidecars, so a resumed child keeps its depth. A child is not offered the tools, as in Codex at its depth limit, except a forked child, which keeps its parent's tools for the prompt cache; a spawn or resume from a child is refused with Codex's message, `Agent depth limit reached. Solve the task yourself.` `MaxDepth` 0 offers no tools; `app.Setup` uses it when `[agents] enabled = false`, and past calls still get an answer. `TestDepth_ChildrenNeverSpawn` pins both with `MaxDepth` 5.
<!-- /memoria:section -->

<!-- memoria:section id="roles" files="roles.go models.go markdown.go" -->
## Roles

Agent types are Codex role files and Markdown agent files, loaded by `LoadRoles` from `~/.uah/agents` and a trusted workspace's `.uah/agents` (recursive `*.toml` and `*.md`). A later directory replaces a role of the same name; within one directory, a Markdown file replaces a TOML file of the same name, with a warning. The subset read from TOML is `name`, `description`, `nickname_candidates`, `model`, `model_reasoning_effort`, `service_tier`, `developer_instructions`, and uah's `tools` and `approve`, with Codex's validation. Other keys produce a warning, and a malformed file is skipped with one. The `spawn_agent` description lists the roles of both formats alike.

`service_tier` is Codex's key: `"priority"` (or its legacy name `"fast"`) runs the role's agents with priority processing when the provider offers it, `"default"` runs them without it, and an absent key follows the parent. `"flex"`, which no provider of uah's serves, is ignored with a warning. Codex rust-v0.156.1 reads the key into the role's config layer but then sets every child's tier to the root's (`apply_spawn_agent_service_tier`); uah applies the role's tier, so a role can turn fast mode on for its agents alone.
<!-- /memoria:section -->

<!-- memoria:section id="markdown" files="markdown.go toolnames.go manager.go" -->
## Markdown agents

A Markdown agent file is Claude Code's subagent format: YAML front matter between `---` lines, then the agent's instructions (`markdown.go`, parsed with `go.yaml.in/yaml/v3`). Claude Code's `.claude/agents/*.md` files load unchanged; keys uah does not use (`color`, `permissionMode`, `hooks`, and others) produce a warning.

```markdown
---
name: reviewer
description: Reviews a diff for bugs and missing tests. Use after a change.
tools: Bash, Edit, mcp__github
model: gpt-6-luna
effort: high
fast: true
approve:
  - git diff
  - Bash(git log:*)
  - mcp__github__get_pull_request
---

Review only; do not edit files. List each finding with its file and line.
```

| Key | Role field | Notes |
| --- | --- | --- |
| `name`, `description` | `Name`, `Description` | Required, as in both Claude Code and Codex |
| the body | `DeveloperInstructions` | Required, as Codex requires `developer_instructions` |
| `model` | `Model` | `inherit` and Claude Code's aliases (`sonnet`, `opus`, `haiku`, `fable`, with a warning) are the parent's model |
| `effort`, `model_reasoning_effort` | `Effort` | Claude Code's key and Codex's; Codex's wins |
| `fast`, `service_tier` | `ServiceTier` | `fast: true` is `priority`, `false` is `default`; `service_tier` wins |
| `tools` | `Tools` | A comma-separated string or a list, mapped by `mapTools` (`toolnames.go`) |
| `approve` | `Approve` | A list, read by `mapApprove` |
| `nickname_candidates` | `NicknameCandidates` | Codex's key |

**Tools.** `mapTools` keeps uah's names and maps Claude Code's: `Edit`, `Write`, `MultiEdit`, and `NotebookEdit` to `apply_patch`, `Skill` to `SkillUse`, and `mcp__<server>` or `mcp__<server>__*` stay patterns for a server's tools. `Read`, `Grep`, `Glob`, `LS`, the spawn tools, and unknown names drop with a warning. A non-nil empty list offers no tools, so a list whose names all drop never grants every tool. A missing `tools` key offers every tool, as in Claude Code.

**Pre-approval.** `mapApprove` keeps command prefixes (`git diff`, Claude Code's `Bash(git diff *)` and `Bash(git diff:*)`), `apply_patch` (or `Edit`, `Write`), and MCP names or server patterns; an empty prefix, a bare tool name, and anything that is not plain words drop with a warning.

**Enforcement.** `Manager.scope` passes the role's `Tools` and `Approve` to the engine as an `engine.Scope` for the child's session ID when the child starts or resumes (a fork gets none: it keeps its parent's tools). The embedded engine keeps it per session, as it keeps the cache key, and each run of the child applies it (`internal/engine/embedded/scope.go`):

- the registry: built-in tools the scope does not offer join the request's `DisallowedTools`, which the registry already honors for Bash, ViewImage, SkillUse, and `apply_patch`, and MCP tools it does not offer are left out, so the model never sees them and a call to one fails as an unknown tool;
- commands and patches: the prefixes are the approver's kind of prefix rule (`rules.FromPrefixes`, matched with `rules.Policy.Check`, so every simple command must match). They sit in front of the run's ask, before the auto-reviewer, and answer "approve" where the approver would ask. They are not added as `allow` rules, because an allow rule runs a command outside the sandbox without asking, which would widen the mode. So a `forbid` rule and `approval_policy = "never"` decide before any ask, an approved escalation runs outside the sandbox as the user's approval would, and in read only mode an escalation is still asked;
- MCP tools: the MCP gate treats a pre-approved tool as `approval_mode = "approve"`, the mode "always allow this tool" sets.

`TestScope_Tools`, `TestScope_PreApproval`, and `TestScope_PreApprovalKeepsReadOnly` check these end to end with fakellm and the sandbox; `TestLoadRoles_Markdown` and `TestLoadRoles_MarkdownBesideTOML` check the loading.
<!-- /memoria:section -->

<!-- memoria:section id="extending" files="tools.go prompt.go roles.go" -->
## Extending

- **A tool.** Add an entry to `tools` in `tools.go` with its name, description, JSON Schema, and run function, and its text to `prompt.go`. The engine offers and runs it with no change. Keep a removed tool's name in `ToolNames`, as `wait` is kept, so sessions with past calls resume.
- **A tool set, such as Codex's v2.** Choose the set in `definitions` and `Call` by configuration; the seam and the engine stay the same.
- **A role key.** Add the field to `Role` with its TOML name and to `front` (and `frontKeys`) with its Markdown name, apply it in `childOptions` or `Manager.scope`, and document it in the configuration reference. `LoadRoles` warns about any key either format does not decode.
<!-- /memoria:section -->
