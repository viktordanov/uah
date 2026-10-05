# TUI design

Status: historical, superseded by the [TUI README](../../internal/tui/README.md) and the [architecture rules](../documentation/architecture.md). Kept as the record of the decisions. Originally: accepted, 2026-09-24 (proposed 2026-09-23). It depends on the library work in [harness.md](harness.md); [implementation.md](implementation.md) places every file. Framework numbers come from [bench/tui](../../bench/tui/README.md).

1. [Goal](#goal)
2. [Framework](#framework)
3. [Framework, revised: our own terminal layer](#framework-revised-our-own-terminal-layer)
4. [Architecture](#architecture)
5. [Screens](#screens)
6. [Input and steering](#input-and-steering)
7. [Slash commands and keys](#slash-commands-and-keys)
8. [Performance rules](#performance-rules)
9. [Testing](#testing)
10. [Phases and open questions](#phases-and-open-questions)

## Goal

`uah` opens a live, resumable session: start or resume a session, watch turns and tools as they happen (including tools that run in parallel with the model), send messages while the agent works, switch model, effort, and fast mode, and browse past runs.
It lives in this repository and builds on uagent's `core` and `harness` packages, so it shares every guard with the `uagent` CLI.

## Framework

Bubble Tea v2 (`charm.land/bubbletea/v2` v2.0.9, with lipgloss v2 and bubbles v2), under two conditions the benchmark showed are essential:

- **Virtualize the transcript.** Render only the visible lines, with each item's rendered lines cached per width. Feeding the whole transcript to `bubbles/viewport` re-measured every line per update: 40 s of CPU for 10,000 events, and 50,000 lines never finished.
- **Batch agent events.** Deliver at most one message per 16 ms window instead of one per event; unbatched bursts held the agent back for up to 0.9 s.

With both, Bubble Tea used 4.2 s of CPU for 10,000 events at 200/s and wrote about 124 bytes per event, the fewest of the widget toolkits, which matters over SSH and tmux.

| Option | First frame | Idle CPU | CPU, 10k events | Bytes per event | Why not first |
| --- | --- | --- | --- | --- | --- |
| **Bubble Tea v2** (virtualized, batched) | 24 ms (15 ms with `WithFPS(120)`) | ~0.6% of a core | 4.2 s | 124 | Chosen: the only option with a multi-line textarea, lists, markdown (glamour), and styling |
| Ultraviolet direct (Charm's renderer) | 3.9 ms | 0 | 4.0 s | 97 | Untagged, unstable API; every widget is ours. The fallback if Bubble Tea's redraw loop ever shows up in profiles |
| vaxis | 3.2 ms | 0 | 2.2 s | 1,250 | Most frugal, but one maintainer, single-line inputs only, and 10x the bytes written |
| tcell v3 | 3.4 ms | 0 | 4.0 s | 1,324 | No widgets, no test screen in v3 |
| tview | 4.6 ms | 0 | 6.9 s | 1,565 | Still on tcell v2 |
| gocui | 4.4 ms | ~0.3% | 6.6 s | 1,362 | Stale; 207 MB with 50,000 lines |

A 20 ms cold-start difference is below what people notice when a TUI opens, and Bubble Tea's idle cost is a constant render ticker. Both are accepted for its widgets and ecosystem.
To keep the choice reversible, all state and layout logic stays framework-free (below), so moving to Ultraviolet or vaxis replaces only the thin shell.

## Framework, revised: our own terminal layer

Decided 2026-10-04 by the owner (option B of the study), built after 1.8.4. uah runs its TUI on `internal/tui/term`, its own event loop and line renderer, and no longer imports Bubble Tea or bubbles; ultraviolet stays as the input decoder only, behind one file. The [TUI README](../../internal/tui/README.md#the-terminal-layer) describes the layer.

**Why.** A profile of uah 1.8.4 under a fake model showed Bubble Tea's costs that uah could not tune away:

- **Idle.** Its renderer checks the view on a frame ticker that never pauses: 92 to 162 wakeups a second and about 3 ms of CPU a second with nothing on screen changing. An upstream fix (charmbracelet/bubbletea#1832) parks the ticker, but leaves the rest.
- **Active.** About 40 to 50% of the TUI's CPU went to the renderer, which parses the whole screen back into cells on every frame (425 µs a frame at 140x45), and 10 to 20% to building the view after every update, where a frame needs it once.
- **The options.** Bubble Tea with the fork fixed idle only; ultraviolet with our own loop kept the re-parse; tcell, vaxis, or gocui meant the largest rewrite for no gain. Our own loop and a line renderer measured best in a prototype that ran uah's model unchanged.

**What changed.** The state, the renderer of lines, and the model's logic are the same; `bubble` speaks `term`'s types (a rename of `tea.*`), and the composer is uah's own (`internal/tui/composer`, replacing bubbles/textarea). The terminal sees the same modes and queries as before (alt screen, bracketed paste, modifyOtherKeys and the kitty flags, the mouse, OSC 11, mode 2026 where Bubble Tea asked), the same key names, and the same cursor. Lines are written with autowrap off, from the first changed cell, and moved with scroll regions.

**Measured.** The same bench before and after: the real uah in tmux at 140x45 against a fake model streaming an answer, 3 runs per phase interleaved, the median shown, for 1.8.4 (Bubble Tea), the prototype, and the final build.

| Phase | CPU ms/s | Wakeups/s | Frames/s | Output KB/s | RSS MB |
| --- | --- | --- | --- | --- | --- |
| Idle, fresh | 3.2 / 0.0 / **0.0** | 92 / 0 / **0** | 0 / 0 / 0 | 0 / 0 / 0 | 35.5 / 34.0 / 33.6 |
| Idle, long transcript | 2.8 / 0.0 / **0.0** | 92 / 0 / **0** | 0 / 0 / 0 | 0 / 0 / 0 | 46.4 / 44.6 / 43.1 |
| Idle, after streaming | 3.5 / 0.0 / **0.0** | 162 / 0 / **0** | 0 / 0 / 0 | 0 / 0 / 0 | 104.3 / 97.9 / 96.5 |
| Streaming | 56.5 / 29.0 / **26.5** | 1,125 / 721 / **647** | 22.1 / 26.5 / 26.4 | 3.4 / 13.0 / 12.0 | 49.5 / 45.9 / 44.1 |
| Streaming, long transcript | 59.8 / 30.1 / **30.5** | 1,091 / 693 / **664** | 22.2 / 26.6 / 26.6 | 3.4 / 12.1 / 11.2 | 102.9 / 96.8 / 95.2 |
| Scrolling | 19.5 / 7.6 / **7.0** | 632 / 253 / **198** | 10 / 10 / 10 | 4.5 / 8.1 / 7.8 | 48.4 / 45.9 / 44.3 |
| Typing | 24.7 / 8.5 / **5.9** | 1,043 / 229 / **173** | 20 / 16.3 / 16.3 | 0.65 / 4.0 / 2.5 | 49.3 / 46.2 / 44.4 |

Each cell is 1.8.4 / prototype / final. Idle costs nothing now; streaming takes about half the CPU, scrolling a third, and typing a quarter, with 40 to 85% fewer wakeups. More frames are drawn while streaming (every change, up to 30 a second), and fewer while typing, where keys that come within 33 ms share a frame; a key after a pause echoes at once. The output is 2.5 to 4 times Bubble Tea's, at most 12 KB a second, because a changed row is rewritten from its first changed cell to its end rather than cell by cell; writing only changed cells cut typing's output from 4.0 to 2.5 KB a second. Memory after streaming is uah's decoded session, the same on all three.

**Risks and how they are held.**

- *Width disagreements* (emoji, East Asian text): a row is erased before it is written and autowrap is off, so a terminal that counts a line differently cuts or pads that row and never wraps or leaves stale text. A row is written from mid-line only when the text before the change is narrow in every terminal.
- *Scroll regions* are plain VT100 (DECSTBM, SU, SD), supported by every terminal uah runs in. *Mode 2026* is used only after the terminal confirms it.
- *Teardown* runs on a quit, on SIGTERM, SIGHUP, and SIGINT, and on a panic anywhere in the model or its commands, with pseudo-terminal tests for each.
- *Tests:* the renderer is checked cell by cell against a terminal emulator on real frames and 4,800 random ones; the loop on a fake clock; the terminal in a pseudo-terminal; the TUI's own tests and goldens are unchanged but for the renamed types.

## Architecture

```text
cmd/uah/tui.go            `uah` default action: flags, session setup, starts the program
internal/tui/state/       pure: State, Reduce, transcript items, queue, command registry, key intents
internal/tui/render/      State -> lines with lipgloss; per-item line cache; no Bubble Tea import
internal/tui/bubble/      the Bubble Tea shell: model, overlays, textarea, event batching, effect executor
```

The core is an Elm-style reducer with no framework import:

```go
type State struct {
    Mode       Mode              // Chat, Picker, Inspector
    Session    SessionView       // ID, workspace, settings, runs
    Transcript []Item            // keyed items, updated in place
    Live       *LiveRun          // nil when idle: run ID, current turn, open tools, timers
    Queue      []PendingInput    // ID, text, state: queued, sent, delivered, failed
    Totals     core.Stats        // from a core.StatsCollector fed the same events
    Caps       harness.Capabilities // live input, live model, service tier
    Notices    []Notice
}

func Reduce(s State, ev Event) (State, []Effect)
```

- `Event` is a `core.Event`, a session event (`InputQueued`, `InputDelivered`, `SettingsChanged`, `Idle`), or a user intent (`Submit`, `Slash`, `Interrupt`, `ToggleExpand`, `Scroll`).
- `Effect` describes work for the shell to do: `StartRun`, `SendInput`, `SetSettings`, `Interrupt`, `LoadSessions`, `LoadSession`, `Quit`.
- Transcript items are keyed (`msg:<id>`, `turn:<n>@<run>`, `call:<id>`, `op:<id>`). A tool that finishes after later turns started updates its original row, which is how this runner's asynchronous tools look.
- Only the root Bubble Tea model has `Update`; child components expose methods and render functions (the pattern Charm's Crush uses).
- The session's event channel is drained by one goroutine that batches events per 16 ms and calls `program.Send` once per batch, so ordering is preserved and nothing is dropped.

## Screens

Chat and live run (alt screen):

```text
┌ uagent · openai-codex/gpt-6-sol · high · ~/code/proj · 3f2a…            ● running 01:42 ┐
│ › Fix the failing test in pkg/foo                                                        │
│ turn 1  12.4k in · 830 out · 3.1s                                                        │
│   ✓ Bash  go test ./pkg/foo          exit 1  2.3s  ├██████┤                              │
│   ✓ Bash  rg -n "func TestBar"       exit 0  0.4s  ├█┤                                   │
│ turn 2  14.1k in · 1.2k out · 5.0s                                                       │
│   ⠋ Bash  go test ./... -run Bar     running 8.7s  ├────████████▶   ok pkg/a 0.2s        │
│   · I'll patch the fixture while the suite runs.                                         │
│ turn 3  ⠋ thinking 4.2s                                                                  │
├ queued · sent when the agent is ready · ctrl+enter to send now ──────────────────────────┤
│  1. also update the README                                                               │
├──────────────────────────────────────────────────────────────────────────────────────────┤
│ > type a message, / for commands                                                         │
├──────────────────────────────────────────────────────────────────────────────────────────┤
│ 26.5k in (18k cached) · 2.0k out · 3 tools (max ∥ 2) · overlap 61%   enter send · ^p cmds │
└──────────────────────────────────────────────────────────────────────────────────────────┘
```

- Tool rows keep their slot, and a small bar shows each tool against its turn, so parallel and overlapping work is visible at a glance.
- A running command shows the last line of its output file; `enter` on a row expands the output.
- The final answer is rendered as markdown in its own block. Reasoning summaries are dim and toggle with `r`.

Session picker (`/resume`, `ctrl+s`): type to filter, a preview of the first prompt, runs, tokens, and the answer; `enter` resumes, `v` opens read-only. It reads only `request.json` and `summary.json` and loads the transcript on selection.

Run inspector (`tab` on a finished run, or `uah --view <run-dir>`): the statistics panel and a full timeline of model time and tool operations per turn.

Model and effort dialog: provider default, recently used models, and a free-text row, then the effort list. The footer says whether the change applies now or at the next run.

### Scrolling back

Added 2026-10-05 (ledger row 137, the owner: "when we scroll up and new content comes in, it shouldn't keep pushing the screen"). The transcript is virtualized, drawn from the bottom up, so a window scrolled up by a count of lines moved up with every line that arrived below it; only a whole new item was counted back. Now the window holds its text, as Codex's owned transcript does (`codex-rs/tui/src/transcript_view/follow_control.rs` on `main`, 2026-10-05):

- **The anchor.** While the window is pinned (scrolled up, or a drag selecting text), `State.Anchor` names the transcript line on its bottom row: an item's key and a line of its drawing. The renderer draws from the bottom up until it reaches that item and ends the window there, so the lines below the anchor are the scroll, however many arrived. An item above the window that grows, a resize, ctrl+t, and `/reasoning` leave the anchor's line on the bottom row; an item the view does not draw anchors at the drawn item above it; an anchor whose item left (a stream reset) falls back to the lines scrolled. A rewind's cut and sending a message return to the bottom.
- **The report.** The state cannot measure lines, so the shell lays out the frame after each update while the window is pinned and reports it back (`state.Anchored`: the bottom row's line, the lines below it, the width; `bubble/scroll.go`). More lines below the same anchor in the same layout are new output (`State.NewBelow`). Laying out in `Update` costs nothing extra: the frame that follows finds every item in the cache.
- **The pill.** New output below shows as Codex's control, ` New activity · ↓ Back to bottom · end ` (shorter at narrow widths, down to ` ↓ end `), centered over the window's last row on the accent. It is an overlay, not a row, so nothing moves when it comes or goes. It stays until the window follows the bottom again (end, scrolling down, sending), hides while a drag selects, and a click on it goes to the bottom. Codex's key is esc; uah's esc interrupts and goes back, so the pill names end.
- **Toasts.** The copy's notice took the status line's row and moved the screen; it is now a toast (`State.Toast`, two seconds) over the window's last row on the right, or its first row while the pill shows. The working line and the status line are untouched. `render.Styles.Note` draws any one-row overlay, for other notes later.
- **Edge scroll.** A drag held on the transcript's top row or below it scrolls on a 50 ms tick, faster further out, as Codex scrolls a row a frame; the tick runs only while the mouse is held there and the transcript can still move, so the idle TUI still does not wake up (`idle/tui` about 2 wakeups a second).

## Input and steering

What Enter does depends on the session state and the engine (see [harness.md](harness.md#two-engines)):

| State | Process engine | Embedded engine |
| --- | --- | --- |
| Idle | Start or resume a run | Submit to the inbox |
| Running | Queue; deliver all queued messages as one resume when the run ends | Queue; deliver at the next tool boundary or idle point |
| `ctrl+enter` while running | Interrupt (SIGINT), then resume with the queue | Submit now: the in-flight model request is cancelled and a new turn starts with the message |

The keys never change meaning: Enter sends (queueing while the agent works), Ctrl+Enter steers immediately, and Shift+Enter inserts a new line. Enter queues because a steer in this runner cancels the in-flight model request and wastes its tokens, unlike Claude Code's delivery at a tool boundary. `↑` on an empty input pulls the last queued message back for editing. Queued items show their state: queued, sent, delivered (acknowledged by the runner's echo of the message ID).

## Slash commands and keys

One registry holds each command's name, aliases, arguments, whether it is available while running, and whether the current engine supports it. Unsupported commands stay visible with the reason.

| Command | Effect | Process engine | Embedded engine |
| --- | --- | --- | --- |
| `/model [id]` | Change the model; no argument opens the dialog | Next run | Next model request |
| `/effort <level>` | `low`, `medium`, `high`, `xhigh`, `max`, `ultra` | Next run | Next model request |
| `/fast [on\|off]` | Priority service tier | Unavailable (upstream request b) | Next model request |
| `/resume [id]` | Open the picker or resume a session | Yes (idle) | Yes (idle) |
| `/new` (`/clear`) | Start a new session | Yes (idle) | Yes (idle) |
| `/stop` | Stop when the current work is done | Interrupt | "Stop when idle" control |
| `/status` | Session, settings, and statistics | Yes | Yes |
| `/inspect` | Open the run inspector | Yes | Yes |
| `/quit` (`/exit`) | Confirm if a run is live, interrupt, then exit | Yes | Yes |

| Key | Action |
| --- | --- |
| `enter` | Send, or queue while running |
| `shift+enter`, `ctrl+j` | New line (`ctrl+j` works in every terminal) |
| `ctrl+enter` | Send now (steer or interrupt and send) |
| `esc`, `esc esc` | Close an overlay; twice while running interrupts |
| `↑` on empty input | Edit the last queued message |
| `ctrl+p` | Command palette |
| `ctrl+l` | Model dialog |
| `alt+,`, `alt+.` | Lower or raise effort |
| `ctrl+s`, `ctrl+n` | Sessions, new session |
| `r`, `o` | Toggle reasoning, expand tool output |
| `ctrl+c` twice | Quit (confirm when a run is live) |

## Performance rules

These come straight from the benchmark and are requirements, not tuning:

1. The transcript is a windowed list: render only visible lines, cache each item's lines per width and version, and never re-render finished items.
2. Agent events are batched per 16 ms window before reaching the program.
3. Timers and spinners tick at 10 Hz only while a run is live; nothing ticks when idle.
4. Tool-output tails are read on a 100 ms tick with a byte cap, and only for visible running tools.
5. Use the real terminal cursor (`SetVirtualCursor(false)`), and `tea.WithFPS(120)` for a faster first frame.
6. The picker never reads full event files; it reads summaries and loads one session on selection.

## Testing

1. **Reducer tests**, framework-free: replay the fixtures (`simple`, `parallel`, `timeout`) through `Reduce` and check item order, parallel tool slots, late tool completion, queue transitions, and which commands the current engine allows.
2. **Golden screens**: render `State` at 100x30 with an ASCII color profile after each prefix of a fixture's events, stored under `internal/tui/render/testdata/` and regenerated with `-update`.
3. **End to end**: drive the program against the fake runner (`FAKERUNNER_SPEED=0`), check that a queued message produces a resume request with the session ID and message IDs, and that interrupting leaves no processes behind. The benchmark's pty harness with a VT emulator (`bench/tui/harness`) checks the real terminal output for every framework, so it can check this TUI too.
4. **By hand**: `FAKERUNNER_SPEED=1 uah --engine process --runner /tmp/fakerunner` replays real timing without tokens.

## Phases and open questions

1. **Library phase 1** from [harness.md](harness.md#roadmap): user messages in events, sessions, history, the session lock, the queue.
2. **TUI v1** on the process engine: chat and live view, queue, picker, inspector, `/model` and `/effort` for the next run, `/new`, `/resume`, `/quit`.
3. **TUI v2** on the embedded engine: live steering, live effort and model, `/fast`, `/stop`.

Open questions:

- Does switching model on resume work with the replayed encrypted reasoning? Capture a real run before promising `/model` on a resumed session.
- Alt screen (Crush) or inline mode with native scrollback (Codex)? Alt screen for v1: it is simpler with overlays and the timeline.
- `teatest` for Bubble Tea v2 is an untagged module; pin it, or rely on the reducer, golden, and pty tests.
