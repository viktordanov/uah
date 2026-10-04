# Selecting and copying text

Status: built (ledger item 48). The package READMEs hold the current contract; this record keeps the research and the decisions.

Ledger item 48: select and copy transcript text inside the TUI while it reports the mouse. A drag selects, and the selection follows the transcript as it scrolls or streams. A double click selects a word, a triple click a line, and letting go copies to the system clipboard. The wheel keeps scrolling.

1. [The problem](#the-problem)
2. [What Codex does](#what-codex-does)
3. [What other terminal programs do](#what-other-terminal-programs-do)
4. [The design](#the-design)
5. [Decisions](#decisions)
6. [Open](#open)

## The problem

A terminal program that reports the mouse gets the wheel and the clicks, and the terminal stops selecting text; the user must hold Option (iTerm2, Terminal) or Shift (most others) to select. A program that leaves the mouse alone gets the terminal's selection, but in the alternate screen the terminal turns the wheel into ↑ and ↓ ("alternate scroll"). uah drew with the mouse off by default (`[tui] mouse = false`): the wheel arrived as ↑ and ↓, which scrolled the transcript but moved the cursor in a multi-line prompt, and the terminal's selection crossed the λ column, the band's padding, and the footer. With `mouse = true`, the wheel scrolled, and selecting needed the modifier.

## What Codex does

Checked against Codex rust-v0.156.1. Paths are under `codex-rs/tui/src/`.

- **Two transcript modes** (`transcript_mode.rs`). By default the transcript goes to the terminal's scrollback and the TUI does not capture the mouse; only overlays that ask for it do (`OverlayInput::captures_mouse` in `tui.rs`: the transcript and usage overlays yes, pagers no). `[tui] fullscreen_transcript = true` ("Own the fullscreen transcript, including scrolling, selection, and search. Defaults to `false`", `config/src/types.rs`) makes the TUI own the alternate screen and capture the mouse (`Tui::set_owned_screen`, `configure_input(…, capture_mouse = true)`).
- **Selection** (`text_selection.rs`, `transcript_view/selection.rs`, `transcript_view/input.rs`). One click places, two select a word (Unicode word bounds), three a logical line; clicks count when they land on the same cell within 400 ms, and a fourth starts over. A drag extends the selection. Positions are anchors in each history cell's source text (an entry and a byte offset), and a snapshot freezes the entries while history keeps advancing, so the selection stays on its text. The wheel scrolls 3 rows and "supersedes the last drag position".
- **Copy** is not on release: ctrl+c, cmd+c (kitty's super), ctrl+shift+c, enter (which also jumps back to the latest output), or a right click copy the selection (`is_copy_key`, `MouseButton::Right`, `transcript_view/input.rs`). `/copy` and ctrl+o copy the last answer.
- **The clipboard** (`clipboard_copy.rs`): the native clipboard through `arboard` (kept open on Linux, where X11 and some Wayland compositors need the writing process alive), with WSL's PowerShell as a fallback. In tmux it also forwards to the attached terminal, and over ssh without tmux it sends OSC 52 directly; locally, OSC 52 only when the native copy fails. Payloads over 100,000 bytes skip OSC 52. A terminal write is unacknowledged, so the notice says "Copy unconfirmed" when only OSC 52 went out.

## What other terminal programs do

Checked on 2026-09-25.

- **Claude Code**, fullscreen rendering ([Fullscreen rendering](https://code.claude.com/docs/en/fullscreen), a research preview, and the default renderer for most who first used Claude Code on or after May 6, 2026). It captures the mouse: click and drag selects anywhere in the conversation, a double click a word ("matching iTerm2's word boundaries so a file path selects as one unit", a whole URL), a triple click the line, and the wheel scrolls. "Selected text copies to your clipboard automatically on mouse release"; Copy on select in `/config` turns that off, and then ctrl+shift+c (or cmd+c with the kitty protocol, or ctrl+c with a selection) copies. Locally it runs pbcopy, wl-copy, xclip, or xsel (also the PRIMARY selection), inside tmux it also fills the paste buffer, and over ssh it falls back to OSC 52; a toast names the path. Esc keeps the selection; most other keys clear it. `CLAUDE_CODE_DISABLE_MOUSE=1` gives the terminal its selection back, and `CLAUDE_CODE_DISABLE_MOUSE_CLICKS=1` keeps only the wheel.
- **opencode** v1.18.32 (`anomalyco/opencode`, `packages/tui`). `[tui] mouse` captures the mouse, "default: true" (`src/config/index.tsx`). The renderer (opentui) selects; the selection copies on mouse up and then clears (`src/app.tsx`, `src/util/selection.ts`), with a "Copied to clipboard" toast; an experimental flag switches to ctrl+c and a right click. `src/clipboard.ts` writes OSC 52 (wrapped for tmux and screen) and then the native tool: osascript, wl-copy, xclip, xsel, or PowerShell.
- **zellij** v0.45.1 (`zellij-utils/assets/config/default.kdl`). `mouse_mode` is on by default; `copy_on_select` (default true) copies and clears the selection on release. It copies with OSC 52 unless `copy_command` names a tool such as `pbcopy`, `wl-copy`, or `xclip -selection clipboard`; `copy_clipboard` picks the system clipboard or the primary selection.
- **helix** 25.07.1 (`book/src/editor.md`). `editor.mouse` is on by default; a drag makes an editor selection, which copies only by yanking it (to the clipboard register with space y), and middle-click pastes.
- **lazygit** v0.65.1 (`docs/Config.md`). `gui.mouseEvents` is on by default and captures the mouse for clicks and the wheel; it selects no text itself, and "it's a little harder to select text: e.g. requiring you to hold the option key when on macOS".

The programs whose view is a transcript (Claude Code, opencode, zellij's panes) capture the mouse by default, select inside, and copy on release with both OSC 52 and a native tool. Codex keeps its opt-in view's copy on a key.

## The design

**Mouse on by default.** `[tui] mouse` defaults to true now, so the wheel scrolls everywhere (also over a multi-line prompt) and a drag selects inside the TUI. `mouse = false` is the escape hatch: the terminal selects as usual and turns the wheel into ↑ and ↓, as before. The terminal's own selection also stays one modifier away. A trusted project file can set it either way (an override that can unset, like `ignore_default_excludes`).

**Gestures.**

| Gesture | Does |
| --- | --- |
| press and drag | Select from the pressed cell to the cell under the mouse, both included, in either direction |
| double click | Select the word under the mouse: a run of non-space, counted in cells |
| triple click | Select the line; a fourth click starts over |
| release | Copy what is selected, and show `copied 3 lines` in the footer for two seconds; the selection stays |
| drag to the top row, or below the transcript | Scroll one line that way per move, and select to the edge |
| wheel during a drag | Scroll, and move the selection's end to the text now under the mouse |
| esc | Clear the selection, and nothing else |
| a click, typing, sending (also `/reasoning`), ctrl+t | Clear the selection, and do what they do |

A plain click selects nothing. Clicks count as a double or triple click when they land on the same cell within 500 ms. The composer, the panels, the picker, and the agent view take no selection; a press there clears it.

**Anchoring** (`state/selection.go`). A position is a `TextPos`: an item's key, a line of the item as drawn, and a cell. Keys do not change while the transcript scrolls or grows, and new output goes below, so a selection stays on its text while an answer streams. A selection whose item leaves (a rewind, `/clear`) selects nothing. The reducer stays pure: the shell passes the pressed line's text with the press (for the word) and the press time (for the count), and the reducer returns `EffCopySelection`.

**Mapping the mouse** (`render/selection.go`). Each frame records which transcript line every row of the window shows, one `TextPos` per row, and where the transcript starts on screen. `Cache.At` turns a screen cell into a position, and `Cache.Edge` tells a drag past the top or bottom. Dragging past the edge scrolls first, lays out the frame again, and then takes the edge row, so the head is always a line that was drawn.

**Drawing.** The selection is a pass over the window's lines after the cache, as the backtrack fade is (`render/backtrack.go`): O(height) per frame, and no item's Markdown is drawn again. The selected cells lose their styles and sit on the theme's `Selection` background (a dark amber in `Amber`, a pale one in `AmberLight`) in the terminal's own text color; the styles in force after the selection are replayed, so the rest of the line keeps its look.

**What a copy holds** (`render.SelectedText`, `render/copytext.go`). What is drawn, cut by cells, with a wide character that is half inside counted whole, minus the decoration:

- the item's gutter: the λ or ! column of your message and a shell command and the indent under it, the answer's • column and its hanging indent, reasoning's `~`, a warning's or an error's mark;
- the indent a block shares: each run of lines between blank lines loses the spaces all of its lines have after the gutter. A code block loses the band's padding and a diff block the padding on its tints, so they paste as code (a block indented as a whole comes out dedented); a table loses the zebra's padding, banded rows and plain ones alike; tool rows and the finish line lose their two-space indent;
- a code block's first line: the fence's language, dim at its right end;
- a quote: its `“` and `”` marks and the indent under the first, so it pastes as its wrapped text;
- every line: its trailing spaces; the copy: blank lines at either end.

What stays: list markers (`•`, `◦`, `1.`) and the indent of a nested item, an alert's title (`! Warning`) and the indent of its text, a heading as drawn (an H1 in capitals), the table's columns as spaced on screen, and the banner's box. The copy is what the user sees rather than the item's source Markdown, even for whole items, so the rule is predictable: a wrapped paragraph copies as its wrapped lines, as a terminal's own selection would.

**The clipboard** (`clipboard_copy.rs`): the native clipboard through `arboard` (kept open on Linux, where X11 and some Wayland compositors need the writing process alive), with WSL's PowerShell as a fallback. In tmux it also forwards to the attached terminal, and over ssh without tmux it sends OSC 52 directly; locally, OSC 52 only when the native copy fails. Payloads over 100,000 bytes skip OSC 52. A terminal write is unacknowledged, so the notice says "Copy unconfirmed" when only OSC 52 went out.

## What other terminal programs do

Checked on 2026-09-25.

- **Claude Code**, fullscreen rendering ([Fullscreen rendering](https://code.claude.com/docs/en/fullscreen), a research preview, and the default renderer for most who first used Claude Code on or after May 6, 2026). It captures the mouse: click and drag selects anywhere in the conversation, a double click a word ("matching iTerm2's word boundaries so a file path selects as one unit", a whole URL), a triple click the line, and the wheel scrolls. "Selected text copies to your clipboard automatically on mouse release"; Copy on select in `/config` turns that off, and then ctrl+shift+c (or cmd+c with the kitty protocol, or ctrl+c with a selection) copies. Locally it runs pbcopy, wl-copy, xclip, or xsel (also the PRIMARY selection), inside tmux it also fills the paste buffer, and over ssh it falls back to OSC 52; a toast names the path. Esc keeps the selection; most other keys clear it. `CLAUDE_CODE_DISABLE_MOUSE=1` gives the terminal its selection back, and `CLAUDE_CODE_DISABLE_MOUSE_CLICKS=1` keeps only the wheel.
- **opencode** v1.18.32 (`anomalyco/opencode`, `packages/tui`). `[tui] mouse` captures the mouse, "default: true" (`src/config/index.tsx`). The renderer (opentui) selects; the selection copies on mouse up and then clears (`src/app.tsx`, `src/util/selection.ts`), with a "Copied to clipboard" toast; an experimental flag switches to ctrl+c and a right click. `src/clipboard.ts` writes OSC 52 (wrapped for tmux and screen) and then the native tool: osascript, wl-copy, xclip, xsel, or PowerShell.
- **zellij** v0.45.1 (`zellij-utils/assets/config/default.kdl`). `mouse_mode` is on by default; `copy_on_select` (default true) copies and clears the selection on release. It copies with OSC 52 unless `copy_command` names a tool such as `pbcopy`, `wl-copy`, or `xclip -selection clipboard`; `copy_clipboard` picks the system clipboard or the primary selection.
- **helix** 25.07.1 (`book/src/editor.md`). `editor.mouse` is on by default; a drag makes an editor selection, which copies only by yanking it (to the clipboard register with space y), and middle-click pastes.
- **lazygit** v0.65.1 (`docs/Config.md`). `gui.mouseEvents` is on by default and captures the mouse for clicks and the wheel; it selects no text itself, and "it's a little harder to select text: e.g. requiring you to hold the option key when on macOS".

The programs whose view is a transcript (Claude Code, opencode, zellij's panes) capture the mouse by default, select inside, and copy on release with both OSC 52 and a native tool. Codex keeps its opt-in view's copy on a key.

## The design

**Mouse on by default.** `[tui] mouse` defaults to true now, so the wheel scrolls everywhere (also over a multi-line prompt) and a drag selects inside the TUI. `mouse = false` is the escape hatch: the terminal selects as usual and turns the wheel into ↑ and ↓, as before. The terminal's own selection also stays one modifier away. A trusted project file can set it either way (an override that can unset, like `ignore_default_excludes`).

**Gestures.**

| Gesture | Does |
| --- | --- |
| press and drag | Select from the pressed cell to the cell under the mouse, both included, in either direction |
| double click | Select the word under the mouse: a run of non-space, counted in cells |
| triple click | Select the line; a fourth click starts over |
| release | Copy what is selected, and show `copied 3 lines` in the footer for two seconds; the selection stays |
| drag to the top row, or below the transcript | Scroll one line that way per move, and select to the edge |
| wheel during a drag | Scroll, and move the selection's end to the text now under the mouse |
| esc | Clear the selection, and nothing else |
| a click, typing, sending (also `/reasoning`), ctrl+t | Clear the selection, and do what they do |

A plain click selects nothing. Clicks count as a double or triple click when they land on the same cell within 500 ms. The composer, the panels, the picker, and the agent view take no selection; a press there clears it.

**Anchoring** (`state/selection.go`). A position is a `TextPos`: an item's key, a line of the item as drawn, and a cell. Keys do not change while the transcript scrolls or grows, and new output goes below, so a selection stays on its text while an answer streams. A selection whose item leaves (a rewind, `/clear`) selects nothing. The reducer stays pure: the shell passes the pressed line's text with the press (for the word) and the press time (for the count), and the reducer returns `EffCopySelection`.

**Mapping the mouse** (`render/selection.go`). Each frame records which transcript line every row of the window shows, one `TextPos` per row, and where the transcript starts on screen. `Cache.At` turns a screen cell into a position, and `Cache.Edge` tells a drag past the top or bottom. Dragging past the edge scrolls first, lays out the frame again, and then takes the edge row, so the head is always a line that was drawn.

**Drawing.** The selection is a pass over the window's lines after the cache, as the backtrack fade is (`render/backtrack.go`): O(height) per frame, and no item's Markdown is drawn again. The selected cells lose their styles and sit on the theme's `Selection` background (a dark amber in `Amber`, a pale one in `AmberLight`) in the terminal's own text color; the styles in force after the selection are replayed, so the rest of the line keeps its look.

**What a copy holds** (`render.SelectedText`). What is drawn, cut by cells, with a wide character that is half inside counted whole, minus the decoration:

- your message and a shell command: the λ or ! column and the indent under it; the band's rows above and below are empty lines;
- the agent's answer: the • column and its hanging indent; a code line (a background that starts after the indent: the band, a diff tint, a banded table row) also loses the padding before its text, and a code block's first line the fence's language at its right end, so a code block pastes as code;
- reasoning: the `~` column; a warning or an error: its mark;
- every line: its trailing spaces; the copy: blank lines at either end.

Tool rows, the banner, and the finish line copy as drawn. It copies what the user sees rather than the item's source text, even for whole items, so the rule is predictable: a wrapped paragraph copies as its wrapped lines, as a terminal's own selection would.

**The clipboard** (`bubble/mouse.go`). The shell writes the text twice, off the update loop: OSC 52 through `term.SetClipboard` (Bubble Tea's `tea.SetClipboard` until 1.8.4), which reaches the local clipboard from a remote shell, and the system's tool through `Deps.CopyText` (`internal/images/clipboard.TextWriter`: pbcopy on macOS, wl-copy in a Wayland session, `xclip -selection clipboard` in an X11 one). A missing tool is skipped silently, and OSC 52 alone remains. Tests inject `CopyText`, so no test touches a real clipboard.

## Decisions

- **Copy on release, not on a key.** The item asks for it, and Claude Code, opencode, and zellij do the same by default. Codex copies on ctrl+c or a right click; in uah ctrl+c already clears the composer and quits, and copying at once saves a step.
- **Mouse on by default.** Selecting inside the TUI takes away the reason to leave the mouse to the terminal: with it on, the wheel scrolls also over a multi-line prompt, and copying still works. Claude Code's fullscreen view, opencode, zellij, helix, and lazygit capture the mouse by default too. Codex keeps it off by default because its default transcript lives in the terminal's scrollback, which uah, drawing in the alternate screen, does not use.
- **Both OSC 52 and the native tool.** OSC 52 needs the terminal's support, which some terminals lack or put behind a setting (iTerm2 blocks it until "Applications in terminal may access clipboard" is on), and gives no acknowledgement; the native tool does not reach a local clipboard over ssh. Writing both covers both, as opencode does; Codex and Claude Code pick one by where they run.
- **Positions in drawn lines, not source offsets.** Codex anchors in source text and freezes a snapshot. uah's items keep their keys, and the renderer's cache already holds each item's drawn lines, so a line index is stable except when the width or the view changes; ctrl+t and `/reasoning` (ctrl+r before the prompt history took the key) clear the selection for that reason.
- **No selection in the composer.** The textarea keeps its own editing, and a press there clears the transcript's selection.

## Open

- **A resize** redraws the items at the new width, so line indexes may name other text; the selection is not cleared.
- **The agent view** takes no selection; it draws another state with its own cache.
- **A held drag past the edge** scrolls only while the mouse moves, since the terminal reports motion only on a new cell; there is no timer.
- **tmux** passes OSC 52 only with `set-clipboard on`; the native tool still copies on the machine uah runs on.
- **Esc** clears the selection here and does nothing else; Claude Code keeps the selection and lets esc interrupt. uah's esc esc still interrupts once the selection is gone.
- **Keyboard extension** (Claude Code's shift+arrows) and copying the whole answer (Codex's `/copy`) are not built.
