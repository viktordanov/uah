<!-- memoria:section id="overview" files="compaction.go" -->
# Compaction

<!-- memoria:export id="summary" -->
uah compacts a long conversation as Codex does: the earlier user messages stay verbatim, and the rest is replaced by a model-written handoff summary, or on OpenAI providers by the provider's encrypted compaction item. uah adds a state ledger read from the tool calls, keeps the last tool calls verbatim, first replaces old tool outputs with stubs when that frees enough, and measures every compaction, with an offline evaluation over recorded sessions.
<!-- /memoria:export -->

This package holds everything about compaction that does not depend on the engine: the request rewrite, the state ledger and the facts it lists, elision, the summary call over any runner `llm.Adapter`, the token estimates, the context window lookup, the compaction log, and the stats. The embedded engine (`internal/engine/embedded/compact.go` and the files next to it) decides when to compact, runs the summary call or the remote compaction, and emits the events. `eval` and `evalrun` below it measure strategies offline. The facts about Codex were checked against Codex `rust-v0.156.1` (local compaction) and `rust-v0.159.1` (remote compaction), and the facts about the runner against unreal-agent v0.1.1. The [design record](../../docs/design/compaction.md) has the measurements behind the defaults.

1. [The request rewrite](#the-request-rewrite)
2. [What a summary keeps](#what-a-summary-keeps)
3. [The summary call](#the-summary-call)
4. [Remote compaction](#remote-compaction)
5. [Persistence and resume](#persistence-and-resume)
6. [Triggers](#triggers)
7. [Measuring compaction](#measuring-compaction)
8. [The context meter](#the-context-meter)
9. [Failures](#failures)
10. [Settings](#settings)
11. [Extension points](#extension-points)
<!-- /memoria:section -->

<!-- memoria:section id="rewrite" files="compaction.go keep.go elide.go" -->
## The request rewrite

The runner's context builder produces every model request: the system message, then the history in order. The builder is append-only after each turn, so the first items of a request stay the same on every later request and after a resume, where only their tool outputs may be rendered differently (see [Persistence and resume](#persistence-and-resume)). A compaction `Record` covers the first `Covered` items after the system message. It covers everything except the user messages at the end of the request, because they are new input and follow the summary, as in Codex, and, for a summary, the last tool calls it keeps (`CoverableKeeping`, five by default).

`Apply` rewrites a request with a record:

1. The system message, unchanged. It can change between requests (instructions, skills) without breaking the record.
2. The developer messages among the covered items, whole (`Developer`): uah's own context, such as the [prepared context](../contextprep/README.md) a session starts with, as Codex keeps its initial context. They are kept whatever the record's floor. The covered configuration updates, the items that set the effort with [effort updates](../engine/README.md#adaptive-effort), are dropped; `Configured` gives the effort they last set, which the requests after the compaction carry instead. So is a [goal](../goal/README.md)'s steering sent as a developer message (the budget limit, an edited objective).
3. The user messages among the covered items that `Kept` keeps: the newest ones up to the record's cap (`Record.Keep`). By default that is Codex's 20,000 tokens (`UserMessageMaxTokens`, Codex's `COMPACT_USER_MESSAGE_MAX_TOKENS`), at most a quarter of the window (`Settings.KeepFor`), so a small model's compacted context is not mostly old messages. The record saves the cap it was made with, so what the model sees does not change when the setting does. The message that crosses the cap is shortened in the middle with Codex's marker, "…N tokens truncated…". Older messages are left to the summary. The runner's heartbeat messages ("Heartbeat: waited …") are not the user's, so they are left to the summary too, and so are a [goal](../goal/README.md)'s continuation messages. The user's goal records (`/goal` set, edit, pause, resume, clear) stay whole, whatever the cap, in their order, as Codex keeps them.
4. The summary message: a user message with Codex's `summary_prefix.md`, a newline, the summary, and the state ledger (`Record.Ledger`). A remote compaction has a placeholder message instead, which the engine's transport replaces with the provider's item, then the ledger as its own message.
5. The items after the covered ones, unchanged, except two kinds. The output of a call the record elided (`Record.Elided`) is a stub. A tool result whose call was covered becomes a user message ("Output of the earlier tool call …"), so the provider never sees an output without its call; an image in it is named, not sent.

A record that covers nothing and elides outputs (an elision pass before any summary) only stubs those outputs.

An example, with fewer calls than a summary keeps, so it covers them all. The request at the time of `/compact`:

```text
system
developer  "<context_preparation>…"              covered
user       "fix the build"                        covered
tool call  c1 Bash {"command":"go build ./..."}   covered
tool result c1 "main.go:3: undefined: x"          covered
assistant  "I fixed main.go."                     covered
user       "also run the tests"                   new input
```

The request after it, and every later one, starts like this:

```text
system
developer  "<context_preparation>…"
user       "fix the build"
user       "<summary prefix>\n<summary>\n\n<uah_state_ledger>…"
user       "also run the tests"
...        (the items that follow)
```

A second compaction covers more items and replaces the first summary. Its summary call sees the first summary, so nothing is lost; the user messages still come from the covered items, so they stay verbatim.
<!-- /memoria:section -->

<!-- memoria:section id="keeps" files="ledger.go facts.go elide.go compaction.go" -->
## What a summary keeps

- **The state ledger.** `ExtractFacts` reads covered tool calls by rules, with no model: the files `apply_patch` changed with their added and removed lines (patches that applied only), the Bash commands whose last run failed (the runner's `Exit code: N` line) with the end of their stderr, the paths the commands named (`PathsIn`, the research's rule) and the images viewed, the skills loaded, and the subagents spawned and not closed. `Ledger` writes them, and the `/compact` focus, in a `<uah_state_ledger>` block in a fixed format: paths grouped by directory, each list keeping its newest entries within a byte budget and counting the rest, at most `LedgerMaxTokens` (850). The compactor builds it from the covered items above a `/clear`'s floor, so it lists the whole session, not only what came after the previous summary.
- **Elision.** `Elision.Elidable` picks the outputs to stub: those with `AfterCalls` calls after them, and those over `BigTokens` with `BigAfterCalls` after them (`DefaultElision`: ten, and 2,000 tokens after three), never a SkillUse body, and never an output under 100 tokens. `Stub` names the tool, the command or path, the exit code, and the size. `Record.WithElided` adds them to a record, which keeps the summary it had; `StillElided` is what a new summary keeps of them.
- **The last calls.** `CoverableKeeping` is `Coverable` leaving the last tool calls and the model output around them after the summary: the covered range ends where the model response that made the chosen call starts, so no call is split from its output. `NewRecordCovering` makes a record over such a range.
<!-- /memoria:section -->

<!-- memoria:section id="summary-call" files="summary.go prompts/prompt.md prompts/sections.md prompts/summary_prefix.md" -->
## The summary call

`SummaryRequest` builds the call from the history as the model sees it (with the latest compaction applied): the system text becomes the call's instructions, then the history, then the prompt as a user message. The prompt (`Prompt`, `prompts/sections.md`) is Codex's opening with fixed sections: Goal, Constraints, Decisions, State, Errors, TODOs, and Next. On the owner's sessions it kept more failing commands, errors, and paths than Codex's own prompt (`CodexPrompt`, `prompts/prompt.md`), in shorter summaries. `compact_prompt` or `experimental_compact_prompt_file` replaces it (`Settings.SummaryPrompt`). `uah prompts init` writes `prompt.md` to `~/.uah/prompts/compact.md` as a starting point for the file. A `/compact <focus>` adds "The user asked this summary to focus on:" and the focus, as Claude Code's `/compact [instructions]`; the record keeps the focus. `Summarize` sends the call through `internal/llmcall` with no tools and returns the text with the call's usage (`Summary`), the summary model (`compact_model`, else the session's current one, as Codex) and effort (`compact_effort`, else the session's), the session as the prompt cache key, and llmcall's timeout (5 minutes). A summary model other than the session's is trimmed to its own window.

The history can be larger than the window, for example when one tool output filled it. `Summarize` drops the oldest items until the estimate fits the window next to the prompt and the instructions, as Codex drops the oldest history when a summary call overflows. When the provider still reports an overflow (`llmcall.ErrContextWindow`), it retries with three quarters of what it sent, at most four times. A tool result whose call was dropped becomes a user message, as in the rewrite.

`prompt.md` and `summary_prefix.md` are Codex's, under the Apache License 2.0 (`prompts/LICENSE-codex`); `sections.md` is uah's, after Codex's.
<!-- /memoria:section -->

<!-- memoria:section id="remote" files="compaction.go settings.go" -->
## Remote compaction

Codex `rust-v0.159.1` compacts on its OpenAI providers by sending the turn's request with a `{"type":"compaction_trigger"}` input item after the history; the provider answers with one `{"type":"compaction","encrypted_content":...}` item, which later requests send after the kept user messages in place of the history. The runner's `llm.Item` (v0.1.1) has neither item, so the engine's transport carries them (`internal/engine/embedded/remotecompact.go`). This package holds the record's side: `Record.Remote` is the item as the provider sent it, `RemotePlaceholder` is the message `Apply` puts where it goes, `RemoteMarker` finds that message in a request body, and `Settings.RemoteKeepFor` is Codex's 64,000-token cap on kept user messages (`RemoteKeepTokens`), at most a quarter of the window. The item cannot be read, so the ledger follows it as its own message.
<!-- /memoria:section -->

<!-- memoria:section id="persistence" files="log.go compaction.go rewind.go" -->
## Persistence and resume

A compaction is one JSON line in `sessions/<id>.compaction.jsonl`, next to the runner's session file: the number of covered items, two SHA-256 fingerprints of them (`Shape` and `Hash`), the summary, the ledger, the remote item, the elided calls, the trigger, the model, the time, and the stats. The last readable line applies. An elision pass writes a line too: the record before it with more elided calls. `Log.Append` syncs the file, and it ends a line that a crash cut short before it writes, so the new line stays readable.

A resumed run replays the session file into a fresh builder, which produces the same covered items, so the record applies again. The runner renders each tool output again from its operation, though, with the tools of the version and configuration that resume: a new output format, or a sandbox hint, can change an output while the history stays the same. So `Apply` checks the record's `Shape`, a hash of the covered items with each tool result by its call alone. `Hash`, of the items with their outputs, is checked only for a record written before `Shape`, and still written for the versions before it. When the history does not match (the session file was changed outside uah), the engine reports the mismatch once and estimates the full history, since the last response measured the compacted one: over the automatic limit, it compacts again from scratch; under it, the full history goes out. A line that does not decode is skipped and logged, so one bad line does not stop a session from resuming.

The log is also the source for a reloaded transcript: `session.Load` adds each record as an `engine.Compacted` event to the run it happened in. The runner's `events.jsonl` stays as the runner wrote it.

Going back to an earlier message writes a `Rewind` to its own log, `sessions/<id>.rewind.jsonl` (`RewindLog`): the message, the first and last runner item sequences it cuts, the time, and the context the last response before the cut reported. `Cuts.Hides` tells the embedded engine's store which items to leave out, so the context builder rebuilds the history without them. Because the builder then produces different items after the cut, a compaction made before a rewind that covered the cut message no longer matches; `Cuts.After` lets the engine skip it silently and apply the one before it. `session.Load` adds each rewind as an `engine.Rewound` event. See the [rewind design](../../docs/design/rewind.md).
<!-- /memoria:section -->

<!-- memoria:section id="triggers" files="estimate.go window.go" -->
## Triggers

- **Manual.** `/compact` compacts before the live run's next model request, or before the first request of the next run when the session is idle.
- **Clear.** `/clear` records a compaction with the `clear` trigger and no summary (`NewClear`): every item so far is dropped from what the model sees, with no model call, and the session and its file stay the same. Its `Floor` is its `Covered`; `Apply` leaves the items below a record's floor out entirely, and a later compaction carries the floor forward, so a summary after a clear keeps only the user messages after it. Codex and Claude Code start a new session for `/clear`; uah stays in the session, and `/new` starts a new one.
- **Automatic.** Before each model request, when the context in use reaches the automatic limit (`Settings.Limit`) and there is something new to cover. It first tries elision; the summary runs when the stubs leave the context above three quarters of the limit. The limit is `auto_compact_percent` of the window (`AutoLimit`; 90 by default, as Codex), lowered to `model_auto_compact_token_limit` when that is smaller, as Codex takes the lower of its token limit and 90% of the window. The second condition stops a history that stays large after a compaction from compacting on every request. An automatic compaction that leaves the context at or above the limit (the system prompt and the kept messages alone fill it) reports a warning and stops automatic compaction for the run, since another summary cannot shrink what stays.

The context in use is Codex's measure (`InUse`, after `get_total_token_usage`): the last response's total tokens plus an estimate of the items added after the last item the model produced, such as tool outputs and new messages. When the last response reported no usage (a provider without usage, or the first request after a compaction), the whole request is estimated. The estimate is Codex's: the model-visible bytes divided by four, 7,373 bytes for an image, and three quarters of the encoded length less 650 for encrypted reasoning.

The window comes from `ContextWindow`, the one function every caller uses: `model_context_window` when set, else the value of the model catalog the caller passes as a `WindowLookup` (the session's `models.Manager.Window`: the provider's list, cached, or Codex's bundled catalog), else 272,000 tokens. The package reads no global catalog.

The engine runs a compaction as a job under the run, not under the request. The runner cancels a model request when a message arrives; the next request then waits for the same job instead of starting a second summary. An interrupt cancels the job, and the request that waited does not go out.
<!-- /memoria:section -->

<!-- memoria:section id="measuring" files="stats.go eval/eval.go eval/facts.go eval/report.go evalrun/cut.go evalrun/capture.go evalrun/strategies.go evalrun/summaries.go evalrun/run.go" -->
## Measuring compaction

Every record carries `Stats`: the `Strategy` (local, remote, elide, clear), the `Phase` (pre-turn or mid-turn, `PhaseOf`), the tokens in use before and the estimate after, the summary's and the ledger's tokens, the stubbed outputs, the summary call's `Usage`, and the duration. `Stats.Line` is the one-line form `uah exec` and `uah sessions show` print, and `Stats.Attrs` the engine's log line.

`eval` measures a strategy's rewrite of one request, with no I/O: `Measure` compares the request after with the `Case`'s request before, for tokens per `Actor`, headroom, the tokens after the prefix both share (the first request's cache miss), the recall of each fact `Kind` outside the system message (`ExtractFacts` of the covered items), the user messages and SkillUse bodies kept, and `Refetch`: among the next 20 Bash calls the session made, those that name a path or repeat a command whose output is gone. `Summarize` aggregates a strategy's results and `Write` prints Markdown tables of numbers only.

`evalrun` runs it on recorded sessions. `Points` picks the cuts: the session's recorded compactions and the requests that first reached `Thresholds` (50k to 200k input tokens). `Cut` copies the session file up to an item, with every `ProcessGroupID` zeroed, since uagent kills the process groups a session file records as live when a run starts. It then appends a canceled `operation` line for each operation not yet ended at the cut, so opening the copy never runs a command again in the session's workspace; the items keep their sequences, which `Points` and the rewinds name. `Capture` opens the copy on the embedded engine in a scratch home, with the session's rewinds made by then, a client that records the first request and ends the run, and subagent tool names known but not offered, and returns that request. `Strategies` are the strategies it compares, built from this package's functions; `Summaries` supplies summaries from the session's compaction log, a cache, a model (`Live`, only when asked), or `StubSummary`. `Run` reports the aggregates and how many recorded compactions the capture reproduced exactly. The hidden `uah compaction eval` prints them.

`evalrun/testdata/sessions` is a synthetic session recorded on the embedded engine with `fakellm` (`TestRecordFixtures -update`); `TestRun_Fixtures` holds the strategies to their bounds in `go test`.
<!-- /memoria:section -->

<!-- memoria:section id="meter" files="window.go" -->
## The context meter

`PercentLeft` is Codex's "N% context left": it treats 12,000 tokens as always in use (the system prompt and tools), so a fresh session shows 100%.

```text
effective = window − 12,000
left      = round(100 × max(effective − max(used − 12,000, 0), 0) / effective)
```

The TUI uses the last response's input plus output tokens as `used`. A compaction clears the meter until the next response.
<!-- /memoria:section -->

<!-- memoria:section id="failures" files="summary.go log.go" -->
## Failures

A failed compaction never loses the request: it goes out uncompacted, and the engine reports the failure (`engine.Compacted` with `Err`). The exception is a request estimated to be over the model's window, which the provider can only refuse: it stops the run with the failure instead.

| Failure | What happens |
| --- | --- |
| The provider rejects the summary call, or it times out | Reported; the request goes out uncompacted. |
| The model answers without text | Reported as a failure (`llmcall.ErrNoText`); Codex would store "(no summary available)" and drop the history, which loses more. |
| The summary input overflows the window | Trimmed and retried, as in [The summary call](#the-summary-call). |
| The provider answers a remote compaction without an item, or fails | The local summary runs instead, as in Codex. |
| The history of a remote compaction is estimated to be over the window | The local summary runs instead, with no remote call: the provider would refuse the history whole, and the summary trims it. |
| Automatic compaction fails three times in a row | Automatic compaction stops for the rest of the run; `/compact` still works. |
| An automatic compaction leaves the context above the limit | Reported as a warning (`engine.Compacted.Warning`); automatic compaction stops for the rest of the run. |
| `experimental_compact_prompt_file` is missing or empty | The session does not start, as in Codex. |
| The user interrupts during the summary | The call is canceled, reported as interrupted, and no request goes out. |
| A `PreCompact` hook blocks it | Reported as stopped; the request goes out uncompacted. |
| The history does not match the saved record | Reported once; the full history is estimated, and compacted again when it is over the limit, else sent. |
| A compaction failed, and the request is estimated to be over the window | The request does not go out; the run ends with the error, which names the estimate, the window, and the failure. |
| The log has an unreadable line | The line is skipped and logged. |
<!-- /memoria:section -->

<!-- memoria:section id="settings" files="settings.go" -->
## Settings

`Settings` is what the configuration sets; `internal/app` fills it from the keys and the engine reads it. The zero value never compacts automatically, never elides, keeps no calls, and never compacts remotely; `internal/app` sets uah's defaults.

| Field | Key | Default |
| --- | --- | --- |
| `Percent` | `auto_compact_percent` | 90 (0 turns automatic compaction off) |
| `TokenLimit` | `model_auto_compact_token_limit` (Codex's) | none |
| `Model`, `Effort` | `compact_model`, `compact_effort` | the session's current model and effort |
| `Prompt` | `compact_prompt` (Codex's), or the text of `experimental_compact_prompt_file` (Codex's) | Codex's `prompt.md` |
| `UserMessageMaxTokens` | `compact_user_message_max_tokens` | 20,000, at most a quarter of the window (64,000 for a remote compaction) |
| `Elision.AfterCalls` | `compact_elide_after_calls` | 10 (0 turns elision off); the big-output rule is 2,000 tokens after three calls |
| `KeepCalls` | `compact_keep_recent_calls` | 5 (0 is Codex's shape) |
| `Remote` | `remote_compaction` | true: on providers that have it (openai, openai-codex) |

`/context` shows the window above `Limit` as the auto-compaction buffer.
<!-- /memoria:section -->

<!-- memoria:section id="extension" files="summary.go compaction.go" -->
## Extension points

- **Another summary strategy.** A `Summarizer` takes the history as the model sees it and returns the summary and its usage. The engine's compactor uses `Summarize` with the configured model and prompt unless its `summarize` field is set, so another strategy plugs in there without touching the rewrite. Add it to `evalrun.Strategies` to measure it against the others.
- **An opaque compaction.** A provider item that the runner cannot represent goes in `Record.Remote`, with a transport that swaps the placeholder for it, as the remote compaction does.
- **Another rewrite.** `Record` and `Apply` are pure functions of the builder's items. A different rewrite keeps the same contract: a record covers a prefix of the history, and its shape guards it.
- **Hooks.** The engine's `BeforeCompact` callback runs as each compaction starts; the `PreCompact` hook attaches there.
<!-- /memoria:section -->
