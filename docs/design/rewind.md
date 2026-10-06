# Going back to an earlier message

Status: built (ledger item 46). The package READMEs hold the current contract; this record keeps the research and the decisions.

Ledger item 46: pick an earlier message of yours, edit it (or drop it), and continue from there. What came after it leaves the model's context, and the old branch stays on disk. Codex calls it backtrack; Claude Code calls it rewind.

1. [What Codex does](#what-codex-does)
2. [What Claude Code does](#what-claude-code-does)
3. [The design](#the-design)
4. [Decisions](#decisions)
5. [Out of scope and open](#out-of-scope-and-open)

## What Codex does

Checked against Codex rust-v0.156.1. Paths are under `codex-rs/`.

- **The keys** (`tui/src/app_backtrack.rs`, `app_backtrack/legacy_input.rs`, `app_backtrack/browsing.rs`):
  - Esc on an empty composer "primes" backtrack and shows the hint "esc again to edit previous message" (`handle_backtrack_esc_key`, `prime_backtrack`; the text in `tui/src/bottom_pane/footer.rs:937`). Any other key, a click, or a paste ends the primed state (`cancel_primed_browsing_for_event`).
  - A second esc opens transcript browsing and highlights your latest message (`open_backtrack_preview`, `step_backtrack_and_highlight`). With no message it says "No previous message to edit."
  - ←/→ (or h/l) step to earlier and later messages, ↑/↓ scroll, and ctrl+t toggles details. Enter confirms. Esc restores the view where browsing started (`cancel_transcript_browsing`). The footer reads "Browsing · ↑↓/jk scroll · ←→/hl prompts · ↵ rewind · esc back" (`app_backtrack/prompt_navigation.rs`).
  - The selectable messages are your messages since the last session start (`user_positions_iter`).
- **Enter** sends `AppEvent::RevertSessionForPromptEdit` with the selected message (`apply_backtrack_selection`). The message's text, its local images (with their `[Image #N]` placeholders), and its remote images return to the composer (`backtrack_selection`). On failure the prompt goes back to the composer with "Failed to edit the selected prompt: …" (`restore_backtrack_prompt_after_revert_error`).
- **Steers.** Only the first message of a turn can be reopened. `backtrack_revert_before_turn_id` refuses "the selected prompt is a steer and cannot be edited independently" and a turn still in progress, "because app-server cannot revert in the middle of a turn".
- **The revert** (`app-server/src/request_processors/thread_processor.rs`, `thread_revert_response`; `thread-store/src/local/revert_thread.rs`): `thread/revert` shuts the thread down, then writes a new rollout file that references the kept prefix, up to before the selected turn (`history_base_at_boundary`, `ForkBoundary::BeforeTurn`), and switches the thread's SQLite rollout pointer to it. "Old rollouts stay intact." The thread keeps its ID, reloads, and the client's transcript ends before the reverted turn.
- **The older marker.** `EventMsg::ThreadRolledBack { num_turns }` ("Legacy persisted marker for dropping the last N user turns", `protocol/src/protocol.rs`) is still replayed from existing rollouts, but live rollbacks no longer write it.

## What Claude Code does

Checked against the [checkpointing](https://code.claude.com/docs/en/checkpointing) page on 2026-09-25.

- `/rewind`, or esc twice on an empty prompt, opens a menu of your prompts. A prompt that joined a running turn is not listed; "rewind to the prompt that started the turn" instead.
- For the selected prompt it offers: restore code and conversation, restore conversation, restore code, summarize from here, summarize up to here, or never mind. After restoring the conversation, the prompt returns to the input for editing.
- Code restore uses file snapshots of Claude's own edit tools, taken before each prompt. Bash commands, subagents' edits (mostly), external edits, and symlinked or hard-linked files are not restored. Checkpoints survive a resume.

## The design

**Keys.** Codex's gesture, where it is free in uah: esc esc already interrupts a busy agent, so the backtrack gesture applies only while the session is idle, nothing is queued, and the composer is empty.

| Key | Does |
| --- | --- |
| esc on an empty composer, idle | Primes: "esc again to edit a previous message" in the status line |
| esc again (within 2 seconds) | Selects your latest message, marks it `▶ … ↵ edit from here`, and scrolls to it |
| ↑, ←, k (while one is selected) | An earlier message |
| ↓, →, j | A later message |
| enter | Go back to before the message: it returns to the composer with its images |
| / | Search your messages and the agent's answers; esc comes back here, on your message at or above the match ([TUI README](../../internal/tui/README.md#keys)) |
| esc, ctrl+c | Cancel |
| any other key | Cancel, then the key does what it does (a typed letter goes into the composer) |

`/rewind` selects the latest message at once. ↑ on an empty composer still takes the last queued message back, since backtrack never starts with a queue. The selection lives in the reducer (`state.Backtrack`, `internal/tui/state/backtrack.go`); the renderer marks the message and computes the scroll that shows it a third of the way down (`internal/tui/render/backtrack.go`). Cancelling changes nothing.

**Enter.** The reducer returns `EffRewind{ID}` and puts the message in the composer, as `DraftRestored` does for a withdrawn message. The shell calls `Session.Rewind(id)`. When the session reports `engine.Rewound`, the reducer cuts the transcript at that message: it and everything after it leave the screen, as Codex's transcript ends before the reverted turn. The footer's context meter takes the cut's token count.

**The session.** `Session.Rewind(messageID)` runs on the loop goroutine. It needs `Capabilities.Rewind` and an engine that implements `engine.Rewinder`, and an idle session with nothing queued and nothing waiting for its hooks (`ErrRewindBusy`). It emits the engine's `engine.Rewound`.

**The cut, in the embedded engine** (`internal/engine/embedded/rewind.go`):

1. It reads the runner's session items and finds the message's inbox input by its ID, the same ID the TUI keys the message by.
2. The cut starts there, or at the first of the inputs right before it: they reached the agent together (a subagent's notification, a `!` command's record, an earlier queued message). The cut drops them too, and the session holds their texts again, so they go with the next message, as they went with this one.
3. It refuses a message that arrived while a tool call the agent made had no status yet (`errMidTool`): the coordinator would run that call again on restore. This is Codex's and Claude Code's rule for steers, narrowed to the case that breaks; a message sent while the model was thinking can be chosen.
4. It appends a `compaction.Rewind` to `sessions/<id>.rewind.jsonl`: the message ID, the first and last runner item sequences it cuts, the time, and the context the last response before the cut reported.
5. It trims the session's last request for `/context` to before the message, so `/context` shows what the model will see.

Every later run opens the runner store through `cutStore`, which leaves the cut items out of `Items`. The coordinator restores its context from them, so the next request carries the history before the message and then the new one. The runner's own store validates that each new turn follows the file's latest turn, which may be a cut one, so `cutStore.AppendTurn` chains a new turn to the file's latest turn. A fork of the session for a subagent reads the same filtered items and chains its copied turns anew.

**Compactions.** A compaction's record covers the first N items of the history with their hash. A rewind leaves any record made before it that covers only items before the cut valid, since those items do not change. A record that covered the cut message no longer matches. Each run settles once which record applies: the newest record that matches, dropping records made before a rewind that do not (`settleLocked`). So going back past an automatic compaction uses the compaction before it, or none, without the mismatch warning, and going back to a message sent after a `/clear` keeps the clear.

**Resume.** The rewind log is the source. The next run's store reads it, so the cut survives restarting uah. `session.Load` adds each rewind as an `engine.Rewound` event after the run it followed, and the TUI applies it after that run's events, so a resumed transcript ends where the session went back to and continues with the edited message. `uah sessions show` prints the whole history with a line `↺ went back to before "<message>"; it and what followed left the agent's context`, and `uah run --stream` writes a `rewound` event.

**The old branch** stays in the runner's session file, the run records, and their events; only what the context builder reads changes.

**The process engine** runs the runner as a separate program that reads its session file itself, so uah cannot filter what it restores, and the item would need a change to the runner. It states the gap in the capability table (`FeatureRewind`): esc esc does nothing more than today while idle, and `/rewind` shows "rewind: not supported by the process engine (esc esc and /rewind cannot go back to an earlier message; /new starts over); use the embedded engine".

## Decisions

- **A cut record in the same session, not a fork into a new session ID.** Codex's revert keeps the thread's ID and points it at a new file that references the old prefix. A new session ID would move the sidecar settings, the run records, the compaction log, and the resume picker entry, and `uah resume <id>` of the old ID would open the old branch. A record next to the session keeps one ID, as `/clear` does, and the old items stay where they are.
- **Runner item sequences, not request indexes.** A compaction record counts items of the model request, which works because the history only grows at its end. A rewind removes items in the middle, and the builder merges several runner items into one request item. The runner's item sequences are stable, so the cut names them, and the context builder never sees the cut items.
- **Cut the transcript on the session's event, not on enter.** A live cut and a replayed one take the same path, and a refused rewind leaves the transcript as it was, with the message in the composer and the error as a notice.
- **Subagents.** A subagent that the cut branch started keeps running; the cut does not stop it. Its `<subagent_notification>` reaches the agent with the next message as any notification does, and `/agents` still lists it, though the agent no longer has the call that started it in its context. Its item leaves the transcript with the rest and comes back with its next update.

## Out of scope and open

- **Restoring files.** uah does not snapshot files, so going back never changes the workspace, as Codex's backtrack. Claude Code's code restore would need snapshots before each prompt; `apply_patch` writes could be recorded, but Bash writes could not, as Claude Code's limits show.
- **Claude Code's summarize from here and up to here.** Not built; `/compact` covers the whole history.
- **Codex's browsing view.** Codex browses in a transcript overlay with ↑/↓ scrolling and ctrl+t details; uah selects in the transcript itself, where ↑ and ↓ move between messages.
- **A subagent forked after a rewind** gets the parent's filtered history, but the parent's compactions are copied as they are. One that covered the cut message does not match the child's history: the child sends its full history and reports the mismatch once.
- **Rewinding the process engine** would need the runner to restore from a filtered or forked history.
