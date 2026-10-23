# Token statistics: `uah stats`

Status: scoped 2026-10-16 and done in v1.13.0; phase 2 is in [Gaps](#gaps). Ledger item 147. TokScale facts are from its README ([junhoyeo/tokscale](https://github.com/junhoyeo/tokscale), October 2026).

A command that adds up the tokens of every uah session by day, week, month, model, workspace, or source, with a few descriptive statistics, for tracking use over time. It prints tables and JSON; it is not a TUI.

1. [Why](#why)
2. [What uah records](#what-uah-records)
3. [TokScale](#tokscale)
4. [The command](#the-command)
5. [Data](#data)
6. [Gaps](#gaps)
7. [Decisions](#decisions)
8. [Out of scope](#out-of-scope)

## Why

- **Nothing adds tokens up across sessions.** The session index already holds every finished run's tokens, but the only view across sessions is the `/status` heatmap, which counts runs per day. `/status` totals, the exit summary, and `uah sessions show` cover one session.
- **External trackers do not see uah.** TokScale and its kind read each client's own logs, such as Codex's `~/.codex/sessions`. uah writes its own (`~/.uah/runs`), so its use is missing from them.

## What uah records

- **Per run, in the index** (`<state dir>/uah.db`, `internal/store/store.go`): the `runs` table has `started_ns`, `wall_ns`, `status`, `provider`, `model`, `effort`, `workspace`, and `tokens`, the run's `core.Tokens` as JSON (input, cached input, cache-write input, output, reasoning). `Reconcile` keeps it current from each run's `summary.json` when the index opens. Every run directory is indexed: all workspaces, `uah exec`, subagents, and `/review` reviewers.
- **Per model response** (`runs/<id>/events.jsonl`): `model_response` items with their `RecordedAt` time and `Usage`; the model and provider are the run's.
- **Per session** (`sessions/<id>.uah.json`): the source (`tui`, `run`, `subagent`), the parent, whether it is a reviewer, and the workspace.
- **Compaction calls** (`sessions/<id>.compaction.jsonl`): each record's `stats.call` has the call's tokens. They are side calls, so they are not in any run's totals.
- **Counting:** input includes cached and cache-write input; output includes reasoning. Codex's total, which the exit summary prints, is `input - cached + output`.
- **Scale** (the owner's state dir, October 2026): 1,602 runs and about 12,000 model responses; `events.jsonl` files total about 450 MB, the index 12 MB.

## TokScale

TokScale reads local logs of many clients (Claude Code, Codex, OpenCode, Gemini, and others) and shows a TUI, tables (`--light`), and JSON (`--json`).

- **Date filters:** `--today`, `--yesterday`, `--week`, `--month`, `--year`, `--since`, `--until` (inclusive).
- **Grouping:** `--group-by model`, `client,model`, `workspace,model`, `session,model`, and more.
- **Buckets:** input, output, cache read, cache write, reasoning; rows carry `messageCount` and `cost`.
- **Cost:** LiteLLM prices, cached for an hour, with overrides in `custom-pricing.json`.
- **Time:** local days, with the time zone pinned on first use so day boundaries stay put.
- **Dedupe:** archive copies of a Codex rollout count once.
- **Also:** a contribution graph, a leaderboard (`submit`), and plan quotas (`usage`).

Taken: the flags' shape (`--since`, `--until`, grouping, `--json`), the token buckets, the per-row counts, and local-time days. Left: the TUI, the graph, the leaderboard, other clients, and live prices.

## The command

```
uah stats                       # the last 7 days by day, their sum, then all time
uah stats --by day|week|month|model|workspace|source
uah stats --since 2026-10-01 --until 2026-10-15
uah stats -C .                  # one workspace
uah stats --json
```

- **Range:** `--since` and `--until` take a date (`2026-10-01`, local midnight) or an RFC 3339 time; `--until` is inclusive. All time by default; with no `--by`, `--since`, or `--until`, the rows are the last 7 days, with all time's sum under theirs and the statistics of all time. Time rows include the empty days, weeks, or months.
- **Workspaces:** all by default; `-C` (or `--workspace`) limits to one, as `uah sessions` does. `--by workspace` and the summary's workspaces group the paths as `-C` matches them, absolute with symlinks resolved, so `/tmp/x` and `/private/tmp/x` are one row.
- **Columns per row:** runs, sessions, input, cached, output, reasoning, total, and the cache hit rate (cached ÷ input). Cached and reasoning are shown as parts, never added again. Model responses are not a column: the index has no count of them, and adding one changes its schema, which would rebuild it whenever an older uah opens it.
- **Statistics over the range,** under the table:
  - active days of the days in the range;
  - tokens per active day: mean, median, p90, and maximum, with the peak day;
  - the current streak of active days;
  - mean tokens per run and per session;
  - each model's share of the total.
- **Time:** local days; weeks start on Monday (ISO weeks); months are calendar months.
- **Output** (`cmd/uah/statsview.go`, after the owner asked for a friendlier summary): a headline with the range's tokens, runs, sessions, and cache share, how it compares with the range of the same length before it, and the busiest day; the rows with dates as people read them (`Mon Oct 12`, `Oct 12 – 18`, `October 2026`), numbers aligned right, a bar of each total against the largest, and today, this week, or this month marked; then a labelled summary, with the largest models and workspaces (by their last path element, the temporary directories together). `--wide` adds the four token buckets and a note on what total counts; `--heatmap` shows the tokens per day as a grid of weeks (`state.HeatGrid`, which `/status`'s heatmap of runs draws too) in place of the table. The `table` in `cmd/uah/style.go` keeps colors off pipes. `--json` prints one object: the range, the rows, and the statistics, with snake_case keys.

## Data

- **Runs:** `store.TokensIn`, next to `Activity`, returns each run's start, model, provider, workspace, session ID, and tokens; `internal/usage/tokstats` adds them up in Go. No schema change.
- **Source:** from the sessions' sidecars (`session.ApplySidecar`): `tui`, `exec`, `subagent`, `review`.
- **Compaction:** read from the `compaction.jsonl` files (few and small) and counted as their own source, `compaction`.

## Gaps

- **A run that crosses midnight** counts on the day it started. Exact days need each response's time: phase 2 keeps a per-day token table in the index, filled from `events.jsonl` when `Reconcile` reads a changed run.
- **Auto-review** tokens reach only the TUI (`engine.AutoReviewed`) and are not saved, so they are missing. Phase 2 saves them with the run.
- **A run still in progress** has no `summary.json` and is counted once it ends.

## Decisions

Open, with a recommendation each:

1. **The name:** `uah stats`. `uah usage` is the ChatGPT plan's rate-limit windows, a different thing.
2. **Cost:** left out. On a ChatGPT plan a price is notional, and uah has one placeholder price (`cachestats.APIPrice`). Later, a `--price input,cached,output` flag in dollars per million tokens, as `tools/agentbench` takes.
3. **The default range:** every workspace, unlike `uah sessions`.
4. **TokScale:** later, a uah client in TokScale, reading `uah stats --json` or the run files.

## Out of scope

A TUI or a graph beyond the heatmap, live prices, other clients' logs, a leaderboard, and a pinned time zone.
