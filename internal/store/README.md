<!-- memoria:section id="overview" files="store.go query.go" -->
# Session index

The store is a SQLite index of uagent's run records at `<state>/uah.db`. It makes session listing, full-text search, and activity counts fast without rereading every run summary. It can be deleted at any time; the next open rebuilds it from the files.

<!-- memoria:export id="summary" -->
A rebuildable SQLite index of the run records makes session listing, full-text search, and activity counts fast. The files stay the source of truth: the index reconciles against them on every open, and listing falls back to a file scan when the index cannot be used.
<!-- /memoria:export -->

The [state storage record](../../docs/design/state.md) explains why the files stay authoritative and how the index differs from the first design.
<!-- /memoria:section -->

<!-- memoria:section id="index" files="store.go query.go copy.go store_test.go" -->
## How it works

1. `Open` creates `uah.db` in WAL mode, so several uah processes can share it, and drops and recreates the tables when the schema version changed.
2. `Reconcile` lists `runs/` and compares each `summary.json`'s modification time and size with the indexed values. It reads only runs that are new or changed and deletes rows for runs that are gone. A summary that does not parse yet is read again next time.
3. Each run is one `runs` row (settings, status, timing, tokens, and the prompt). The tokens come from the summary's stats (`session.SummaryTokens`), which uagent's `stream.SummaryFromDTO` leaves out; schema version 1 recorded them as 0, so version 2 rebuilds such an index. The prompt and the answer also go into the `messages` FTS5 table.
4. Queries fold the rows into one `session.Info` per session, the same way `session.Sessions` folds the files, and add what the sidecar records (`ApplySidecar`): the source, the parent, the saved settings, and the last item's `last_sequence` and `last_activity`.

| Function | Used by |
| --- | --- |
| `List` | `uah sessions`, `uah resume`, the TUI picker, `--last`, shell completion. Falls back to the file scan |
| `Load` | A resumed TUI, an agent view, `uah sessions show`, and the TUI's prompt cache stats: `session.LoadRuns` of the session's runs (`runs_by_session`), so only their summaries are read. Falls back to `session.Load`'s file scan |
| `SearchIn` | `uah sessions --search`. Each word is quoted, so user text is never FTS syntax |
| `ActivityIn` | `/status`'s 12-week heatmap |
| `TokensIn` | `uah stats`: every run's start, model, workspace, and tokens, oldest first |
| `ForgetIn` | `uah sessions rm`: deletes the removed sessions' rows at once, which the next `Reconcile` would also do |

`CopyIndex` copies an index into another state directory for the one-time move to `~/.uah` (`internal/home/migrate`). SQLite writes a consistent snapshot with `VACUUM INTO`, so another uah may hold it open; the old directory is only read (opened immutable when no WAL exists, so no `-wal` or `-shm` file appears next to it). The copy's `dir` column is repointed at the new `runs/`.

No code writes to the index except `Reconcile` and `Forget`, which only deletes, so runs written by `uagent` or by another uah process appear on the next open. `TestIndexMatchesTheFiles` requires the index and the file scan to return the same sessions, and `TestLoadMatchesTheScan` the same runs of one session; `BenchmarkLoadSession` compares the two on 2,000 runs.
<!-- /memoria:section -->
