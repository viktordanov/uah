# Goals (`/goal`)

Status: built (ledger item 115). The package READMEs hold the current contract: [internal/goal](../../internal/goal/README.md), [sessions](../../internal/session/README.md#goals), the [engine](../../internal/engine/README.md#the-goal-tools), and the [TUI](../../internal/tui/README.md#goals). This record keeps the research and the decisions.

Ledger item 115, from the Pi study (`.scratch/archive-2026-10-03/pi-REPORT.md`, idea 7): a persistent objective, and the harness keeps the agent working until the goal is met. The study's sources were Codex's `/goal`, Pi's pi-goal-x, Claude Code's ralph-loop plugin, and oh-my-opencode's todo-continuation enforcer, and its risk was a runaway loop, "so it needs a budget and an auditor". The owner asked for parity with Codex first, then for what safety needs: a budget, a stop on repeated turns without progress, and esc and clear always stopping it.

1. [What Codex does](#what-codex-does)
2. [The other harnesses](#the-other-harnesses)
3. [The design](#the-design)
4. [Decisions](#decisions)
5. [Try it](#try-it)
6. [Open](#open)

## What Codex does

Checked against Codex `main` at b741e48 (2026-10-03). Paths are under `codex-rs/`. The feature is the `goal` extension (`ext/goal/`), the goal store (`state/`), the app server's goal requests, and the TUI.

- **The goal is stored per thread** in the state database (`state/goals_migrations/0001_thread_goals.sql`): one row per thread with a goal ID, the objective, the status, an optional token budget, the tokens and seconds used, and the times. The statuses are `active`, `paused`, `blocked`, `usage_limited`, `budget_limited`, and `complete`; `budget_limited` and `complete` are terminal (`state/src/model/thread_goal.rs`). An objective is at most 4,000 characters (`MAX_THREAD_GOAL_OBJECTIVE_CHARS`).
- **The feature** is `[features] goals`, stable and on by default (`features/src/lib.rs`). `[goals] max_goal_token_budget` caps a goal's budget and is the budget of a goal set without one (`config/src/config_toml.rs`, `ext/goal/src/api.rs`).
- **What the model sees.** Nothing in the system prompt. When the user sets, edits, pauses, resumes, or clears the goal, the thread records a user-role message with host provenance, `<codex_internal_context source="user_goal">` and `User set the goal: "<objective as JSON>"`, `User set goal status: "paused".`, or `User cleared the goal.` (`core/src/context/user_goal.rs`); an objective over 700 bytes is recorded as omitted, so a cut cannot change its meaning. Each automatic turn starts with a hidden user message, `<codex_internal_context source="goal">` around `templates/goals/continuation.md`: the objective as data in `<objective>`, the token budget and what is left, a no-progress check, fidelity rules, a requirement-by-requirement completion audit, and when to report a blocker. Two more messages steer a live turn: `budget_limit.md` when the budget runs out (wrap up, no new substantive work) and `objective_updated.md` when the user edits the objective (`ext/goal/src/steering.rs`). The paragraph about `update_plan` is dropped when the plan tool is off (`prompts/src/update_plan_instructions.rs`).
- **Three tools** (`ext/goal/src/spec.rs`, `tool.rs`): `get_goal` (the goal, its usage, and the remaining budget), `create_goal` ("only when explicitly requested by the user or system/developer instructions"; an optional `token_budget`; refused while an unfinished goal exists), and `update_goal` with `status` `complete`, `blocked`, or `paused` only. Each returns `{"goal":{…},"remainingTokens":…,"completionBudgetReport":…}`; marking a budgeted or timed goal complete adds a note asking the model to report its usage.
- **When it continues.** When the thread goes idle (`on_thread_idle`, after any turn ends and nothing is queued) and the goal is active, the extension starts a turn with the continuation message (`continue_if_idle`, `ext/goal/src/runtime.rs`). Setting or resuming a goal while idle starts one at once. A turn the user started while the goal is active counts as a goal turn, and the goal continues after it. A resumed thread does not start a turn by itself: its active goal continues when the next turn ends.
- **Who decides it is done.** The model, with `update_goal` `complete`, after the completion audit the continuation message demands. There is no separate judge: the "auditor" is the audit in the prompt plus the runtime's guards below.
- **The guards** (`ext/goal/src/accounting.rs`, `runtime.rs`, `extension.rs`). Tokens count as input not read from the cache plus output, per response; when the budget is used, the goal becomes `budget_limited` and the live turn gets the budget message after its next tool call. Three automatic turns in a row with no activity (no text, no tool call) block the goal; so do three goal turns in a row where a command failed and no tool succeeded. A turn that ends with an error blocks it ("to prevent automatic continuation from looping and consuming tokens"); a usage-limit error makes it `usage_limited`. The model blocks it with `update_goal` only after the same blocker repeated for three goal turns.
- **The user** (`tui/src/slash_command.rs`, `tui/src/app/thread_goal_actions.rs`, `chatwidget/goal_menu.rs`, `goal_display.rs`): `/goal` alone shows the status, objective, time, tokens, and budget with the commands that apply; `/goal <objective>` sets it, asking before it replaces an unfinished goal; `/goal edit` opens the objective in a prompt and keeps the usage (a finished goal becomes active again); `/goal pause`, `/goal resume`, `/goal clear`. `/goal` works while a turn runs. The footer shows `Pursuing goal (12.5K / 50K)` or the elapsed time, `Goal paused (/goal resume)`, `Goal stalled (/goal resume)`, `Goal unmet (63.9K / 50K tokens)`, or `Goal achieved (…)` (`bottom_pane/footer.rs`, `chatwidget/goal_status.rs`). Esc during an active goal's turn pauses the goal (`chatwidget/interaction.rs`). After a resume, a paused or stalled goal gets a "Resume paused goal?" prompt.
- **Compaction** keeps the user's goal messages (`input_goal_ids`, `core/src/compact.rs`, `session/mod.rs`), whole and in their order; the continuation and steering messages are contextual fragments, left to the summary.
- **Subagents** each have their own thread, and so their own goal row; none inherits its parent's. The tools are hidden from a review subagent. A child's tokens count toward the root goal's budget.
- **`codex exec`** has no `/goal`; its thread is persistent, so the model may call the tools.

## The other harnesses

From the Pi study, which lists them as the same idea.

- **Claude Code's ralph-loop** (a plugin): a Stop hook that sends the same prompt again when the agent stops, until the agent prints a completion promise or an iteration cap is reached. uah's Stop hooks can already do this (at most five times in a row).
- **oh-my-opencode's todo-continuation enforcer**: continues the agent while its todo list has open items.
- **Pi's pi-goal-x**: a persistent objective that the harness keeps the agent on, with an auditor; the study names it, without its details.

## The design

1. **The goal is the session's** (`internal/session/goal.go`), kept in its sidecar (`sessions/<id>.uah.json`, `goal`), so it survives a restart and a resume, and a compaction cannot touch it. `internal/goal` holds the goal as data: the statuses, the checks, Codex's texts, the tools' definitions and results, and the footer's words. It imports no uah package.
2. **The texts are Codex's**, word for word, in Codex's wrapper, `<codex_internal_context source="…">`, so a model trained on Codex reads them as it does there: the user's record (`user_goal`), the continuation, the budget limit, and the edited objective. uah adds one line to the continuation's budget, `- Automatic continuations: 3 of 50`, and drops the `update_plan` paragraph, as Codex does without the plan tool. The system prompt and the tools never change, and each message is appended, so every request extends the one before it and the prompt cache holds (`TestGoal_ContinuesUntilComplete` checks each request's input against the last).
3. **The user's changes** (set, edit, pause, resume, clear) are held as Codex's `user_goal` records and go to the model with the next run, as `Session.Inject` holds a message.
4. **Continuing.** When a run ends with the goal active, nothing queued, no message waiting for its hooks, and the Stop hooks done, the session starts a run of its own with the continuation message (`continueGoal`), instead of reporting `Idle`. `/goal <objective>` or `/goal resume` while idle starts one at once. A run the user starts while the goal is active works on the goal, and the goal continues after it. A resumed session continues its active goal when the next run ends, as Codex does.
5. **The tools** are Codex's three, with Codex's names, descriptions, schemas, refusals, and results (`internal/engine/embedded/goaltool.go`). The session applies a call on its loop (`engine.Options.Goal`); the call's decision does it (`gatedTranslator`), and the call then runs as a job (`uah.goal`) that completes at once with the result, as an answered `request_user_input` does, so a run that restarts never applies a call twice. A goal the model creates makes its run a goal run.
6. **Done** is the model's `update_goal` `complete`, after Codex's completion audit, as in Codex.
7. **The guards**, which stop the goal and say why (`Goal.Reason`, the notice, and `/goal status`):

   | Guard | Status | Source |
   | --- | --- | --- |
   | The token budget (`token_budget`, `[goals] max_goal_token_budget`) is used | `budget_limited`, and Codex's budget message to the live run | Codex |
   | The run fails (an error, the disk limit) | `blocked` | Codex (a turn error) |
   | Three automatic runs in a row make no tool call | `blocked` | Codex's three empty turns, made stricter: a turn that only writes text is no progress either |
   | Three goal runs in a row where a command failed and no tool call succeeded | `blocked` | Codex |
   | The goal used its automatic continuations (`[goals] max_continuations`, 50) | `budget_limited` | uah |
   | The user interrupts a run while the goal is active (esc esc, `/stop`) | `paused` | Codex (the TUI's esc) |
   | `/clear` | the goal is cleared | Codex (`/clear` starts a thread without one) |

8. **Steering a live run.** The budget message and the edited objective go to the live run as developer messages, which ask for no response of their own, so they cancel no model request: each rides the run's next request, with the output of the calls under way. A user message, as Codex sends, would make the runner drop a request already sent with the tool output and ask again.
9. **The TUI** (`internal/tui/state/goal.go`): `/goal` with Codex's words and `status`; the footer's indicator with Codex's words, its time counting the live run; a notice for each change (a guard's as a warning); a continuation's message as `↻ continuing the goal automatically (3 of 50)` in place of its text, and the user's records only in the detailed view, also in a resumed transcript. `/goal edit` alone puts `/goal edit <objective>` in the composer.
10. **`uah exec`** has no `/goal`, as `codex exec`; the tools are offered, so a prompt that asks for a goal gets one, and `uah exec` waits until the session is idle, so it runs the goal to its end. `--json` adds `goal_updated`, `goal_continued`, and `goal_cleared` events, and the progress lines say each change and continuation. `uah sessions show` names the goal's messages.
11. **Compaction** keeps the user's goal records whole, whatever the cap on kept messages, and leaves the continuations and the steering to the summary (`compaction.Kept`, `compaction.Developer`), as Codex does. The goal itself is in the sidecar, and each continuation carries the objective again.
12. **Subagents** never inherit the goal and get no goal tools: their sessions have a parent, which turns goals off (`session.Options.Goals` is ignored). A child forked with `fork_context` is offered the tools, so its tools and prompt cache stay its parent's, and is refused at the call, as for `request_user_input`.

## Decisions

- **Codex's tools and texts, not new ones.** The parity rule: a model trained on Codex calls `update_goal` and runs the completion audit as it does there.
- **No separate auditor.** Codex has none: the continuation message makes the model audit every requirement against current evidence before it marks the goal complete, and the runtime's guards stop a loop that does not finish. A second model judging completion would cost a call per run and disagree with the model that did the work in ways the user cannot see; it stays open below.
- **A cap on continuations by default.** Codex's loop ends only by the model, the budget, or a guard. Without a token budget, which is unset by default, a goal that never finishes and never trips a guard (a model that calls a tool every turn) would run until the plan's limit. uah stops it after 50 automatic runs; `/goal resume` gives it 50 more, and `max_continuations = 0` restores Codex's behavior.
- **A text-only automatic turn is no progress.** Codex counts a turn with text as activity. A continuation that only restates the plan or says it is waiting does not move the work, and three in a row are the loop the study warned about.
- **The interrupt pauses in the session**, not in the TUI as Codex does, so every client gets it: an interrupt during a goal run pauses the goal, and nothing continues. Closing the session does not pause it.
- **`/clear` clears the goal.** uah's `/clear` keeps the session where Codex starts a new thread, which has no goal.
- **No replace prompt.** `/goal <objective>` over an unfinished goal is refused with the way out (`/goal edit`, or `/goal clear` first); a finished goal is replaced at once.
- **No `usage_limited`.** uah does not tell a usage limit from other run failures; such a run blocks the goal, and `/goal resume` continues it.
- **The steering's role** is developer, not Codex's user (above): the text is the same, and no request is thrown away.
- **Subagents have no goals.** Codex gives each child thread its own; uah's children are workers that report to the main agent, which owns the goal.

## Try it

```sh
uah
/goal make `go test ./...` pass and keep the coverage of internal/foo above 80%
```

The footer shows `Pursuing goal (…)`; each automatic run starts with `↻ continuing the goal automatically (n of 50)`. `/goal` shows the goal, esc esc pauses it, `/goal resume` continues it, and `/goal clear` drops it. Headless: `uah exec "Set a goal to make the tests pass, then work on it"`, with `[goals] max_goal_token_budget = 200000` to bound it.

## Open

- A separate auditor: a direct model call that checks the claim of completion against the objective and the transcript before the goal is complete, as the Pi study suggested.
- A time budget, and `usage_limited` from the provider's usage-limit error.
- `uah exec --goal <objective>`, which Codex's exec does not have.
- A picker to confirm replacing an unfinished goal, and Codex's "Resume paused goal?" prompt after a resume (a notice says it today).
