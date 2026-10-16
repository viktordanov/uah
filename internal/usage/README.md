<!-- memoria:section id="overview" files="usage.go payload.go fetch.go reader.go" -->
# Subscription usage

The usage package reads the rate limits of the ChatGPT subscription behind the openai-codex provider: each window (a week, or 5 hours and a week), the percent used, and when it resets. `uah usage`, the TUI's `/status`, footer, and warnings, and `uah doctor` show it; the [usage design record](../../docs/design/usage.md) has the research and the decisions.

<!-- memoria:export id="summary" -->
The usage package reads the ChatGPT subscription's rate limits for the openai-codex provider, as Codex does: one read-only GET to the ChatGPT backend's usage endpoint with the login's credentials and uah's own identity. A reader caches the answer for 60 seconds and sends one request at a time; it reads on demand and after each run, never on a timer. Other providers have no usage.
<!-- /memoria:export -->

1. [The reader](#the-reader)
2. [Reading usage](#reading-usage)
3. [Rows, warnings, and the limit](#rows-warnings-and-the-limit)
4. [Headers and errors](#headers-and-errors)
5. [Session prompt cache](#session-prompt-cache)
6. [Tests and the probe](#tests-and-the-probe)

The `cachestats` package accounts for what a session spends on its prompt cache: per request, the input the provider could have served from the cache, what it served, and why the rest missed. `/usage`, `/status`, and `uah sessions show` show it.

The facts about Codex were checked against Codex `rust-v0.156.1`, and the facts about the runner against unreal-agent v0.1.1.
<!-- /memoria:section -->

<!-- memoria:section id="reader" files="reader.go" -->
## The reader

`Reader.Usage(ctx, maxAge)` returns the latest snapshot, reading the backend when the cached one is older than `maxAge`. `For(provider, opts)` returns the reader for a provider: a `CodexReader` for openai-codex, and for any other provider one whose error wraps `ErrUnsupported` and names it ("usage is not available for ollama").

| Property | How |
| --- | --- |
| One request at a time | A read holds a one-slot semaphore; a caller that waited gets the snapshot of the read that finished meanwhile, even with max age 0. Waiting ends with the context |
| Cache | The last snapshot stays in memory. The TUI's read after a run uses `CacheFor` (60 s); `uah usage`, `/status`, `uah doctor`, and the limit notice use 0, which always reads |
| Credentials | Read on each read with `codexauth.Login.Creds`, as the engine reads them, because Codex and uah refresh the login file. After a 401, the reader renews the login once (`Login.Renew`) and reads again; a login that cannot be refreshed is `ErrUnauthorized` |
| Errors | The last snapshot comes back with the error, so `/status` can show it as stale |

Only `internal/app` builds a reader: `app.NewUsage` for the settings' provider and base URL, once per session in `app.Setup` (`Result.Usage`), next to the model catalog. `cmd/uah` passes it to the TUI in `bubble.Deps.Usage`, and `app.Doctor` and `app.ReadUsage` build their own. The TUI's state and renderer use the types and the formatting in this package, never `Fetch`.
<!-- /memoria:section -->

<!-- memoria:section id="usage" files="usage.go payload.go fetch.go" -->
## Reading usage

`Fetch(ctx, creds, opts)` sends one GET to `https://chatgpt.com/backend-api/wham/usage`, the endpoint that Codex's `/status` reads. The credentials are `codexauth.Creds`. The request sends the same headers that the engine sends to `/responses`: the bearer token, `ChatGPT-Account-ID`, and `originator` and `User-Agent` set to `uah-core`, not Codex's user agent. The client does not follow redirects, and an error never contains the token or the response body.

`URL` chooses the path as Codex does: `/wham/usage` under `/backend-api`, else `/api/codex/usage`. It accepts only the runner's base URL or a loopback test server, so the token cannot go to another host. `--base-url` (`UAH_LLM_BASE_URL`) sets it, as for the model list.

`Parse` turns the body into a `Snapshot`:

| Field | Meaning |
| --- | --- |
| `Plan` | The ChatGPT plan, such as `plus` or `pro` |
| `Limits` | The ordinary limit (`codex`) first, then any per-model limits |
| `Limit.Primary`, `Limit.Secondary` | The two windows; either one can be missing |
| `Window.UsedPercent`, `Minutes`, `ResetsAt` | Percent used, window length, reset time |
| `Credits` | Extra-usage credits, when the backend sends them |
| `ReachedType` | Why a limit is reached, when one is |

A window's name comes from its length, not its position: `Label` returns `5h`, `daily`, `weekly`, `monthly`, or `annual` as Codex does. This is necessary because a Pro login returned only a weekly window, and the backend sent it as the primary window. `ResetLabel` gives Codex's reset time, `15:44` today or `15:44 on 26 Sep`. `Stale` applies Codex's 15-minute limit.
<!-- /memoria:section -->

<!-- memoria:section id="rows" files="rows.go limit.go" -->
## Rows, warnings, and the limit

`Snapshot.Rows` lists every window as a `Row` with its label (an additional limit's name first), and every place that shows usage draws from it:

| Helper | Gives | Used by |
| --- | --- | --- |
| `Row.Left`, `Row.Text` | `78% left`, `weekly 78% left (resets 15:44 on 26 Sep)` | The footer, `uah doctor` |
| `Bar` | A 20-cell bar of the percent left, as Codex's status card | `uah usage`, `/status` |
| `Snapshot.Tightest` | The ordinary limit's window with the least left | The footer, `uah doctor` |
| `Window.Crossed` | The highest of `WarnAt` (75, 90, 95% used) the window reached | The TUI's warnings |
| `Snapshot.Reached`, `BlockedUntil` | Whether a limit is used up, and when the usage frees up | `uah usage`, `uah doctor`, the limit notice |

`LimitReachedIn` reads a run's failure text. The runner reports a failed model call as text only, such as `responses API request failed with status 429: …` or `responses API error usage_limit_reached: …`, and drops the body's `resets_at`. The function accepts `usage_limit_reached`, or 429 with "usage limit", and takes the reset time from the text when the body survived in it; otherwise the TUI reads the usage to say when to try again.
<!-- /memoria:section -->

<!-- memoria:section id="calls" files="headers.go transport.go" -->
## Headers and errors

The backend also sends the rate limits as headers on each `/responses` call (`x-codex-primary-used-percent`, `-window-minutes`, `-reset-at`, the same for `secondary`, and `x-codex-credits-*`). `ParseHeaders` reads them, including extra limit families such as `x-codex-other-primary-used-percent`. `Transport` is an `http.RoundTripper` that calls `Observe` with each response's snapshot. Nothing uses it yet: it can be used only by a client whose `*http.Client` uah builds, because the runner's openai-codex client builds its own client (option B in the design).

`ParseLimitReached` reads the body of a 429 whose error type is `usage_limit_reached`, which gives the plan and the reset time.
<!-- /memoria:section -->

<!-- memoria:section id="cache" files="cachestats/cache.go cachestats/attribute.go cachestats/summary.go" -->
## Session prompt cache

OpenAI keeps a prompt cache per model and per reasoning effort, and drops it after the cache is idle for some time. A session pays for input it sent before when adaptive effort switches the effort, when the user switches the model or pauses, and when a compaction rewrites the history. The [cost study](../../docs/design/adaptive-effort-costs.md) measures the effort switches in the benchmark. `cachestats` measures all of these causes in real sessions.

`session.CacheRequests` (in `internal/session`) reads a session's model requests from what its runs recorded. So it works for every past session, and it needs no new record:

| Field | From |
| --- | --- |
| Input, cached input, output; start and end | Each `model_response` and the `turn` before it, in the run's events |
| Model | The run's request |
| Effort | The request's `model_attempt` line in the run's `stderr.log`: its `request_effort`, the effort the request carried, which an effort update (a `configuration_update` item) leaves at the session's base, else its `effort` (with adaptive effort, or after the backend rejected the updates, the request's own effort); in a run from before uah logged it, the run's latest settings |
| Rewritten | A compaction that succeeded, or a rewind, since the request before |
| Opener | The first request after a user's message |

`Cache` is the cache model, which agentbench's `-turns` replay also uses. Each key (model and effort) keeps the longest prompt sent at it. A request finds cached the part of its input that this prompt covers, in whole blocks of 128 tokens. A rewritten history leaves every key only the base, which is the first request's cached input (the instructions and tools that sessions share).

`Attribute(requests, TTL)` gives each request `Expected` (what its own key holds), `Reachable` (what any key holds, so the input past it is new and is not a miss), the gaps since the request before and since the last request at the same key, and its misses by cause:

| Cause | Rule |
| --- | --- |
| `cold` | The session's first request: all its uncached input |
| `compaction` | The first request after a compaction or a rewind: all its uncached input |
| `idle` | A request that came more than `TTL` after the request before it: all its missed input |
| `effort` | The part sent before only at another request effort; an effort update keeps the request's effort, so it causes none. Also the rest of the miss when this effort's cache was unused for more than `TTL` while the other effort was in use |
| `model` | As `effort`, when the request before it went to another model. A model shares no cache with another model, so all of the miss is `model` |
| `other` | The rest: the provider evicted the prefix or did not route to it. A request just after the first one of a session often misses all of it |

A miss of at most `NoiseFloor` (1,024 tokens) does not count. `TTL` is 30 minutes. This value comes from the owner's sessions (agentbench `-cache-sessions`): after a pause of up to 30 minutes, the cache served about 90% of the messages at the same effort, as after short pauses; after 30 minutes to 3 hours, 67%; after more than 3 hours, none.

`Summarize` totals the requests: the cached share, the misses by cause in the order of `Causes`, and `MissedShare`, which is what the missed input cost (the uncached price less the cached price) as a share of the session's usage at `APIPrice`. `APIPrice` is \$1.25, \$0.125, and \$10 per million uncached, cached, and output tokens, as in the cost study. The subscription's weighting is not published, so the share is an estimate. `Summary.Line` is the line that the TUI and `uah sessions show` print:

```text
cache 86% · missed 119k: effort switches 72k, cold start 9k, other 38k · ≈20% of usage (API-price estimate)
```

`uah sessions show --json` adds `cache`: the summary (`requests`, `input`, `cached`, `output`, `missed` by cause, `missed_share`, `idle_gaps`, `longest_idle_gap_ms`) and `by_request`, each request with its model, effort, tokens, `expected`, `gap_ms`, and `misses`.
<!-- /memoria:section -->

<!-- memoria:section id="tokstats" files="tokstats/collect.go tokstats/stats.go" -->
## Token statistics

`tokstats` adds up the tokens of every session in a state directory for `uah stats` ([design](../../docs/design/token-stats.md)). Unlike the rest of this package it reads no backend: only what uah recorded.

- **Collect** reads each finished run's tokens from the session index (`store.TokensIn`: start, model, workspace, `core.Tokens`), with the session's source from its sidecar (`tui`, `exec` for `uah exec`, `subagent`, `review` for a /review's reviewer, `unknown` without one), and each compaction call's from the sessions' `compaction.jsonl` (source `compaction`, not a run), since no run's totals hold them. A compaction that made no call (elide, clear) has none. A forked subagent's log starts with copies of its parent's records, so a call counts once (by its hash and time), as the parent's: the logs are read by depth in the sidecars' parents, so a session's parents come before it (a subagent ID without a sidecar's parent counts as one level down). A sidecar without a workspace (from before uah kept it) takes the workspace of the session's runs.
- **Build** keeps the entries in the range (`Since`, `Until` excluded) and, with `Workspace`, one workspace's, compared as `session.NormalDir` makes them. It groups the rows by day, ISO week, or month, every one from the range's first day (`Since`, or the first entry's) to its last (`Until`, or today) with an empty one too, or by model, workspace, or source, the largest total first. A row has the runs, the sessions with any entry, and the tokens; `Total` is Codex's, input not served from the cache plus output, since cached input is part of input and reasoning part of output.
- **Days** are the range's days in `Now`'s location, stepped at local noon, since where a clock change skips midnight, midnight plus a day can land on the same date: the active ones (with any entry), their totals' mean, median, nearest-rank 90th percentile, and maximum with its day, and the streak of active days up to today, or up to yesterday while today has none yet. A run counts on the day it started. The report also has the total per run and per session and each model's share.
<!-- /memoria:section -->

<!-- memoria:section id="development" files="parse_test.go headers_test.go fetch_test.go reader_test.go rows_test.go probe_test.go testdata/pro_weekly_only.json testdata/plus_two_windows.json cachestats/attribute_test.go tokstats/stats_test.go tokstats/collect_test.go" -->
## Tests and the probe

`testdata/pro_weekly_only.json` is the response that a Pro login received on 2026-09-24, with the identifiers replaced. `plus_two_windows.json` is synthetic and has two windows, an additional limit, and a reached limit. The header tests use the header names that a real `/responses` call returned. `Fetch` is tested against a loopback server, for the headers, the path, and errors that contain no secrets. The reader is tested against a loopback server with a test clock: the cache, one request for many callers, the context, errors that keep the last snapshot, and unsupported providers. `uah usage`, `uah doctor`, and the TUI are tested against loopback servers too; no test calls the real backend.

`cachestats/attribute_test.go` attributes synthetic request sequences: one effort (new content is no miss), an effort switch each way, a pause past the TTL and one within it, an effort whose cache expired while the other served, a model change, a compaction, and misses under the noise floor. It also checks the summary line and the share at API prices. `internal/session/cachestats_test.go` reads requests from run events and a `stderr.log` with attempt lines.

`tokstats/stats_test.go` builds reports from synthetic entries: days with an empty one, the range with `Until` excluded, weeks from Monday, the groupings' order, a workspace, the days' statistics and the streak, the models' shares, and the dates across Santiago's midnight clock change. `tokstats/collect_test.go` collects a real run record, its sidecar's source, a compaction log with a call and an elision, a fork's copy of it counted once, and a workspace from the runs when the sidecar has none.

`probe_test.go` needs the `probe` build tag and a Codex login. It sends one real GET to the usage endpoint and makes one small model call through `Transport`:

```sh
go test -tags probe -run TestProbe -v ./internal/usage/
```
<!-- /memoria:section -->
