<!-- memoria:section id="overview" files="main.go perf/env.go" -->
# Performance harness

<!-- memoria:export id="summary" -->
`go run ./tools/perf` measures what uah costs outside the model: loading a session, the TUI's first frame and scrolling, an active turn, subagents and forks, the idle TUI, and leaks, on synthetic sessions of 100 to 10,000 records or on copies of real ones, against a scripted fake model. It prints a table, saves JSON, and compares a run with a baseline.
<!-- /memoria:export -->

The harness drives uah's real stack: `app.Setup` and `session.Open` as `uah` resumes a session, the embedded engine with its sandbox, and the TUI on `term`'s real loop and renderer, headless. The model is [`testing/fakellm`](../../testing/fakellm/fakellm.go), so no run needs a network, a login, or tokens. Every scenario runs in its own scratch home, and the process environment points `HOME`, `UAH_HOME`, `CODEX_HOME`, and the XDG directories there and clears the variables that change what uah does (`home.Variables`: the provider, model, endpoint, key, sandbox, approval policy, and the rest), so the harness never reads `~/.uah`, `~/.codex`, the user's configuration, or settings exported in the shell.

1. [Run it](#run-it)
2. [Scenarios](#scenarios)
3. [What a sample measures](#what-a-sample-measures)
4. [How fixtures are built](#how-fixtures-are-built)
5. [Real sessions](#real-sessions)
6. [Baseline](#baseline)
7. [The test](#the-test)

It is a command under `tools/` rather than a hidden `uah` subcommand, as `uah compaction eval` is, because it needs nothing of the user's: the fake model and the fixture builder are test code, and `go run` keeps them out of the shipped binary.
<!-- /memoria:section -->

<!-- memoria:section id="usage" files="main.go perf/run.go perf/report.go perf/compare.go" -->
## Run it

Run every command from the repository root.

```sh
go run ./tools/perf                         # every scenario, small, medium, and large: about 2 minutes on main
go run ./tools/perf -sizes small -run 'load|tui'   # a subset (the regular expression matches scenario names)
go run ./tools/perf -count 3                # each scenario three times; the report keeps the medians
go run ./tools/perf -baseline tools/perf/baseline.json   # run, then compare; exit status 3 on a regression
go run ./tools/perf -compare old.json new.json           # compare two saved reports without running
go run ./tools/perf -run 'turn/large' -cpuprofile -memprofile   # profiles of each scenario that runs
go run ./tools/perf -run leak -goroutines   # the stacks of the goroutines each scenario leaves behind
```

Each run prints a table and a line of each scenario's own measurements, and saves the report as JSON in `tools/perf/results/<time>.json` (`-out` names another file). `results/` is ignored by git. Profiles go next to the report in `<report>-profiles/` (`-profiles` names another folder): `<scenario>.cpu.pprof`, and `<scenario>.mem-before.pprof` and `<scenario>.mem.pprof`, whose difference is the scenario's allocations (`go tool pprof -base <scenario>.mem-before.pprof <scenario>.mem.pprof`).

A comparison lists each metric that moved by more than `-threshold` (default 0.25, that is 25%) and by more than the metric's floor, as `REGRESSION` or `better`; `-all` lists every metric. The floors keep noise on small numbers out: 30 ms for `cpu_ms` and `child_cpu_ms` (a small turn's CPU time moves between 80 and 125 ms from run to run of one commit), 5 ms for other times, 1 MB for sizes, 3 goroutines, 1 connection, 20,000 allocations, 200 wakeups. Metrics that depend on the scenarios before (`goroutines_before`, `goroutines_after`) or describe the run (`events`, `views`, `request_mb`, `server_ms`) are never compared.

Numbers vary with the machine and its load. Compare runs from one machine, with `-count 3` when the change is small.
<!-- /memoria:section -->

<!-- memoria:section id="scenarios" files="perf/scenarios.go perf/memory.go perf/tui.go perf/workload.go" -->
## Scenarios

Each scenario builds its session in a fresh scratch home, measures one block, and cleans up. Scenarios marked "per size" run on each size of `-sizes` and on each copied real session; the others run on the small fixture. The TUI scenarios run the TUI with `[tui] file_links = "peek"`, uah's default, so paths are drawn as links and the agent's messages are looked up for them.

| Scenario | Per size | Block | Its own measurements |
| --- | --- | --- | --- |
| `load` | yes | Resume the session as `uah resume` does (index already built) and send one message, to the first request and the end of the run | `open_ms`, `first_request_ms` (message to the request's arrival at the fake model), `session_syncs`, `history_ms` (`session.Load`, the TUI's transcript, outside the block), `index_ms` (building the SQLite index, outside the block) |
| `tui` | yes | Start the TUI on the session; wait for the first frame of the resumed transcript; page up 20 times, then down 20 times | `first_frame_ms` (the view after the TUI takes the opened session), `first_paint_ms` (the renderer's first write after it), `scroll_p50_ms`, `scroll_p95_ms`, `scroll_max_ms` (a key to its view, each key sent a frame after the last, as a key after a pause, which `term` draws at once), `view_*_ms` (the model's View), `term_kb` |
| `turn` | yes | Resume the session and run the workload turn headless; the session closes before goroutines and connections are counted | `turn_ms`, `first_request_ms`, `events`, `events_per_s`, `records_appended` (records the turn added to the session file), `session_syncs` (the syncs of session files), `requests`, `request_mb` |
| `spawn` | yes | Resume the session; the model spawns one child, waits, and finishes | `child_first_request_ms` (the parent's reply to the child's first request, once the fake model has read it; see `server_ms`) |
| `fork` | yes | The same with `fork_context`: the child copies the whole history | `child_first_request_ms` |
| `memory` | yes | Resume the session as the TUI does, run three turns that stream a 400-piece answer, and wait 2 seconds as an idle TUI would | `live_mb` (the live heap the open session kept, after a collection), `retained_mb` (the heap the process kept from the OS, before one), `os_mb` (the memory the system charges the process: its footprint on macOS, its resident set on Linux), each against the session just opened |
| `tui-turn` | no | The workload turn typed into the TUI, until the answer shows and the footer is idle | `turn_ms`, `views`, `view_*_ms`, `term_kb`, `term_writes` |
| `idle/tui` | no | The same TUI for 3 seconds after that turn | `updates_per_s` (0: the TUI's clock stopped), `views_per_s`, `cpu_ms_per_s`, `wakeups_per_s`, `term_bytes_per_s` |
| `agents` | no | One turn that spawns two children and forks one, waits for all, and finishes | `spawn_a_ms`, `spawn_b_ms`, `fork_ms`, `peak_goroutines`, `peak_conns` |
| `leak` | no | Open the session, run a turn with one command, close it; five times | `goroutines_left`, `conns_after` |

The workload turn ([workload.go](perf/workload.go)) is what one user turn of a coding session sends uah: four model responses with reasoning summaries and streamed commentary, six shell commands that read Go-like source (4 to 24 kB) and a 64 kB test log, one `apply_patch`, and a final answer in Markdown. The fake model answers at once, so a turn's time is uah's: the engine, the sandboxed commands, the session file, and the run record.

The TUI runs at 120×40 in true color. Its input never comes; the harness sends keys as messages, and a probe around the model counts each Update and times each View. The terminal counts the bytes and writes the renderer sends and drops them.
<!-- /memoria:section -->

<!-- memoria:section id="metrics" files="perf/metrics.go perf/sys_darwin.go perf/sys_linux.go perf/sys_other.go perf/report.go" -->
## What a sample measures

Every scenario reports the same columns for its block:

| Metric | Meaning |
| --- | --- |
| `wall_ms` | Wall time of the block |
| `cpu_ms`, `child_cpu_ms` | The process's user and system time (`getrusage`), and the commands' it waited for |
| `alloc_mb`, `allocs` | What the Go heap allocated (`runtime/metrics`) |
| `peak_heap_mb` | The most live heap, sampled every 2 ms |
| `goroutines_before`, `goroutines_after`, `goroutines_left` | Goroutines before the block and 0.5 s after its cleanup; `left` is the difference less one goroutine per open connection, which is the fake model's server, not uah's |
| `conns_after` | Connections to the fake model still open after the cleanup, idle ones included |
| `disk_written_mb` | Bytes the process wrote to disk: the kernel's count on macOS (`proc_pid_rusage`), `write_bytes` of `/proc/self/io` on Linux |
| `state_growth_mb` | How much the scratch home grew |
| `wakeups` | macOS: idle and interrupt wakeups; Linux: voluntary context switches |

`session_syncs` (the `turn` and `load` scenarios) counts the engine's syncs of session files in the block, from `embedded.SessionSyncs`; the engine syncs a group of records at once ([state.md](../../docs/design/state.md)), so it is lower than `records_appended`. Other `fsync` calls, such as the run record's, are not counted.

The fake model runs in the same process. Scenarios without subagents set `fakellm.Server.Light`, so it reads each request's bytes and parses nothing; the subagent scenarios need the parsed requests to route children, and `server_ms` reports the fake model's own time.
<!-- /memoria:section -->

<!-- memoria:section id="fixtures" files="perf/fixture.go perf/workload.go" -->
## How fixtures are built

A session of 10,000 records cannot be made by running turns: every model request carries the whole history, so it would take minutes. The harness records two real turns once per run, a seed and the workload turn, on the real stack, and a fixture repeats the workload turn's records:

1. The session file's items and operation records, split into tokens: text kept as is, and the values each copy changes.
2. Each copy gets fresh IDs (each UUID's fourth group is the copy's number; the session ID stays), fakellm's response and call IDs with the copy's number, sequence numbers moved past the previous copy, times 2 minutes apart ending now, and the scratch home's path.
3. The first turn of a copy follows the previous copy's last turn, so the turn chain is whole.
4. Each copy gets its run record (`events.jsonl`, `request.json`, `summary.json`, `stderr.log`) and the operations' output files; the recorded session's `$TMPDIR` is not an operation and is left out.

| Size | Records | Session file | Runs |
| --- | --- | --- | --- |
| small | 107 | 0.5 MB | 3 |
| medium | 1,994 | 9.6 MB | 40 |
| large | 10,001 | 48 MB | 197 |

One workload turn writes 51 records, and the seed 5. What uah reads back is what it wrote; only the IDs, times, and paths differ.

A size is a number of records, so a fixture holds as many workload turns as fit: when a turn's records change, so does the history of each size. A turn wrote 75 records until a shell command wrote two awaiting records instead of eight (ledger item 85), so since then small holds two turns, not one, and medium and large about 1.45 times as many. What grows with the history grew with them, with no change in cost per turn: the fork's copy (`disk_written_mb`, `state_growth_mb`), the peak heap of a spawn or fork, the allocations of the small subagent scenarios, and `index_ms`.
<!-- /memoria:section -->

<!-- memoria:section id="real" files="perf/real.go" -->
## Real sessions

`-real <uah home>` adds the largest sessions of that home (`-real-sessions`, default 3) to the per-size scenarios `load`, `tui`, and `turn`, as `real-1`, `real-2`, and so on. Each is copied into the scratch home: its session file, sidecar, operation outputs (not the commands' `$TMPDIR`, `operations/<id>/tmp`), and run records, with the home's path rewritten. The harness never writes to that home and reads nothing else from it: no configuration, credentials, or index.

A session with an operation that never finished is skipped: resuming it would carry the operation on, and its recorded paths can point outside the copy. The turn on a real session answers with text only, since its workspace is not here. A recorded `SkillUse` call needs its tool to restore, so the scratch workspace has one stub skill.
<!-- /memoria:section -->

<!-- memoria:section id="baseline" files="baseline.json" -->
## Baseline

[baseline.json](baseline.json) is the report of `go run ./tools/perf -count 3` at 1eaf7e6 (lane tuicore on main after 1.8.5: the TUI on uah's own terminal layer, `internal/tui/term`, instead of Bubble Tea), the medians of three runs on an Apple M4 Max (14 cores), macOS 27.2, Go 1.27.1, in the workspace-write sandbox. Compare a change with it on a similar machine: `go run ./tools/perf -baseline tools/perf/baseline.json`. Replace it, with a new commit and this paragraph, when a change moves the numbers on purpose.

| Scenario | Wall ms | CPU ms | Alloc MB | Peak heap MB | Goroutines left | Conns after | Its own |
| --- | --- | --- | --- | --- | --- | --- | --- |
| `load/small` | 17.4 | 14.7 | 10.0 | 8.06 | 0 | 0 | first_request_ms 7.13 |
| `load/medium` | 83.0 | 93.8 | 112 | 40.9 | 0 | 0 | first_request_ms 57.7 |
| `load/large` | 343 | 380 | 544 | 167 | 0 | 0 | first_request_ms 266 |
| `tui/small` | 1,395 | 64.3 | 4.39 | 6.71 | 0 | 0 | first_frame_ms 34.7, first_paint_ms 34.7, scroll_p95_ms 0.73 |
| `tui/medium` | 1,431 | 87.9 | 14.3 | 10.8 | 0 | 0 | first_frame_ms 33.9, first_paint_ms 34.0, scroll_p95_ms 1.49 |
| `tui/large` | 1,489 | 174 | 55.0 | 16.9 | 0 | 0 | first_frame_ms 86.4, first_paint_ms 86.4, scroll_p95_ms 1.47 |
| `turn/small` | 371 | 62.5 | 19.5 | 12.0 | -1 | 0 | turn_ms 371, records_appended 51 |
| `turn/medium` | 437 | 169 | 170 | 48.2 | -1 | 0 | turn_ms 437, records_appended 51 |
| `turn/large` | 769 | 531 | 807 | 238 | -1 | 0 | turn_ms 764, records_appended 51 |
| `spawn/small` | 64.5 | 41.1 | 18.7 | 13.1 | -1 | 0 | child_first_request_ms 31.4 |
| `spawn/medium` | 189 | 200 | 196 | 88.6 | -1 | 0 | child_first_request_ms 27.1 |
| `spawn/large` | 792 | 853 | 948 | 384 | -1 | 0 | child_first_request_ms 28.5 |
| `fork/small` | 65.6 | 41.3 | 28.9 | 18.3 | -1 | 0 | child_first_request_ms 32.2, disk_written_mb 0.50 |
| `fork/medium` | 275 | 316 | 381 | 120 | -1 | 0 | child_first_request_ms 119, disk_written_mb 5.03 |
| `fork/large` | 1,171 | 1,374 | 1,925 | 506 | -1 | 0 | child_first_request_ms 514, disk_written_mb 24.2 |
| `memory/small` | 2,079 | 158 | 36.6 | 13.2 | 0 | 0 | live_mb 0.31, retained_mb 0.97 |
| `memory/medium` | 2,260 | 371 | 345 | 52.7 | 0 | 0 | live_mb 3.95, retained_mb 8.88 |
| `memory/large` | 3,068 | 1,248 | 1,653 | 255 | 0 | 0 | live_mb 19.3, retained_mb 35.5 |
| `tui-turn/small` | 412 | 84.9 | 19.9 | 13.3 | 2 | 1 | view_p95_ms 0.91 |
| `idle/tui` | 3,001 | 0.15 | 0 | 0 | -1 | 1 | cpu_ms_per_s 0.05, updates_per_s 0, wakeups_per_s 2.67 |
| `agents/small` | 119 | 81.6 | 37.8 | 20.8 | -1 | 0 | fork_ms 69.1 |
| `leak/5-runs` | 508 | 139 | 60.8 | 15.1 | 0 | 0 | goroutines_left 0 |

Against the baseline before it (1eaf98d, on Bubble Tea), the idle TUI wakes 2.67 times a second instead of 165 and uses 0.05 ms of CPU a second instead of 4.78: `term` draws only after a change and has no ticker. The `tui` scenarios changed how they measure, so their wall time, CPU time, and wakeups are not comparable: each scroll key is now sent a frame (33 ms) after the last, as a key after a pause, which `term` draws at once, so a scenario lasts about 1.4 s instead of 45 to 110 ms, and its CPU and wakeups include the resumed session's own background work over that time (a TUI that only sleeps through the same 1.4 s costs as much). `scroll_p95_ms` (0.7 to 1.5) is a key to its drawn frame now, where with Bubble Tea it was a key to the model's view (0.4), before a paint that came on the next tick; `first_frame_ms` (34) is the first drawn frame of the opened session, which waits for the frame budget after the startup frame, as Bubble Tea's first paint waited for its tick (`first_paint_ms` 34 on both). `term_kb` counts every scroll step's frame, where Bubble Tea painted the whole burst of steps once or twice. The other scenarios moved by main's changes since 1eaf98d, and by noise.

Against 1eaf6a4, the baseline before 1eaf98d, the runner fork cuts what the history costs by a third to two thirds: turn/large allocates 857 MB instead of 2,582 and takes 858 ms instead of 1,270, load/large's first request comes in 327 ms instead of 462, and fork/large allocates 1,979 MB instead of 3,630. Peak heap is up 10 to 20% on medium and large (turn/large 268 MB instead of 240) because the runner keeps the last request's item encodings between requests, about one request body. `child_cpu_ms` of turn/large (139 instead of 97) is the sandboxed commands' time, which no change here touched.

Against 1eafd1f, the baseline before 1eaf6a4 (with the fork rows after ledger item 84 and the TUI-turn and idle rows after item 86), a turn takes 360 ms instead of 590 on the small fixture and writes 51 records instead of 75, a load's first request comes in 7 ms instead of 27, the large TUI allocates 58 MB instead of 403, and the subagent scenarios take half the time. Three changes there are not regressions:

- The fixtures hold more turns (see [How fixtures are built](#how-fixtures-are-built)): fork/large writes 23.9 MB instead of 16.5 and peaks at 471 MB instead of 357, and small spawns and forks allocate about a third more. Per workload turn, the fork's copy is the same or smaller (0.13 MB on medium).
- `first_paint_ms` on the small TUI is 33.8 instead of 17.1. The renderer writes on its frame ticker only, which starts with the program: the first write comes at the first tick after the first frame, at 33 ms at 30 frames a second (17 ms at 60). The first frame comes at 4 ms; Bubble Tea has no way to write a frame before its tick, and its ticker cannot pause, so the first paint waits one frame at most.
- `turn` goroutines and connections left are now -1 and 0 (2 and 1 before): the scenario counted them with its session still open, so they were the session's own goroutines and its model connection. It closes the session first now, as `leak` does. `tui-turn` keeps its TUI open for `idle/tui`, so it still counts the open session's.

`child_cpu_ms` of `tui-turn/small` (the sandboxed commands' CPU time) moves between 130 and 210 ms from run to run of one commit, so a change from the 107 before is noise. Before 1eafd1f, on 1eaf678: load/large first request 3,308 ms, turn/large 4,050 ms with 5.9 GB allocated, fork/large first child request 23,293 ms, and two goroutines and one connection left per closed session. A fork took 33 s on the large fixture before item 84: the child ran the parent's shell operations again.
<!-- /memoria:section -->

<!-- memoria:section id="test" files="perf/perf_test.go perf/race_test.go perf/norace_test.go perf/fork_internal_test.go" -->
## The test

`go test ./tools/perf/...` runs every scenario on the small fixture once, about 16 seconds, and checks generous ceilings: about ten times the baseline. Under the race detector it skips, since the detector would only blur the ceilings; CI runs it in a step without it. The tight ceilings are `idle/tui` `wakeups_per_s` below 10 (2 to 4 on macOS, the Go runtime's), which fails if the TUI wakes up while idle again, as Bubble Tea's frame ticker did (about 165 at 30 frames a second); `memory/small` `live_mb` below 0.6 (about 0.2), which fails if the open session keeps its last run in memory (about 1 MB); and `memory/small` `retained_mb` below 4 (about 0.3), which fails if the engine stops returning the free heap after its runs (about 8 MB). It catches a large regression, such as the TUI's clock running while idle, a turn that allocates ten times as much, or goroutines left by every session, without failing on a slow machine. `go test -short` skips it. `TestForkRerun` forks a session whose parent appended to a file with a command and checks that the file still has one line: a fork's first run must not start the parent's work again.
<!-- /memoria:section -->
