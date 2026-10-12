# Background shells

Status: planned and built 2026-10-12 (see [As built](#as-built)). Ledger item 142. Codex facts are from openai/codex at a9b0815 (2026-10-11; `C/` is `codex-rs/`); Claude Code facts are from its public documentation and its tool descriptions.

uah's `Bash` never blocks a turn, but a command cannot outlive the run that started it, and nothing tells the model when a process it started itself ends. This plan gives the model shells that run in the background, a notice when one exits, and a way to stop one, delivered the way a subagent's `<subagent_notification>` is today.

1. [What uah does today](#what-uah-does-today)
2. [What the owner's sessions show](#what-the-owners-sessions-show)
3. [How Codex and Claude Code do it](#how-codex-and-claude-code-do-it)
4. [Plan](#plan)
5. [Not planned](#not-planned)
6. [Open decisions](#open-decisions)
7. [As built](#as-built)

## What uah does today

- **One tool.** The main agent's `Bash` takes `command` and `max_output_length` (uah-core `harness/tool/static.go`). It is asynchronous and has no time limit. The reviewer's `exec_command` stops after 10 seconds and has no `write_stdin` ([sandbox design](sandbox.md)).
- **The wake hold.** A turn's results wait until every call in it ends. After 5 minutes (`wakeHold`, `internal/engine/embedded/wake.go`), the model gets "Still running after 5 minutes. The call continues in the background, and its result arrives in a later turn.", the last 4,000 bytes of each stream, and `kill -- -N` for the process group. The real result replaces that placeholder later in the same run.
- **The heartbeat.** While the run waits only on tool calls, the model is woken every 10 minutes with the calls still running.
- **A pending call keeps the run alive.** The run cannot end while a call is pending, and a hard stop or a resume ends the process ("shell execution was interrupted before an exit status was recorded").
- **No polling.** The model is not woken just to hear that a call is still running ([ledger](../ledger.md) item 91, [engine README](../internal/engine/README.md#the-wake-policy), [agent tuning](agent-tuning.md)). A `background` argument to `Bash` was part of that experiment and was removed with the wake variants it was tested beside.
- **Stopping a process from inside the sandbox.** A sandboxed command cannot signal another, so uah runs a kill aimed at the session's own process groups outside the sandbox (`ownKill`, `internal/engine/embedded/commands.go`; `killOwn`, `sandboxtool.go`).

## What the owner's sessions show

7,103 `Bash` calls in 231 sessions, 2026-09-23 to 2026-10-12:

- **Most commands are short.** p50 0.2 s, p90 4.8 s, p99 50 s. The slowest 1% (71 calls) took 59% of all shell time.
- **The long ones are hangs.** An offline `pnpm install` ran for 75 minutes until the owner interrupted it; `make acceptance` ran for 27 and 13 minutes; a docker smoke test ran for 20.
- **The model does not sleep on its own.** Of 24 `sleep` calls, about 3 came from the model; the rest were the owner's tests.
- **The model already detaches processes, and that works.** `subprocess.Popen(..., start_new_session=True)` 25 times in 15 sessions (mostly dev servers and workers logging to a file), `nohup … &` over ssh, `docker compose up -d`, tmux.
- **Watching a detached process is where the requests go.** In one session the model started 1,470 tasks in tmux and then ran a 50-second watch script 48 times in a row over 50 minutes. Health checks on minima were `curl` loops of 1 to 3 minutes.
- **Stopping one is done by hand.** 26 kill commands in 12 sessions, with PIDs found through `lsof`, `ps`, and pid files; 16 of 51 `ps`, `pgrep`, and `lsof` calls failed in the sandbox.
- **Input while a command runs is rare.** About 20 calls to a PTY tool from an MCP server, and a few answers piped in with `printf`.

## How Codex and Claude Code do it

| | Codex (unified exec) | Claude Code |
| --- | --- | --- |
| Start | `exec_command` waits `yield_time_ms` (10 s by default, 30 s at most); a process still alive returns a `session_id` and keeps running | `Bash` with `run_in_background` returns a task ID and an output file at once; a foreground command at its timeout (2 minutes by default) moves to the background instead of stopping |
| Output | `write_stdin` with empty `chars` returns what the process printed since the last read | `Read` on the output file |
| Input | `write_stdin` `chars`, with a PTY when the call asked for `tty` | None |
| Stop | The model writes ctrl+c (`\u0003`); the user's `/stop` stops them all | `TaskStop`; the user stops one in `/tasks` |
| Completion | The model polls: the exit watcher (`C/core/src/unified_exec/async_watcher.rs`) tells only the UI | The harness tells the model when the command exits; `Monitor` turns output lines into notices |
| Limits | 64 processes, oldest exited ones pruned first; 1 MiB of output kept, head and tail | A time limit only in unattended sessions; 5 GB of output |
| Resume | Not restored | Not restored |

## Plan

### Starting one

- **`Bash` gets `background`.** `background: true` starts the command and returns at once with a shell ID (`shell-1`, `shell-2`, … in the session), its log path, and its process group. The description says when to use it: servers, watchers, and builds or jobs the model wants to keep working beside, not commands it waits on.
- **A long call moves to the background.** When the 5-minute wake hold ends, a call still running becomes a background shell instead of a pending call: its placeholder says so and gives its ID, and the run may end. Its output so far stays in its log.
- **The log is a file.** stdout and stderr go to one log in the session's operations directory, which the model reads with `Read`, `grep`, or `tail`. No read tool and no drain cursor.

### Hearing that it ended

- **A notice when it exits.** `<background_shell_notification>` with the shell ID, the command, the exit code, how long it ran, and the last lines of the log. It goes through `Session.Inject`, as a subagent's notice does: into a live run as a developer message, which cancels no request; with no run, it starts one (as built; the plan held it for the next message).
- **`wait_shell` to wait for one.** As `wait_agent` does for a child: it blocks until one of the named shells exits or the wait ends (4.5 minutes by default and at most, under the wake hold), returns its notice, and withdraws the held copy. Nothing sleeps or ticks; the wait is on the exit.

### Stopping one

- **`kill_shell`.** It takes a shell ID and stops the process group (SIGTERM, then SIGKILL) through `ownKill`, so it works from inside the sandbox. A killed shell sends no notice: the model already knows.
- **esc** interrupts the run only. Background shells and subagents keep running, and their notices wait for the next message.
- **ctrl+c** stops every background shell and subagent, then quits.
- **`/ps`** lists the session's shells with their command, state, and time running, and stops one.

### Limits and lifetime

- **A cap on running shells** (16), refused with a message that names the running ones.
- **Disk.** A shell's log counts toward `--max-disk`, as a pending call's output does; a log past its cap is cut at the head and tail.
- **Resume.** A shell from an earlier process is not reattached. On resume, the transcript says that the shell was lost, so the model does not wait for it.
- **Headless.** `uah exec` stops its background shells when its run ends and lists them in its result.
- **Sandbox.** A background shell keeps the sandbox it started with: its writes and network are fixed at launch.

## Not planned

- **`write_stdin` and a PTY.** For REPLs, prompts in the middle of a command, and debuggers. The sessions barely need it, and the model avoids prompts with non-interactive flags. Added when a real need shows up.
- **A `service` flag**, which would tell the run not to wait for a process that never exits: confusing for the model. Background shells never keep a run alive, so no flag is needed.
- **Keeping the turn going while a shell runs.** The turn still ends while a shell runs; the shell's end starts a new one (see As built), rather than the turn waiting for it.
- **A per-line `Monitor`.** A command that exits when its condition holds (`until curl -sf …/health; do sleep 1; done`) covers the health checks in the sessions with one notice.

## Open decisions

1. The background-shell cap: 16, or Codex's 64. Built with 16.
2. Whether a call moves to the background at the 5-minute hold, or earlier. Not built: it needs uah-core (see As built).
3. Whether `/ps` stops one shell, all of them, or both. Both.

## As built

The [engine README](../../internal/engine/README.md#background-shells) has the mechanics, and the [guide](../guide.md#39-background-shells) the use. Where the build differs from the plan:

- **A long call does not move to the background at the wake hold.** The hold and a pending call's lifetime are uah-core's coordinator's; moving a running operation out of its call needs a change there. A call still running after 5 minutes keeps today's behavior: the "still running" result, then its real result in a later turn.
- **A shell's end starts a turn when the session is idle** (the owner, after trying the build: "the shell result didn't create a new turn"), and so does a subagent's: `Session.Inject` starts a run for a held notification, unless the user stopped the last run, which leaves it for the user's next message. This replaces the plan's "starts no run" for both.
- **Running shells stay on screen:** the footer counts them (`1 shell running · /ps`) while any runs, the Bash call that started one reads `BG shell-1 <command>`, and `wait_shell` and `kill_shell` read `WAIT shell-1` and `KILL shell-1`.
- **The open decisions** took the plan's defaults: 16 running shells per session; `/ps` stops one (`/ps stop shell-1`) or all (`/ps stop all`).
- **A stop by the user tells the agent**, through the same notification; a stop by the agent (`kill_shell`) and a session's close do not.
- **ctrl+c asks twice while a shell runs**, as while a run is live, and names what quitting stops.
- **A scope with a command limit gets no background shells:** a `/review`'s reviewer, whose commands stop after 10 minutes, would otherwise start one that outlives the limit. A scope or `--tools` that leaves out `wait_shell` leaves them out too.
- **A resume writes no "lost" line.** The shells live in uah's process and stop when it quits; after a restart, `wait_shell` and `kill_shell` on an earlier shell say it is unknown and that shells do not survive a restart.
- **`uah exec`** prints each shell's start and end, waits for a run a shell's end started as for any run, also when stdin ends meanwhile (`TestDriveWaitsForARunANotificationStarted`), and returns only once its session's close has stopped the shells still running (`TestExecStopsBackgroundShells`); their stop comes after the event stream ends, so it is not printed.
- **One check on a clock.** A shell that ended while children it started still run keeps its process group; nothing reports when such a group empties, so the engine checks it each second and retires it once empty, so it never signals a group ID another process may have taken. The model is never woken by it.
- **The log** goes to `operations/<session>/shells/shell-N.log`, capped at 256 MiB; the notification carries its last 2,000 bytes.
