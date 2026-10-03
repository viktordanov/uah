<!-- memoria:section id="overview" files="goal.go" -->
# Goals

`internal/goal` is Codex's `/goal` as data: a session's goal, its statuses and usage, the texts that tell the model about it, the goal tools' definitions and results, and the words the TUI and `uah exec` show. It does no I/O and imports no uah package. The session keeps the goal and continues it ([sessions](../session/README.md#goals)), the engine offers the tools ([engine](../engine/README.md#the-goal-tools)), and the TUI drives it ([TUI](../tui/README.md#goals)). The [design record](../../docs/design/goal.md) has Codex's behavior, checked at b741e48, and the decisions.

<!-- memoria:export id="summary" -->
`/goal <objective>` keeps the agent working, run after run, until the model marks the goal complete with Codex's `update_goal` after Codex's completion audit. Each automatic run starts with Codex's continuation message; the goal, kept in the session's sidecar, survives a resume and a compaction. A token budget, a cap of 50 automatic runs, a failed run, and three runs in a row without progress stop it; an interrupt pauses it, and `/goal clear` or `/clear` drops it. Subagents never inherit it.
<!-- /memoria:export -->

1. [The goal](#the-goal)
2. [What the model reads](#what-the-model-reads)
3. [The tools](#the-tools)
4. [What the user sees](#what-the-user-sees)
5. [Tests](#tests)
<!-- /memoria:section -->

<!-- memoria:section id="goal" files="goal.go" -->
## The goal

`Goal` is what the session keeps in its sidecar's `goal` field: Codex's fields (an ID, the objective, the status, the token budget, the tokens and seconds used, the times) and uah's (`Continuations`, the runs uah started for it, `MaxContinuations`, their cap, and `Reason`, why a guard stopped it). The statuses are Codex's, but `usage_limited`: `active`, `paused`, `blocked` (shown as stalled), `budget_limited`, and `complete`; `Finished` is true for the last two, which a new goal replaces without a clear.

| Function | Does |
| --- | --- |
| `CheckObjective` | Trims the objective; empty or over 4,000 characters (Codex's `MAX_THREAD_GOAL_OBJECTIVE_CHARS`) is an error with Codex's text |
| `CheckBudget` | Codex's `validate_goal_budget`: positive, and at most `[goals] max_goal_token_budget` |
| `TokenDelta` | What one model response adds to the goal's tokens: input not read from the cache, plus output, Codex's `goal_token_delta_for_usage` |
| `OverBudget`, `OutOfContinuations`, `RemainingTokens` | The budget and the cap |

`Settings` are the `[goals]` keys: `Disabled` (`[features] goals = false`), `MaxTokenBudget`, and `MaxContinuations` (`DefaultMaxContinuations`, 50, when the key is unset; 0 is no limit).
<!-- /memoria:section -->

<!-- memoria:section id="texts" files="context.go prompts/continuation.md prompts/budget_limit.md prompts/objective_updated.md" -->
## What the model reads

Every text is Codex's (`codex-rs/ext/goal/templates/goals/` and `core/src/context/user_goal.rs`, b741e48, Apache License 2.0), in Codex's hidden-context wrapper, and goes to the model as a message the session appends, so the system prompt and every earlier item stay the same and the prompt cache holds:

| Function | Text | Sent |
| --- | --- | --- |
| `Continuation` | `<codex_internal_context source="goal">` around `prompts/continuation.md`: the objective as escaped XML text in `<objective>`, the budget, the no-progress check, fidelity, the completion audit, and the blocked audit. The `update_plan` paragraph is dropped, as Codex's `without_update_plan_instructions` drops it, and with a cap a line `- Automatic continuations: 3 of 50` follows the budget | As the user message of each run uah starts for the goal |
| `BudgetLimit` | `prompts/budget_limit.md`: wrap up, no new substantive work | To the live run when the budget runs out |
| `ObjectiveUpdated` | `prompts/objective_updated.md`, the new objective in `<untrusted_objective>` | To the live run when the user edits the objective |
| `UserSet`, `UserCleared` | `<codex_internal_context source="user_goal">` with `User set the goal: "<objective as JSON>"`, `User set goal status: "paused".`, or `User cleared the goal.`; an objective over 700 bytes is recorded as omitted | With the next run, after the user's change |

The prompt files are Codex's, byte for byte, with Codex's `{{ name }}` placeholders, which `render` fills in one pass. `Parse` tells the five apart (`KindContinuation`, `KindBudgetLimit`, `KindObjectiveUpdated`, `KindUser`, `KindNone`) from the wrapper and the first line, so the TUI, `uah sessions show`, compaction, and the auto-reviewer know them in a live and a resumed session.
<!-- /memoria:section -->

<!-- memoria:section id="tools" files="tools.go" -->
## The tools

`Tools` are Codex's `get_goal`, `create_goal`, and `update_goal` (`codex-rs/ext/goal/src/spec.rs`): the names, descriptions, and JSON schemas word for word. `ParseArgs` reads a call's arguments; `ErrUnfinished`, `ErrNoGoal`, and `ErrUpdateStatus` are Codex's refusals. `Result` is Codex's `GoalToolResponse`: `{"goal":{"threadId",…,"tokensUsed","timeUsedSeconds","createdAt","updatedAt"},"remainingTokens":…,"completionBudgetReport":…}` with `null` for what is missing, the session ID as the thread ID, and Codex's completion report when the model marks a budgeted or timed goal complete. The session applies the calls ([sessions](../session/README.md#goals)); the engine offers them ([engine](../engine/README.md#the-goal-tools)).
<!-- /memoria:section -->

<!-- memoria:section id="display" files="display.go" -->
## What the user sees

`Indicator` is the footer's text, with Codex's words: `Pursuing goal (12.5K / 50K)` with a budget, else the time, `Goal paused (/goal resume)`, `Goal stalled (/goal resume)`, `Goal unmet (63.9K / 50K tokens)` or `Goal unmet (50 / 50 continuations)`, and `Goal achieved (…)`. `Summary` is `/goal status`: the status, objective, time, tokens, budget, continuations, why a guard stopped it, and the commands that apply. `Line` is the goal in one line, for a notice and `uah exec`. `Elapsed` and `Tokens` are Codex's compact forms (`1h 30m`, `12.5K`); `Usage` is the command's synopsis.
<!-- /memoria:section -->

<!-- memoria:section id="tests" files="goal_test.go" -->
## Tests

`goal_test.go` pins each text against Codex's prompt files (every line kept but the plan paragraph and the placeholders, the objective escaped), the `user_goal` record and its 700-byte limit, `Parse` on each kind and on look-alikes, the checks with Codex's errors, the token delta, the tools' schemas, `Result`'s JSON, and the footer's words. The loop itself is tested end to end in `internal/app/goal_test.go` ([sessions](../session/README.md#goals)).
<!-- /memoria:section -->
