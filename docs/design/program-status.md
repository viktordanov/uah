# Program status: OSC 7501

Status: done in v1.12.0. Ledger item 144.

The TUI tells the terminal what the session is doing (idle, working, blocked on you, done, or failed) with the [program status protocol](https://superlogical.com/rex/docs/build/program-status), OSC 7501, revision 0.4 (2026-10-10, a draft). A terminal, or a tool that hosts terminals, can then show it in a tab, a session list, or a notification, through ssh and containers, with no hooks to set up.

1. [The protocol](#the-protocol)
2. [What uah reports](#what-uah-reports)
3. [How it is sent](#how-it-is-sent)
4. [Decisions](#decisions)
5. [Who reads it](#who-reads-it)

## The protocol

- **A report** is `ESC ] 7501 ; key=value:key=value ESC \`. The keys are `state`, `id`, `kind`, `progress`, `app`, `title`, and `msg`; unknown keys are ignored.
- **States:** `idle` (at rest, waiting for the user's next instruction), `working`, `blocked` with a `kind` (`permission`, `question`, or `auth`), `done` (finished work the user has not seen yet), and `error`. `clear` removes a record and every record beneath it; with no `id`, every record on the terminal.
- **Records:** a report without `id` is the program's own record; `id` addresses one beneath it, such as `agent/scout`: segments of `[A-Za-z0-9_.+-]{1,32}`, at most 8 of them and 128 bytes. A record without `app` takes its nearest ancestor's.
- **A report replaces its record whole:** a key left out is gone from the record.
- **Text:** `msg` and `title` are standard base64 of UTF-8 with no control characters (newlines and tabs included): `msg` at most 2048 bytes before encoding, `title` 192, the whole sequence 4096. Breaking a limit, or an `id` the grammar refuses, drops the whole report.
- **No heartbeat:** a program reports on transitions. When it exits, the terminal drops its `working` and `blocked` records and keeps `done` and `error`.

## What uah reports

The session's own record, the most pressing state first (`State.ProgramStatus` in `internal/tui/state/programstatus.go`):

| The session | Reports | `msg` |
| --- | --- | --- |
| An approval waits | `blocked`, `kind=permission` | `Approve <command>` |
| The agent's questions wait | `blocked`, `kind=question` | the question shown |
| A `/review` runs | `working` | `Reviewing` |
| A `/review` finished while the agent was idle | `done`, until you type, or `error` (`blocked:auth` for an expired login) | `Review: 3 findings (…) · patch is incorrect`, or the error |
| The agent works, a `!` command runs | `working` | what it does, as the wait line says it but with nothing that counts (bytes, seconds, a retry's countdown), so a report goes out when the run moves on, not on every frame: `Thinking`, `Running 2 commands`, `Writing a patch · main.go`, `Reconnecting` |
| A subagent runs after the agent's run finished (a failed one reports its error; a resumed subagent with no message yet does not count) | `working` | `Waiting for Ada`, or `Waiting for 2 subagents`: the run is not done, since a subagent's end starts the agent's next run |
| The last run finished, and no subagent works | `done`, until you type | the start of the answer, up to 300 characters |
| The last run failed | `error`, until you type | the run's first error from the runner or the model |
| The ChatGPT login expired, during a run or before it could start | `blocked`, `kind=auth`, until you type | the error, which says to sign in |
| A message was not delivered: a hook blocked it, or its run failed to start | `error`, until you type | `not delivered: <reason>` |
| A run could not start, also one a subagent's or a shell's end started | `error`, until you type | the error |
| Interrupted, at the prompt, quitting | `idle` | none |

Each subagent that runs, or that finished or failed since you last typed, has a record of its own, `agent/<nickname>` with the nickname it has now (a resumed child may get another), titled with its nickname and role: `working` with the tool call it runs, `done` (`Finished`), or `error` with its message. One stopped or shut down while running has none; its record is cleared. A nickname comes back once its child closed: only the child whose work started last counts (a resumed one too), so a stopped one never brings back the earlier one's end. A nickname that is not a valid ID as it is adds the start of its child's UUID, `Scout A` is `agent/1a2b3c4d.Scout-A`, so two nicknames that would make one ID, such as `Scout A` and `Scout-A`, keep two records, and each child keeps its ID whatever others show. Only when two such children's UUIDs also start with the same 8 characters, about one chance in four billion, does the later take more of its UUID while the other shows, and its record moves once when that one goes. Closing a child whose end you saw brings nothing back.

"Until you type" is any change to the composer (a key, a paste, a recalled prompt, the editor), or a message sent: that is when the result is taken in. A new run replaces it at once. A resumed or switched session's saved runs and subagents are taken in as they load: they are not news. A subagent it saved as running has no record: loading does not restart it, so it shows again only when it updates.

## How it is sent

- **In the frame** (`internal/tui/term/status.go`): `term.View.Status` holds the records, and `Program.draw` compares each with the report last sent for its ID, as it does the window title. A changed record goes in the same write as the frame, so a report never lands inside one; an unchanged one is not sent again; a record the view leaves out is cleared. What the terminal was told is kept only once a frame's write succeeds: after a failed write, part of which may have reached the terminal, every record, and every clear, is sent again with the next, and a record the failed frame added is cleared if the next leaves it out.
- **IDs:** `term.StatusID` makes a nickname a valid segment (`Scout #2` is `Scout-2`, cut to 32 bytes with no dash left at the end), and a record whose ID the protocol's grammar refuses is not sent.
- **Text:** control characters become spaces, and `msg` and `title` are cut on a character's boundary to their limits, so no report is dropped for its length.
- **Leaving:** the last reports are written after the terminal is left, so they go out also when it was already released, as while ctrl+g's editor runs. On a quit, the subagents' records are cleared and the session's own is reported `idle`: a `done` record would otherwise outlive uah, though you saw it. When the TUI stops on an error or a panic, the session's own reports `error` with the reason, which stays after uah exits.
- **Only the TUI** reports. `uah exec` and `uah review` do not, and the TUI runs only on a terminal.
- **Off:** `[tui] program_status = false`, which sends nothing (`term.Options.StatusApp`).

## Decisions

- **The state, not the events.** The records are a function of the TUI's state, as the window title is, so every path into a state (an event, a key, a session switch) reports it, and a table of states tests it.
- **No `clear` for the session's own record on quit.** `clear` without an `id` removes every record on the terminal, other programs' too; `idle` says the same and the terminal may drop it.
- **No `progress`.** A run has no known end; absent means indeterminate.
- **No feature detection.** It is optional: a terminal that does not know OSC 7501 ignores it, and reading its answer would compete with the TUI's input.
- **No tmux passthrough.** The protocol does not cover multiplexers, and tmux passes OSC sequences on only with `allow-passthrough`; tmux drops the reports.
- **The login only when it expired.** A login uah waits on is the one that blocks a run, `codexauth.ErrLoginExpired`, which reaches the TUI as the run's error text.

## Who reads it

Rex shows the records in its session picker and tab headers; libghostty has an implementation in review. The protocol's author asks implementers to get in touch to be listed.
