# Prompt history and a taller composer

Status: built (ledger items 68 and 74). The package READMEs hold the current contract; this record keeps the research and the decisions.

Ledger item 68: ↑ and ↓ bring back earlier prompts, from this session and from earlier ones, ctrl+r searches them, and the composer grows past 8 rows for a long prompt.
Ledger item 74: ↑, ↓, and ctrl+r show only the prompts sent in the session's workspace, as Claude Code keeps history per project.

1. [What Codex does](#what-codex-does)
2. [What Claude Code does](#what-claude-code-does)
3. [The design](#the-design)
4. [History per folder](#history-per-folder)
5. [Keys and their conflicts](#keys-and-their-conflicts)
6. [Decisions](#decisions)
7. [Open](#open)

## What Codex does

Checked against Codex rust-v0.159.1. Paths are under `codex-rs/`.

### Where the history lives

- **The file.** `~/.codex/history.jsonl` (`message-history/src/lib.rs:52`, `HISTORY_FILENAME`), one JSON object per line: `{"session_id":"<thread id>","ts":<unix seconds>,"text":"<prompt>"}` (`HistoryEntry`). There is one file for every workspace and every session; the session ID is recorded but nothing filters by it.
- **Who writes it.** The TUI, not the agent core: `App::append_message_history_entry` (`tui/src/app/thread_routing.rs:558`) spawns `codex_message_history::append_entry` and logs a failure at warn level; the prompt is then lost. A test (`tui/src/app_server_session/prompt_history_tests.rs`) checks that the history stays the TUI's own when the app server runs elsewhere, whatever the server's `[history]` says.
- **The write** (`append_entry`, `lib.rs:104`):
  - The whole line, with its `\n`, is built first and written with one `write` call on a file opened with `O_APPEND` and mode 0600 (`lib.rs:149`).
  - Before writing, it narrows the file to 0600 when the mode is different (`ensure_owner_only_permissions`, `lib.rs:308`). On Windows it does nothing.
  - The write holds an exclusive advisory lock (`File::try_lock`), tried 10 times 100 ms apart (`MAX_RETRIES`, `RETRY_SLEEP`, `lib.rs:58-59`); after that the append fails with `WouldBlock`.
  - A `// TODO: check text for sensitive patterns` (`lib.rs:119`) says nothing is filtered: the file holds exactly what was typed.
- **The cap** (`enforce_history_limit`, `lib.rs:195`). After the write, still under the lock, a file larger than `max_bytes` loses its oldest lines until it is at most 80% of the cap (`HISTORY_SOFT_CAP_RATIO`, `lib.rs:56`), so the next append does not trim again. The newest line always stays, even when it alone is larger than the cap. The rest of the file is rewritten in place (`set_len(0)` and a write).
- **The configuration** (`config/src/types.rs:201`): `[history]` with `persistence = "save-all"` (the default) or `"none"`, and `max_bytes` (unset by default: no cap; 0 also means no cap). `none` stops the writes only; the TUI still reads the file.
- **Reading.** A session does not load the file. When a thread starts, resumes, or forks, the TUI gets the file's identity (the inode, or the creation time on Windows) and its line count (`history_metadata`, `lib.rs:285`). Up/Down then asks for one entry at a time by offset (`lookup`, `lib.rs:300`, under a shared lock with the same retries), and ctrl+r asks for batches of at most 128 rows or 64 KiB (`lookup_batch`, `batch.rs:19-20, 111`). A lookup against a file whose identity changed returns nothing, so a replaced file cannot give a wrong entry. A line that is not valid JSON is skipped.

### ↑ and ↓

`tui/src/bottom_pane/chat_composer_history.rs`, `ChatComposerHistory`:

- **One offset space.** The file's entries (counted when the thread started) come first, then this TUI session's own submissions (`local_history`), newest last. The session's own prompts are also written to the file, but the TUI browses them from memory, with their images, pasted text, and mentions (`HistoryEntry`).
- **When the arrows recall** (`should_handle_navigation`, line 394): never when there is no history; always on an empty composer; otherwise only when the composer's text is exactly the last recalled entry (`last_history_text`) and the cursor is at its start or end. Otherwise ↑ and ↓ move the cursor. The composer calls it with the editor's move-up and move-down keys, ↑/↓ and ctrl+p/ctrl+n (`chat_composer.rs`, after the ctrl+d check), and not while the mouse has a selection in the composer.
- **↑** (`navigate_up`, line 419): the newest entry first, then older ones; at the oldest it stays. An entry the file has not given yet is fetched and the composer waits for it.
- **↓** (`navigate_down`, line 445): moves the cursor unless browsing; past the newest entry it clears the composer and stops browsing. There is no saved draft to return to: browsing only starts from an empty composer.
- **The recalled text.** It replaces the composer with the cursor at its end (`move_cursor_to_history_entry_end`, `chat_composer.rs:1669`), so the next ↑ recalls again. Editing it makes it the user's draft: the text no longer equals the recalled one, so the arrows move the cursor. The stored entry never changes.
- **Duplicates.** A submission equal to the previous one of the session is not added again (`record_local_submission_inner`, line 330). The file keeps every submission. When a resumed thread replays its messages into the session's history, a file entry equal to one of them is skipped while browsing (`persistent_entry_duplicates_local`, line 909).
- **The menu.** While the composer shows a recalled entry, popups stay closed (`browsing_history`, `chat_composer.rs:3958`), so a recalled `/model x` does not open the command menu and take ↑.
- **ctrl+c** on a draft clears it and adds it to the session's history, so ↑ brings it back (`clear_for_ctrl_c`, `chat_composer.rs:1706`). It is not written to the file.
- **Vim mode** has its own history keys in normal mode (`chat_composer/vim_history.rs`).

### What goes in

- **Messages**: the text as sent, with images as their `[Image #N]` placeholders; the file cannot restore the image itself (`chatwidget/input_submission.rs:525`). Mentions are encoded as links in the text and decoded on recall. The session's own entries keep images, remote image URLs, and pasted text.
- **Shell commands**: `!cmd` with its `!` (`submit_shell_command_with_history`, `input_submission.rs:53`). Recalled, the `!` puts the composer back in shell mode.
- **Slash commands**: never in the file, except `/goal` and its objective (`chatwidget/slash_dispatch.rs:353, 959, 1011`). They do go into the session's history after they run (`record_pending_slash_command_history`, `chat_composer.rs:1808`), so ↑ recalls them in the same session.
- **When.** A message goes to the file when it is submitted to the thread; a queued message when the queue sends it.

### ctrl+r

`tui/src/bottom_pane/chat_composer/history_search.rs` and `ChatComposerHistory::search` (line 581):

- **Opening** (`begin_history_search`, line 127): ctrl+r saves the whole draft (text, images, vim state) and turns the footer into the query: `reverse-i-search: `. Nothing is previewed until the query has text.
- **Matching** (`search_matches`, line 785): a case-insensitive substring of the entry's text. Each edit of the query starts again from the newest entry. Within one search, a text is shown once: a repeated prompt is one match (`seen_texts`).
- **Keys** (`handle_history_search_key`, line 166): ctrl+r or ↑ go to an older match, ctrl+s or ↓ to a newer one (`history_search_previous` and `history_search_next`, `tui/src/keymap.rs:1686-1687`, configurable); at either end the match stays (`AtBoundary`). Enter accepts only an actual match, as an editable draft with the cursor at its end; it does not send. Esc and ctrl+c put the saved draft back. Backspace and ctrl+h delete from the query, ctrl+u clears it, other characters and pastes go into it; other keys do nothing.
- **The look** (`history_search_footer_line`, line 396): `reverse-i-search: <query>` in the footer, the query in cyan with new lines as `↵` and tabs as `⇥`, then `enter accept · esc cancel` for a match, `no match` in red, or `searching` while an entry is fetched. The composer shows the match with the query's occurrences highlighted (`history_search_highlight_ranges`, line 427), and the terminal's cursor sits after the query in the footer (`history_search_query_cursor_pos`, line 502). With no match, the composer shows the saved draft.
- **After accepting**, the history cursor stays on the match, so ↑ and ↓ go on from it.

### Related keys and the composer's height

- ctrl+t opens the transcript overlay (`open_transcript`, `keymap.rs:1647`), where reasoning shows. Codex has no key that shows or hides reasoning summaries; alt+r toggles raw output (`keymap.rs:1656`).
- alt+↑ or shift+← takes the last queued message back (`edit_queued_message`, `keymap.rs:1675`); shift+↑/↓ (and alt+. / alt+,) change the effort (`keymap.rs:1669`).
- **The composer has no fixed height.** The textarea wants as many rows as its wrapped lines (`bottom_pane/textarea.rs:523`), and the inline viewport grows to fit, clamped only to the terminal's height (`tui.rs:1213`, `height.min(screen_size.height)`). Codex can do that because its transcript is the terminal's scrollback above the viewport. ↑ on the first visual row moves the cursor to the start of the text (`textarea.rs:1465`).

## What Claude Code does

Checked against the Claude Code documentation ([interactive mode](https://code.claude.com/docs/en/interactive-mode)) on 2026-09-30.

- History is kept per working directory, and ↑ also reaches prompts of earlier sessions of the same project. `/clear` starts a new session; its prompts come first.
- ↑ and ↓ (or ctrl+p and ctrl+n) move the cursor inside a prompt of more than one row and recall history once the cursor is on the first or last row. While messages are queued, ↑ from the first row takes them back.
- The same prompt sent twice in a row is one entry. A recalled prompt with pasted text sends the whole paste again.
- Esc esc on a draft clears it and saves it to history.
- ctrl+r searches every project's prompts in the classic renderer, newest first, duplicates collapsed to the newest, with the term highlighted. Ctrl+r again goes older; tab or esc accepts to edit, enter accepts and sends, ctrl+c cancels, and backspace on an empty query cancels. The fullscreen renderer shows a dialog instead, with ctrl+s cycling the scope: this session, this project, all projects.

## The design

- **The file** (`internal/history`): `<uah home>/history.jsonl` (`~/.uah`, or `$UAH_HOME`), in Codex's format with Codex's field names and one field of uah's, `workspace` (see [History per folder](#history-per-folder)). `File.Append` builds the lines first, opens the file `O_APPEND` and 0600, narrows it to 0600 when it is not, takes an exclusive `flock` (10 tries 100 ms apart; a process-wide mutex orders the process's own appends first, so the tries only wait on other processes), writes them in one call, and trims to the cap as Codex does: the oldest lines go until the file is at most 80% of `max_bytes`, and the newest line stays. `File.Load` reads every line under a shared lock and skips a line that is not an entry. `Recorder` keeps the order: the shell adds each prompt on its update loop and flushes off it, and a flush writes everything queued so far, in order.
- **The configuration**: `[history]` with Codex's keys, `persistence = "save-all" | "none"` and `max_bytes`. `none` stops the writes and still reads the file, as in Codex. `max_bytes` defaults to 8 MiB (8388608); 0 means no cap. An invalid `persistence` stops the TUI before it starts. `uah config` shows both keys.
- **State** (`internal/tui/state/history.go`, `historysearch.go`). `State.History` holds the entries, oldest first: the file's (`PromptsLoaded`, from `EffLoadPrompts` at startup), then this process's, each with its workspace, and shows the session's workspace's entries. Everything else is pure:
  - `Recalls(draft, atEdge, newer)` is Codex's `should_handle_navigation`: always on an empty composer; on the recalled text left as it was with the cursor at its start or end; ↓ only while browsing. The shell asks it before dispatching `RecallOlder` or `RecallNewer`, so a key it declines keeps its old meaning.
  - A recall returns `EffSetDraft`. An entry that starts with `!` puts the composer in shell mode with the command; a message gets its images back from its tags. Past the newest, ↓ gives the empty composer and leaves shell mode.
  - `Reduce` records a `Submit` or `Steer` before reducing it (`remember`), so shell mode and the images are those of the prompt sent. The entry of this process keeps the images' tags; the file gets `EffRecordPrompt` with the text as shown: placeholders, `!` for a command. Slash commands stay out of the file. A repeat of this process's last prompt is not added again. Each submission ends browsing and searching.
  - `DraftCleared` (ctrl+c on a draft) adds the draft to this process's history only, then reduces as `DraftChanged`.
  - While the composer shows a recalled prompt, or a search is open, `Suggestions` is empty, so the menu does not open on a recalled `/model x`.
  - The search keeps the saved draft (text, shell mode, images), the query, its status, and the matching entries, newest first, one per text. Every query edit recomputes them from the newest, over the entries in memory.
- **Shell** (`internal/tui/bubble/history.go`, `composer.go`). ↑ and ↓ ask `Recalls` with `atEdge` (the cursor at the draft's first or last character); ctrl+r dispatches `SearchOpen`; while a search is open every key goes to `onSearchKey`, and a paste goes into the query. `runHistory` loads the file and appends through the recorder; a failure shows as an error notice. While the search is open, the terminal's cursor sits after the query in the footer.
- **Render** (`internal/tui/render/history.go`). The footer becomes ` reverse-i-search: <query>` (the query in the accent, `↵` and `⇥` for new lines and tabs) with `  enter accept · esc cancel` for a match or `  no match` in the theme's bad color. The agent view draws the session's search too.
- **The composer's height**: `composerRows(h) = max(8, h/2)`, applied as the textarea's `MaxHeight` on every resize (`resizeComposer`), so a 30-row terminal gives 15 rows and a 50-row terminal 25. The draft itself has no limit short of the textarea's 10,000 lines, and the composer scrolls to keep the cursor in view.

## History per folder

The file stays one global file with the size cap; only what ↑, ↓, and ctrl+r see is per folder.

- **The line.** Each line uah writes adds `"workspace"`, the session's workspace as uah resolves it: an absolute, clean path (`--workspace`, the resumed session's, or the directory uah started in). For example: `{"session_id":"…","ts":1790000000,"text":"fix the tests","workspace":"/src/app"}`. Codex's three fields are unchanged, so the file stays Codex's format with one extra field that a reader of Codex's format ignores.
- **The view.** `State.History` keeps every prompt with its workspace: the file's and this process's. It shows only those whose workspace equals the session's, compared as strings: the file's, then this process's. ↑, ↓, and ctrl+r see only that view.
- **Following the session.** `SessionOpened` carries the session's settings. When its workspace differs from the one shown, the view is rebuilt for the new workspace and browsing and searching end. `/new`, `/resume`, and the session picker all open a session this way, so `/resume` into another folder shows that folder's history, and `/resume` back shows the first folder's again, with this process's prompts of that folder.
- **Before a session opens.** A prompt sent while the first session opens (the initial prompt, or one typed during startup) has no session ID or workspace yet. uah holds it and writes it when `SessionOpened` arrives, with that session's ID and workspace, and adds it to that workspace's view.
- **Old lines.** A line without `workspace` (Codex's lines, and uah's lines from before item 74) belongs to no folder, so no session shows it. uah does not migrate or rewrite them; the size cap drops them as the file grows.

Claude Code keeps history per project, which is its working directory, and ↑ reaches earlier sessions of the same project. uah does the same with the session's workspace, which is uah's working directory unless `--workspace` or a resumed session names another. uah uses the workspace as given, not its git root: this is the same rule the session picker uses to list "this folder's" sessions, and a subfolder of a repository is its own folder, as in Claude Code. Claude Code's classic ctrl+r searches every project; uah's ctrl+r searches the folder, as ↑ does, so both keys show the same prompts.

## Keys and their conflicts

| Key | Before | Now | Why |
| --- | --- | --- | --- |
| ↑ on an empty composer, with messages queued | Take the last queued message back | The same | Kept first, as the task asked; Claude Code does the same from the first row |
| ↑ on an empty composer | Scroll the transcript (for a wheel sent as ↑ without mouse reporting) | Recall the previous prompt | Codex. With `[tui] mouse` on, the default, the wheel arrives as mouse events and still scrolls |
| ↑ / ↓ on a recalled prompt, cursor at its start or end | Move the cursor or scroll | Recall the previous or next prompt | Codex's `should_handle_navigation` |
| ↑ / ↓ on a draft of your own | Move the cursor; on the first or last row, scroll the transcript | The same | A draft is never replaced by a stray arrow |
| esc esc, then ↑ / ↓ (or k / j) | Choose an earlier or later message to go back to | The same | The selection takes its keys before history |
| shift+↑ / shift+↓, pgup / pgdn, wheel | Scroll | The same | Codex uses shift+↑/↓ for the effort; uah keeps them for scrolling and uses alt+, / alt+. for the effort |
| ctrl+r | Show or hide reasoning summaries | Reverse search | Codex binds ctrl+r to the search and has no key for reasoning. `/reasoning` still shows or hides them |
| ctrl+s | Session picker | The same, except inside the search, where it goes to a newer match | Codex's `history_search_next` only applies while searching |
| ctrl+c on a draft | Clear it | Clear it, and ↑ brings it back | Codex's `clear_for_ctrl_c` |

## Decisions

- **Load the whole file at startup.** Codex reads entries by offset on demand because its file has no cap by default. uah keeps a cap by default (8 MiB), reads the file once off the update loop, and searches in memory. The observable rules (newest first, unique texts, boundaries that keep the match) are Codex's.
- **One history per process, shown per folder.** Codex resets its session entries when another thread starts. uah keeps them across `/new` and `/resume` in the same process, tagged with their workspace, and shows those of the session's workspace; they are in the file by then anyway. Prompts another uah process sends after startup appear after a restart, as in Codex, whose line count is fixed when the thread starts.
- **Record at send time.** Codex writes a queued message to the file when the queue sends it. uah writes it when you press enter, which is when you typed it; a message taken back from the queue and sent again is recorded again.
- **The file keeps placeholders, the process keeps images.** As in Codex, `[Image #1]` in a recalled prompt from the file is text; a prompt of this process brings its images back.
- **No match highlighting in the composer.** The composer is a Bubble Tea textarea, which draws one style for its text. The footer shows the query instead.
- **The composer's height follows the lead's rule, not Codex's.** Codex's composer can take the whole screen because its transcript lives in the terminal's scrollback. uah draws its transcript on the same screen, so the composer stops at half of it, and never below the 8 rows it had.
- **Failures are notices.** A failed read or append shows an error notice, as other failed effects do; Codex logs a warning. uah's TUI logs nothing of its own.
- **No `flock` on other platforms.** uah builds for macOS and Linux, where `flock` exists.
- **The workspace, not the git root** (item 74). The workspace is what the session already records and what the session picker filters by. A git root would need a git lookup for each session and would merge the history of every subfolder of a repository.
- **Exact paths.** Workspaces compare as strings, without resolving symbolic links, so the reducer stays pure. A folder opened through a symlink has its own history.
- **No legacy handling** (item 74). Lines without `workspace` stay in the file and show nowhere; nothing is migrated, as the owner asked.

## Open

- Ctrl+r's scope is the session's workspace (item 74). Claude Code's fullscreen dialog can also scope to the session or to all projects; uah has no key to change the scope.
- Pasted text is not collapsed into placeholders in uah's composer, so nothing like Codex's pending pastes needs restoring.
