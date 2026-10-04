# Compaction and the context meter: plan

Status: accepted, 2026-09-24 (ledger item 4); configurable and looked at again in ledger item 24; measured, with a ledger, elision, and kept calls, in ledger item 69 (2026-09-30); remote compaction in ledger item 70 (2026-09-30). Embedded engine only.

1. [How Codex compacts](#how-codex-compacts)
2. [What the runner supports](#what-the-runner-supports)
3. [Design](#design)
4. [Validation](#validation)
5. [Measuring compaction](#measuring-compaction)
6. [What a summary keeps](#what-a-summary-keeps)
7. [Remote compaction](#remote-compaction)
8. [Open decisions](#open-decisions)

## How Codex compacts

Paths are in Codex `rust-v0.156.1`, `codex-rs/`.

- **The call** (`core/src/compact.rs`, `run_compact_task_inner_impl`). The current history plus one user message with the summarization prompt (`prompts/templates/compact/prompt.md`) goes to the session's model, without tools. The last assistant message is the summary.
- **The new history** (`build_compacted_history`). Every earlier user message stays as it was, in order, followed by one user message: `summary_prefix.md`, a newline, and the summary. Assistant messages, reasoning, tool calls, and tool outputs are gone. Earlier summaries are recognized by the prefix (`is_summary_message`) and dropped, because the new summary covers them. User messages are capped at 20,000 tokens in total, newest first (`COMPACT_USER_MESSAGE_MAX_TOKENS`).
- **When** (`core/src/session/turn.rs`, `core/src/session/context_window.rs`). Before a turn samples, and after a response that needs a follow-up (tool results), when the context in use reaches the limit: 90% of the model's context window (`ModelInfo::auto_compact_token_limit` in `protocol/src/openai_models.rs`), or `model_auto_compact_token_limit` from the configuration if lower. `/compact` runs the same task on demand.
- **Windows** (`models-manager/models.json`). 272,000 tokens for every current model except `gpt-daybreak-red-latest` (372,000); unknown models fall back to 272,000 (`models-manager/src/model_info.rs`). `model_context_window` in the configuration overrides it.
- **The measure** (`core/src/context_manager/history.rs`, `get_total_token_usage`). The context in use is the last response's total tokens plus an estimate (bytes/4) of the items added after the last item the model produced. The trigger compares this, not the last response alone, with the limit.
- **Overflow** (`compact.rs`). When the summary call exceeds the window, Codex removes the oldest history item and retries. Other errors are retried with backoff up to the provider's stream retry limit.
- **The meter** (`tui/src/token_usage.rs`). "N% context left" uses the last response's total tokens (input plus output) against the window, after subtracting a 12,000-token baseline from both, so a fresh session shows 100%.

## What the runner supports

unreal-agent v0.1.1 names a compaction turn (`harness/session/session.go`: `TurnCompaction`) but never creates one: `requestModelResponse` in `harness/coordinator/loop.go` always appends `TurnRegular`. When a compaction turn is replayed, the coordinator skips its model response (`loop.go:467`, `loop.go:610`) and changes nothing else: the context builder keeps every earlier item, so the turn would not shrink the context even if uah wrote one. `contextbuilder.ChangeCompacted` is a report label with no producer, and `Store.Fork` copies a session up to a turn without summarizing it. Nothing in the runner can be driven from outside to compact, so uah compacts on its own side and the runner stays unchanged.

## Design

The coordinator calls one `llm.Adapter` for every model request. The embedded engine already wraps it (`switcher`, for live model and tier). A second wrapper, `compactor` (`internal/engine/embedded/compact.go`), sits in front of it:

1. **Apply.** Each request the context builder produces is rewritten with the session's latest compaction: the system message, then the user messages among the covered items verbatim and in order (the newest 20,000 tokens of them, without the runner's heartbeats), then the summary message (Codex's prefix and the summary), then the items after the covered ones. A compaction covers everything except the user messages at the end of the request, which are new input and follow the summary, as in Codex, where compaction runs before the new message is recorded. A tool output whose call was covered becomes a user-role note, so the provider never sees an orphan output. Everything that does not depend on the engine lives in `internal/compaction` (the rewrite, the summary call, the estimates, the window table and meter formula, the log); its [README](../../internal/compaction/README.md) describes it.
2. **Compact.** Before sending, the compactor compacts when `/compact` asked for it or when the context in use (Codex's measure) reached `auto_compact_percent` of the window. It sends the covered history (as the model saw it, so an earlier summary is included), trimmed to the window, plus Codex's prompt through `internal/llmcall` with the session's model and effort and no tools, records the result, and rewrites the request with it. The call runs as a job under the run's context: a request the coordinator cancels for a new message leaves it running, and the next request waits for it. An interrupt cancels it. If it fails, the request goes out uncompacted and the failure is reported.
3. **Persist.** A compaction is a line in `sessions/<id>.compaction.jsonl`: how many builder items it covers, a SHA-256 of those items, the summary, the trigger, and the time. The builder's history is append-only between the system message and the end, so the covered items are the same on every later build and after a resume (the coordinator replays the session file into a fresh builder). On resume the compactor reads the last readable line; if the covered items do not match it, it reports that and compacts the full history again when it is over the limit (see item 25).
4. **Events.** The engine emits `engine.CompactionStarted` and `engine.Compacted` (trigger, summary, error) into the run's event stream. The session passes them on; the TUI shows "Context compacted" (with the summary in the detailed view), and `uah run` prints a progress line. `uah run --stream` writes them as `compaction_started` and `compacted`. A reloaded transcript (the TUI, `uah sessions show`) takes them from the compaction log, because the run records hold only the runner's own events.
5. **Triggers.** `Session.Compact()` compacts before the live run's next model request, or before the first request of the next run when idle (the TUI says "The context will be compacted before the next message"). A request the live run ends without serving moves to the next run. A new run reads the last response's usage from the session file, so automatic compaction also applies after a resume. Automatic compaction needs no session involvement. Every compaction, manual or automatic, goes through `compactor.compact`, whose `BeforeCompact` callback (`embedded.Config.BeforeCompact`, unset today) is where item 9's `PreCompact` hook attaches.
6. **Meter.** The TUI keeps the last `ModelResponded` usage and shows "N% context left" in the footer (both views) with Codex's formula. The window is `model_context_window` if set (carried in `session.Settings.ContextWindow`), else the table's value for the current model. It works on both engines because it reads core events; a compaction clears it until the next response.
7. **Process engine.** `Capabilities.Compaction` is false; `/compact` says it needs the embedded engine. The runner process builds its own context and cannot be rewritten from outside.

`internal/llmcall` is a model-agnostic one-shot call: items in, text out, with the model, effort, and timeout per call, over any `llm.Adapter`. The embedded engine hands it an adapter that uses the session's current client (built by `Provider.NewClient`, so every provider works) without the live model override, so a caller can pick another model. Lane A's auto-reviewer uses the same package.

Configuration:

```toml
auto_compact_percent = 90                   # 0 turns automatic compaction off
model_context_window = 272000               # tokens; default from the model catalog, else 272000
model_auto_compact_token_limit = 200000     # Codex's: compact sooner than the percent when lower
compact_model = "gpt-6-luna"                # the summary model; default: the session's, as Codex
compact_effort = "medium"                   # default: the session's
compact_prompt = "…"                        # Codex's; or experimental_compact_prompt_file = "~/…"
compact_user_message_max_tokens = 20000     # Codex's constant; default at most a quarter of the window
```

The Codex keys and where Codex reads them (`rust-v0.156.1`, `codex-rs/`): `compact_prompt` (`config/src/config_toml.rs:264`), `experimental_compact_prompt_file` (`config_toml.rs:543`; `compact_prompt` wins, `core/src/config/mod.rs:3908-3974`), `model_auto_compact_token_limit` (`config_toml.rs:170`; applied in `models-manager/src/model_info.rs:29`, and the effective limit is the lower of it and 90% of the window, `protocol/src/openai_models.rs:525-536`), and `model_context_window` (`config_toml.rs:167`). Codex has no summary model setting: it compacts with the session's model (`core/src/compact.rs`), and with the previous model when a model switch shrinks the window (`core/src/session/turn.rs:1342`). The kept-message cap is a constant (`COMPACT_USER_MESSAGE_MAX_TOKENS`, `core/src/compact.rs:60`). Codex's `/compact` takes no argument (`tui/src/slash_command.rs:166`, not in `supports_inline_args`). Claude Code documents `/compact [instructions]` for focus instructions, the `autoCompactEnabled` setting, a "Compact Instructions" section in CLAUDE.md, and no separate compaction model (code.claude.com/docs: commands, settings reference, how Claude Code works).

## Validation

Checked on 2026-09-24 against Codex `rust-v0.156.1` and the runner (unreal-agent v0.1.1). Each fix has a test through the real engine and `fakellm`; the compaction tests pass with `-race -count=20`.

| # | Finding | Severity | Status |
| --- | --- | --- | --- |
| 1 | The automatic trigger used only the last response's tokens. A large tool output after it could push the next request past the window without a compaction, and the request failed. Codex adds an estimate of the items after the last model item. | High | Fixed: `compaction.InUse`. |
| 2 | A provider that reports no usage (or a request right after a compaction) never triggered automatic compaction. | High | Fixed: the whole request is estimated when usage is missing. |
| 3 | The summary call sent the whole history even when it exceeded the window (one huge tool output, or a mismatched record that sends the full history), so it failed exactly when it was needed. | High | Fixed: trimmed to the window from the oldest item, and retried with less on a context-length error, as Codex. |
| 4 | The coordinator cancels the model request when a message arrives. A message sent during the summary canceled it: `/compact` was lost (reported as "failed: context canceled") and an automatic compaction started over, paying twice. | High | Fixed: the compaction is a job under the run's context that the next request joins. |
| 5 | An interrupt during the summary was reported as a failure, and the request could still go out. | Medium | Fixed: reported as interrupted (`engine.Compacted.Interrupted`); the waiting request does not go out; the run waits for the job before it ends. |
| 6 | One unreadable line in the compaction log (a write cut short by a crash) stopped every later run of the session from starting. | High | Fixed: unreadable lines are skipped and logged; a cut line is ended before the next append; appends are synced. |
| 7 | Codex's 20,000-token cap on kept user messages was not applied, so very long messages could fill the window again right after a compaction. | Medium | Fixed: Codex's selection and middle truncation, with Codex's marker. |
| 8 | The runner's heartbeat messages were kept as user messages, with stale lists of running calls. Codex keeps only real user messages. | Low | Fixed: heartbeats are left to the summary. |
| 9 | An automatic compaction whose summary call keeps failing ran before every model request. | Medium | Fixed: it stops for the run after three failures in a row. |
| 10 | Compactions were missing from reloaded transcripts and `uah run --stream`. | Medium | Fixed: see Events above. |
| 11 | A late output of a covered call became a note without its images. | Low | Fixed: the note names each image. |
| 12 | Multiple compactions: the second summary call sees the first summary, and the rewrite replaces it. | — | Verified; test. |
| 13 | A tool call still running at the compaction: its placeholder is covered, and its final output arrives later as a note, never as an orphan output. | — | Verified; test. |
| 14 | The covered items are stable: the builder commits a request's items when the turn starts, before the model call, and later tool results only append. A resume replays the same items. | — | Verified in the runner (`coordinator/loop.go`, `contextbuilder/builder.go`); resume and mismatch tests. |
| 15 | A summary with no text fails the compaction; Codex stores "(no summary available)" and drops the history. | Low | Kept: failing keeps the full history, which loses less. |
| 16 | Images in user messages: the runner's `llm.Message` carries text only (v0.1.1), so user messages have no images to keep. | — | Not applicable. |
| 17 | Each request hashes the covered items (JSON and SHA-256 of the history). | Low | Kept: a few milliseconds per MB of history, well under a model call. |

### Second look (ledger item 24)

Checked on 2026-09-24 after the keys above were added. The compaction tests, old and new, pass with `-race -count=10`.

What a summary keeps: the system prompt (the base instructions, AGENTS.md, the environment context, and skills, which uah never rewrites, where Codex re-injects its initial context after compacting), the user's messages word for word up to the cap, and the model's handoff summary: progress, decisions, constraints, next steps, and the data it judged critical. What it loses: tool outputs (file contents, test output, diffs), the assistant's messages and reasoning, and the list of tool calls. On the next turns the model works from the summary and reads again the files it needs, a few tool calls, as in Codex. The prefix after a compaction (system, kept messages, summary) stays the same on every later request, so the prompt cache holds from the second request on.

| # | Finding | Severity | Status |
| --- | --- | --- | --- |
| 18 | An automatic compaction that leaves the context above the limit, because the system prompt and the kept messages alone fill it, compacted again before every later request: one summary call per model request. Codex accepts this risk ("as long as compaction works well…", `core/src/session/turn.rs`). | High | Fixed: the compaction reports a warning (`engine.Compacted.Warning`) and automatic compaction stops for the run; `/compact` still works. Test: `TestEmbedded_AutoCompactionStopsWhenItCannotGetUnderTheLimit`. |
| 19 | Codex's 20,000-token cap is a fourteenth of its 272,000-token windows, but more than the whole window of many local models (8,000 to 32,000 tokens), so a compacted context there was mostly old messages, or did not fit. | Medium | Fixed: the default cap is at most a quarter of the window (`Settings.KeepFor`); a configured cap wins. |
| 20 | A cap applied at rewrite time would change what every earlier compaction rewrites to when the setting changes: another request prefix, a prompt cache miss, and a history the model never saw. | Low | Fixed: a record saves its cap (`Record.Keep`). |
| 21 | The summary model is the session's; a cheaper one saves cost, but its call cannot reuse the session model's prompt cache, and its window may be smaller. | — | Configurable (`compact_model`, `compact_effort`); a smaller window trims the oldest history first, as a summary overflow does. |
| 22 | A model switch to a smaller window: the next request compacts with the new model, which trims the oldest history to fit. Codex first compacts with the previous model (`turn.rs:1342`). | Low | Open: see Later in the ledger. |
| 23 | Claude Code steers a summary with `/compact [instructions]`. | — | Adopted: `/compact <focus>` adds the focus to the prompt for that summary; the record keeps it. |
| 24 | Claude Code's summary prompt asks for more structure (files and code, errors and fixes, all user messages), and its "Compact Instructions" in CLAUDE.md steer every summary. uah keeps the user messages themselves and Codex's shorter prompt. | — | Kept: `compact_prompt` or `experimental_compact_prompt_file` sets a longer prompt for those who want one. |

### A saved compaction that stopped matching (2026-10-04)

A long session on the owner's machine stopped working after an upgrade: every message reported "the saved compaction no longer matches the session; sending the full history", the full history (16 MB) overflowed the window, and the next message did the same.

| # | Finding | Severity | Status |
| --- | --- | --- | --- |
| 25 | Item 14 holds for the items, not for the tool outputs: a resume renders each output again from its operation, with the resuming version's tools. The sandbox hint went by the run's sandboxing shells, and a new policy (the session's `$TMPDIR`, the read-only scripts) named the shell anew, so four denied commands lost their hint and every record's hash failed. | High | Fixed: the hint goes by the command's recorded shell (`sandbox.ScriptMode` reads an older script's header), so old records match again; new records also carry `Shape`, which leaves the tool outputs out, so a later change of an output's format does not break them. Test: `TestEmbedded_CompactionSurvivesANewSandboxPolicy`. |
| 26 | After a mismatch the context in use still counted the last response's tokens, which measured the compacted request: the full history looked small, no compaction ran, and it went out over the window. | High | Fixed: the full history is estimated, and compacts again from scratch over the limit. Test: `TestEmbedded_AStaleCompactionCompactsAgain`. |
| 27 | A remote compaction sent a history over the window whole, which the provider refuses; a request after a failed compaction went out although it could not fit. | Medium | Fixed: such a history goes to the local summary, which trims it; a request estimated to be over the window after a failed compaction is not sent, and the run ends with why. Tests: `TestEmbedded_RemoteCompactionOverTheWindowSummarizesLocally`, `TestEmbedded_AHistoryOverTheWindowIsNotSent`. |

## Measuring compaction

Ledger item 69. The research before it (2026-09-30, 54 sessions of the owner's) found that tool outputs are about 72% of a long context (Bash 79% of them, SkillUse 19%), that uah's summaries were 173 to 521 tokens and kept 9% of the file paths read and none of the failing commands, and that the next 20 Bash calls after a compaction read again what was dropped at a rate of 0.27, against 0.18 at random points. Nothing logged what a compaction did.

**Each compaction.** A record carries `Stats` (`internal/compaction/stats.go`): the strategy (local, remote, elide, clear), the phase (pre-turn when the request ends with new user messages, else mid-turn), the context in use before (Codex's measure) and the estimate of the request after, the summary's and the ledger's tokens, the stubbed outputs, the summary call's usage (input, cached, output, reasoning), and the duration. The engine logs a line with them; `uah exec`, `--json` (`stats`), and `uah sessions show` print them, for example `context compacted (manual): 145,809 → 23,147 tokens (local, pre-turn, 480-token summary, 350-token ledger, 12.3s)`.

**Offline.** `internal/compaction/eval` measures a strategy on one request, with no I/O: tokens per actor (system, user, summary, assistant, reasoning, tool call, tool output), headroom in the window, the tokens after the longest prefix shared with the request before (what the first request after pays uncached), the input of the model call the strategy made, the facts of the covered history found in the request after (by the rules below, outside the system message), the user messages kept word for word, the SkillUse bodies kept, and re-fetch risk: among the next 20 Bash calls the session really made, those that name a path or repeat a command whose output the strategy dropped. `internal/compaction/evalrun` runs it on recorded sessions. The session file is append-only, so any prefix is a valid session: it copies a session up to an item into a scratch home (with its rewinds made by then and every process group zeroed, since uagent kills the groups a session file records as live), opens it on the embedded engine with a provider whose client captures the next request, and applies each strategy to that request with the compaction package's own functions. The cut points are the session's recorded compactions and the requests that first reached 50k, 100k, 150k, and 200k input tokens. Summaries come from the session's compaction log (for the default prompt), a cache keyed by the covered items' hash and the prompt, or a fixed stub, so a run never calls a model; `--summary-model` writes the missing ones with a real model and caches them.

`uah compaction eval [session file or directory]` (hidden) prints the tables, numbers only. `internal/compaction/evalrun/testdata/sessions` is a synthetic session the embedded engine recorded with `fakellm` (`go test ./internal/compaction/evalrun -run TestRecordFixtures -update`), and `TestRun_Fixtures` holds each strategy to its bounds in `go test`, as the Markdown benchmarks gate the renderer. On the owner's sessions (15 sessions, 31 cases) the capture reproduced all 5 recorded compactions exactly: the covered items hash as the records do.

The owner's sessions, medians over the 31 cases and pooled recall (2026-09-30; stub summaries except 5 recorded and 8 written by gpt-6.1-sol):

| Strategy | Tokens after | After / before | Uncached first request | Changed files | Failing commands | Paths read | Re-fetch |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| None | 80,261 | 1.000 | 0 | 100% | 100% | 100% | 0% |
| Codex's summary | 8,069 | 0.095 | 491 | 23% | 6% | 10% | 27% |
| Summary and ledger | 8,518 | 0.100 | 723 | 100% | 92% | 88% | 27% |
| Summary, ledger, last 5 calls | 23,147 | 0.332 | 15,614 | 100% | 98% | 93% | 15% |
| Elision alone (default rules) | 38,023 | 0.515 | 27,175 | 100% | 100% | 100% | 7% |
| uah's automatic compaction | 31,938 | 0.476 | 22,924 | 100% | 100% | 100% | 10% |

The facts are checked by rules, which cannot judge a summary's goal or decisions; recall of an encrypted remote item cannot be measured at all, so the tables show it as n/a and take its size as the summary's. Estimates are bytes/4, as Codex's.

## What a summary keeps

**The state ledger** (`compaction.Ledger`). uah reads the covered tool calls by rules (`compaction.ExtractFacts`): the files `apply_patch` changed with their added and removed lines, the Bash commands whose last run failed with the end of their stderr, the paths the commands named and the images viewed, the skills loaded, the subagents spawned and not closed, and the `/compact` focus. It appends them to the model's summary in a `<uah_state_ledger>` block in a fixed format: paths grouped by directory, each list keeping its newest entries under a byte budget and counting the rest, at most 850 tokens. The record saves it (`Record.Ledger`), so every later request sends the same text. It costs about 350 tokens per compaction on the owner's sessions and lifted the recall of changed files from 23% to 100%, of failing commands from 6% to 92%, and of paths read from 10% to 88%.

**Elision** (`compaction.Elision`). Before an automatic summary, the outputs of tool calls with ten calls after them, and outputs over 2,000 tokens with three after them, become a stub: the tool, its command, the exit code, the size, and "Run it again if you need it". SkillUse outputs never do: a skill's body is instructions. The record keeps the elided call IDs (`Record.Elided`) and a later record carries them, so every later request, and a resumed session, sends the same stubs and the prompt cache holds after the pass; a new summary keeps only those still after the covered items. When the stubs bring the context under three quarters of the automatic limit, no summary runs; otherwise the summary runs over the history as the model last saw it, which the prompt cache already holds. It reduces how often a summary is needed; it does not replace it. `compact_elide_after_calls` sets the age (0 turns it off).

**The last calls** (`compaction.CoverableKeeping`). A summary covers all but the last five tool calls, cut where the model response that made the fifth-last call starts, so no call is split from its output and no reasoning from its call. It never covers less than the compaction before it, and covers everything when the kept calls would take more than a quarter of the window. Five calls halved re-fetch (27% to 15%) for about 15,000 more tokens after the compaction; ten brought it to 8% for 30,000. `compact_keep_recent_calls` sets the count; 0 is Codex's shape.

**The prompt.** The default summary prompt keeps Codex's opening and asks for fixed sections: Goal, Constraints, Decisions, State, Errors, TODOs, and Next (`prompts/sections.md`; Codex's is `prompts/prompt.md`). Eight cases under 50,000 tokens were summarized by gpt-6.1-sol at medium effort with each prompt: the summary alone kept 83% of the failing commands against 67%, 50% of their errors against 8%, and 48% of the paths read against 39%, in 13% fewer tokens. With the ledger the two score the same on these rules. `compact_prompt` still replaces it.

## Remote compaction

Ledger item 70. Codex `rust-v0.159.1` compacts on its OpenAI providers remotely (`core/src/compact_remote_v2.rs`, `compact_remote_v2_attempt.rs`): the turn's request, with its instructions, tools, and history, plus one `{"type":"compaction_trigger"}` input item, streamed like a turn; the answer must hold exactly one output item `{"type":"compaction","encrypted_content":...}`. The new history is the retained user messages (and hook prompts) up to 64,000 tokens (`RETAINED_MESSAGE_TOKEN_BUDGET`), then that item. Codex advertises the feature in the `x-codex-beta-features` header; its `remote_compaction_v2` key is a removed feature, always on for providers with `RemoteCompactionSupport::V2`, and there is no other toggle.

**The probe** (2026-09-30, openai-codex, gpt-6.1-sol, low effort). A request whose history held a tool output with a code, ending with the trigger, returned 200 with one `compaction` item (`id`, `type`, `encrypted_content` of 1,656 bytes; 124 input and 115 output tokens) in `response.output_item.done`, and a `response.completed` without output, as the ChatGPT backend always does. A second request with the user message, the item, and a question answered with the code, which only the dropped tool output held; the item counted as 117 input tokens. Both worked with and without Codex's beta header. The API-key openai provider was not probed; Codex enables it there.

**In uah.** The runner's `llm.Item` (v0.1.1) has neither item type, and its parser rejects an unknown output item, so the transport carries both, as it does for web searches, and the runner stays unchanged (`internal/engine/embedded/remotecompact.go`, `compactremote.go`):

1. On openai and openai-codex, with `remote_compaction` on (the default), the compactor sends the turn's model, tools, and history as the model sees it, through the switcher, with a request marker. The transport appends the trigger to the body's `input`, reads the whole answer, keeps the compaction item (from `response.output_item.done`, or from the completed response's output as the API sends it), and hands the runner the stream with an assistant message in its place.
2. The record keeps the item (`Record.Remote`), the covered count and hash as any record, and the kept-message cap (Codex's 64,000 tokens, at most a quarter of the window). `Apply` puts a placeholder user message, `[uah-remote-compaction:<hash>] …`, after the kept user messages, then the ledger. Every turn request carries the record's item in its context, and the transport replaces the placeholder's input item with it, byte for byte. A resumed session reads the item from the log.
3. Stats: the strategy is remote, the summary tokens are the call's output tokens, and the estimate after counts the item as that size.

Through the engine (the probe test `TestProbeRemoteCompactionEndToEnd`, four requests): a command printed a code, `/compact` went to the provider (6,938 input tokens, 98% cached; 72 output tokens; 4.1 s), and the next answer gave the code.

A failed remote compaction falls back to the local summary, as Codex does. A `/compact` with focus instructions uses the local summary, since only a prompt can take them. Every other provider uses the local summary and the ledger.

## Open decisions

Defaults taken; the owner can change them.

| Decision | Default | Alternative |
| --- | --- | --- |
| User messages kept after compaction | Codex's cap: the newest 20,000 tokens, the one that crosses it shortened in the middle | All, verbatim |
| Runner heartbeat messages (user-role) | Summarized, as Codex keeps only real user messages | Kept as user messages |
| A summary with no text | The compaction fails and the history stays | Codex's "(no summary available)" |
| Automatic compaction after failures | Stops for the run after three in a row | Keep trying before every request |
| `/compact` while idle | Compacts before the next message's model request | An immediate compaction run, which needs a replay-only coordinator |
| Auto-compaction measure | Last response's input plus output tokens, as Codex | Last input tokens only |
| Summary model | The session's model and effort, as Codex; `compact_model` and `compact_effort` set another | — |
| Kept-message cap | Codex's 20,000 tokens, at most a quarter of the window | `compact_user_message_max_tokens` |
| An automatic compaction that cannot get under the limit | Warn and stop automatic compaction for the run | Compact before every request, as Codex |
| Where the record lives | `sessions/<id>.compaction.jsonl` | The `<id>.uah.json` sidecar (metadata only today) |
| The ledger | Always on, appended to every summary and after a remote item | A key to turn it off |
| Elision | Before an automatic summary only, sticky, ten calls or 2,000 tokens after three | Elide on every request, which breaks the prompt cache each time |
| Recent calls after a summary | Five | Codex's none (`compact_keep_recent_calls = 0`) |
| The summary prompt | Codex's opening in fixed sections | Codex's prompt (`compact_prompt`) |
| Remote compaction | On for openai and openai-codex, as Codex | `remote_compaction = false` |
| A remote item after a model switch | Sent as saved; the provider decides | Compact again locally, as Codex's model-hash checks hint |
