# Editing the prompt in an editor (ctrl+g)

Status: built (ledger item 61). The package READMEs hold the current contract; this record keeps the research and the decisions.

Ledger item 61: ctrl+g opens the composer's draft in the user's editor, and the saved text comes back as the draft. Image placeholders keep their images, and a multi-line draft comes back unchanged.

1. [What Claude Code does](#what-claude-code-does)
2. [What Codex does](#what-codex-does)
3. [The design](#the-design)
4. [Decisions](#decisions)
5. [Open](#open)

## What Claude Code does

Checked against the Claude Code documentation on 2026-09-29 and Claude Code 2.1.284.

- **The key.** Ctrl+G, or the readline chord Ctrl+X Ctrl+E, "Open in default text editor": "Edit your prompt or custom response in your default text editor" ([interactive mode, keyboard shortcuts](https://code.claude.com/docs/en/interactive-mode)). The action is `chat:externalEditor` and can be rebound ([keybindings](https://code.claude.com/docs/en/keybindings)).
- **The editor.** The pages say "default text editor" for the prompt and "`$VISUAL` or `$EDITOR`" for the transcript viewer's `v`. They do not name a fallback when neither is set; the transcript viewer says "no $VISUAL/$EDITOR set" in that case.
- **Extras.** With **Show last response in external editor** on in `/config`, Claude's last reply goes above the prompt as `#` comment lines, which Claude Code strips when the file is saved.
- The documentation does not say how the file is named, where it lives, or what a failed editor does.

## What Codex does

Checked against Codex rust-v0.156.1. Paths are under `codex-rs/tui/src/`.

- **The key.** `open_external_editor`, ctrl+g by default (`keymap.rs:1649`), configurable as `tui.keymap.global.open_external_editor`. It works when no popup or modal view is open (`bottom_pane/mod.rs:1731`, `can_launch_external_editor`); a running task does not block it. While the editor is open the footer says "Save and close external editor to continue." (`app.rs:290`).
- **The editor** (`external_editor.rs`, `resolve_editor_command`): `$VISUAL`, else `$EDITOR`, split with `shlex` (`winsplit` on Windows). With neither set it opens nothing and says "Cannot open external editor: set $VISUAL or $EDITOR before starting Codex." (`app/input.rs:241`). An empty command is an error.
- **The file** (`run_editor`): a temporary file with the `.md` suffix, in `<codex home>/editor`, a directory it refuses when the sandbox policy can write it (`editor_directory`), so a sandboxed command cannot read or change the draft. The editor runs with the terminal's stdin, stdout, and stderr, with the TUI's terminal restored around it (`tui.with_restored`). The file is removed afterwards (a `TempPath`).
- **The result** (`app/input.rs:274-286`):
  - A non-zero exit is an error: "Failed to open editor: editor exited with status …" in the transcript, and the composer keeps its draft.
  - Otherwise the text, with all trailing whitespace trimmed (`trim_end`), replaces the draft (`chat_composer.rs:1229`, `apply_external_edit`). An empty file gives an empty composer.
  - Attached images are kept while their `[Image #N]` placeholder occurs in the new text, one image per occurrence; the others drop. Pending large-paste placeholders are cleared.

## The design

- **State** (`internal/tui/state/editor.go`). ctrl+g is `EditDraft{Draft}`, and the reducer returns `EffEditDraft{Text}` in the chat view, also while the agent works; the picker, the approval overlay, and `/config` do not take ctrl+g. The result is `DraftEdited{Text, Err}`:
  - With an error, the draft and its images stay, and a warning says `editor: vim: exit status 1; the draft is unchanged`.
  - Otherwise `\r\n` becomes `\n`, exactly one trailing newline goes (the one editors add to the last line), and the text replaces the draft through `EffSetDraft`. `pruneImages`, which `DraftChanged` uses, drops the images whose placeholder is gone. A placeholder typed in the editor that names no attached image is text.
- **Shell** (`internal/tui/bubble/editor.go`). `EffEditDraft` becomes a `term.Exec` of an `editorRun` (a `tea.Exec` until 1.8.4, when uah ran on Bubble Tea): `term` releases the terminal (the alt screen, the mouse, bracketed paste, its input reader), the run writes the draft to `<uah home>/editor/prompt-*.md` (the directory 0700, the file 0600), runs the editor on it with the terminal's input and output, reads the file back, and removes it; then `term` restores the terminal and repaints the whole screen. `Deps.Exec` replaces `term.Exec` in tests, which have no program to release.
- **The sandbox check** (`draftDirExposed`), as Codex's `editor_directory`: before it writes anything, the shell builds the session's sandbox policy (the permission mode and workspace from the state, the writable roots from `Deps.WritableRoots`) and refuses when a sandboxed command could write `<uah home>/editor` or its parent, or a writable root lies inside it. The draft stays, and the warning names the directory and says to move `$UAH_HOME` out of the workspace, `/tmp`, `$TMPDIR`, and the writable roots. Full access has no sandbox, so it is always allowed. `~/.uah` is never exposed: read-only writes nothing, and workspace-write keeps a `.uah` directory read-only inside every writable root, also when the workspace is the user's home. Only a `$UAH_HOME` elsewhere inside a writable root is refused. The editor directory must be a directory, not a symbolic link. Before the first session opens, the mode is unknown and counts as workspace.
- **The editor command** (`editorCommand`): `$VISUAL`, else `$EDITOR`, split into words as a shell would with `mvdan.cc/sh/v3/shell.Fields` (quotes, backslashes, and `$VARS`; `code --wait`, `nvim -f`), else `vim`, or `vi` when vim is not on the path. The file's path is the last argument.
- **Shell mode.** In `!` mode the draft is the command, and ctrl+g edits it the same way; the composer stays in shell mode.

## Decisions

- **A default editor.** The ledger item asks for vim by default. Codex refuses to open anything without `$VISUAL` or `$EDITOR`; uah falls back to vim, or vi, as git and most Unix tools do, so ctrl+g works on a fresh machine.
- **uah's home, not the temporary directory.** uah's workspace-write sandbox writes `/tmp` and `$TMPDIR`, so a sandboxed command could read or change a draft there while the editor is open. As Codex does, the file goes in a directory the sandbox cannot write, `<uah home>/editor`, and ctrl+g refuses when the policy could write it.
- **One trailing newline, not all trailing whitespace.** Codex trims all of it; uah strips only the newline the editor adds, so a draft saved unchanged is the same draft, trailing blank lines and spaces included.
- **Images by placeholder.** Labels are unique in uah (`[Image #N]` is never reused while its image is attached), so "the placeholder is still in the text" is the whole rule; a placeholder repeated in the editor keeps its one image.
- **No hint while the editor is open.** Codex draws its footer hint because it runs inline; uah's alt screen is released while the editor runs, so there is no screen to draw it on.

## Open

- ctrl+x ctrl+e, Claude Code's readline chord, is not bound: uah has no chords.
- Claude Code's option to show the last answer as comments above the prompt is not built.
- ctrl+g is not configurable; no uah key is.
- The check takes the writable roots `cmd/uah` resolved at start. A session opened later in another workspace uses its own workspace and mode but those roots, which can differ when that project's configuration adds roots.
