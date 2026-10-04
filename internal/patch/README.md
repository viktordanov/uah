<!-- memoria:section id="overview" files="parse.go update.go apply.go diff.go tool.go plain.go" -->
# Patches

This package is Codex's `apply_patch`: the patch format that Codex models are trained to edit files with, its parser, its applier, and the diff of what a patch changed. The embedded engine offers it as a tool (see [the engine](../engine/README.md#the-tool-registry)), and the TUI and `uah sessions show` draw the diff.

<!-- memoria:export id="summary" -->
Models edit files with Codex's `apply_patch` tool: a patch of `*** Add File`, `*** Update File` (with `*** Move to`), and `*** Delete File` sections with `@@` hunks, parsed and applied as Codex does, with its lenient context matching and its messages. The embedded engine applies patches inside the writable roots at once, and asks for any other write as for a Bash escalation; forbid rules refuse first in every mode, and a patch writes only the paths it was approved at, never through a symlink put in since; the diff it records shows under the call in the TUI and in `uah sessions show`.
<!-- /memoria:export -->

The code is ported from Codex `rust-v0.156.1` (`codex-rs/apply-patch`, Apache License 2.0), with the attribution in each file.

1. [The format](#the-format)
2. [Applying a patch](#applying-a-patch)
3. [The diff](#the-diff)
4. [The tool](#the-tool)
5. [Tests](#tests)
<!-- /memoria:section -->

<!-- memoria:section id="format" files="parse.go update.go" -->
## The format

```
*** Begin Patch
*** Add File: hello.txt
+Hello world
*** Update File: src/app.py
*** Move to: src/main.py
@@ def greet():
-print("Hi")
+print("Hello, world!")
*** Delete File: obsolete.txt
*** End Patch
```

`Parse` returns one `Hunk` per file section: `Add` with the new contents, `Delete`, or `Update` with its `Chunk`s. A chunk has an optional `@@` context line, its old and new lines, and `*** End of File` when it must match the end of the file. The parser is Codex's streaming parser fed one line at a time, and it is lenient in the same ways:

- Whitespace around the markers is ignored, and a patch wrapped in a `<<'EOF'` heredoc is unwrapped.
- An update's first chunk may start without `@@`.
- A bare empty line in a chunk is an empty context line.

Errors are Codex's, for example `invalid hunk at line 5, Expected update hunk to start with a @@ context marker, got: 'bad'`.
<!-- /memoria:section -->

<!-- memoria:section id="apply" files="apply.go update.go files.go files_unix.go files_other.go" -->
## Applying a patch

`Compute(cwd, hunks)` works out every change without writing, reading each file as the earlier sections of the same patch leave it. `Targets.Write(changes)` writes them in order, and creates missing parent directories. It is all or nothing: it first reads every file the changes touch, and when a write fails (permissions, a full disk, a file where a directory must go) it puts each file back as it was, removes the files it added, and returns the failure. A parent directory it created stays. A patch that does not apply fails in `Compute`, before anything is written.

A patch writes only where it was approved. `Targets` maps each path the patch names to the path the engine checked: the same path with its symlinks resolved when the patch was approved. `Targets.Compute` and `Targets.Write` read and write each file only at its target, through directory handles (`files_unix.go`): each directory is opened from `/` one name at a time with `O_NOFOLLOW`, missing ones are made with `mkdirat`, and the file is opened, created, or removed relative to its directory's handle, also with `O_NOFOLLOW`. A symlink anywhere in a target, put there after the approval by a command running at the same time, fails the patch (`a symlink or not a directory, and the patch does not follow symlinks`), and the undo puts back what was already written. A path without a target fails too, and a FIFO is refused instead of blocking. This needs Unix (Linux and macOS); elsewhere a confined patch fails. Files are keyed by their target, which the engine spells once (`sandbox.Canonical`), so two spellings of one file in a patch, such as another case on macOS or a path through a symlinked directory, are one file: a second update reads the first one's result, and a move onto the same file under another spelling is an update that never removes it. The engine verifies a patch before asking anyone with `Targets.Compute` on the same targets, so a patch that applies is never refused as unverifiable. `Summary(changes)` is Codex's output: `Success. Updated the following files:`, then `A`, `M`, and `D` lines.

An update finds each chunk as Codex does (`seek_sequence.rs`). It looks after the `@@` context line and after the previous chunk, and tries four matches in turn: exact, then without trailing whitespace, then without surrounding whitespace, then with typographic dashes, quotes, and spaces made ASCII. The matched lines are replaced by the chunk's new lines, and the file ends with a newline. This is Codex's default mode: line endings become LF, and a context line takes the patch's text.

Relative paths resolve against the working directory. `Paths` lists every path a patch writes, move destinations included, for the sandbox check. `LastFile` names the file of the last complete file header in the tail of a patch still being written, where only the line's end ends the path, for the status line while the model writes an `apply_patch` call.
<!-- /memoria:section -->

<!-- memoria:section id="diff" files="diff.go plain.go" -->
## The diff

`Diffs(changes)` turns the changes into `FileDiff`s for display: the op, the path and move path, the added and removed counts, and hunks of `DiffLine`s. Each line has its kind (` `, `+`, or `-`), its old and new line numbers, and its text. An update is diffed with Myers' algorithm, with one line of context around each change, as Codex's TUI shows it. An added or deleted file is one hunk of all its lines.

A diff keeps at most 2,000 lines per file and counts the rest in `Omitted`, so a large new file does not bloat the session file. `Plain` writes diffs as text for `uah sessions show`.
<!-- /memoria:section -->

<!-- memoria:section id="tool" files="tool.go apply_patch.lark" -->
## The tool

`apply_patch` is a custom tool, as Codex offers it to the models whose catalog entry has `apply_patch_tool_type`: its input is the raw patch, which the provider samples from Codex's Lark grammar, with Codex's description, both verbatim (`Description`, `Grammar` from `apply_patch.lark`). `ParseArgs` reads a call's patch: input that starts with `*** Begin Patch` is the patch, else it is the `{"input": patch}` of a call recorded when `apply_patch` was a function tool, which resumed sessions still hold.

Hooks see the call as Codex shows it to them: `tool_name` `apply_patch` and `tool_input` `{"command": "<patch>"}`, plus `file_path` and `file_paths` for Claude Code-style scripts (`HookInput`). A matcher of `apply_patch`, `Edit`, or `Write` matches it (`HookAliases`). A PreToolUse hook's `updatedInput` replaces the patch through its `command`, or `input` (`FromHookInput`). `Describe` names the files for a tool line.
<!-- /memoria:section -->

<!-- memoria:section id="tests" files="parse_test.go apply_test.go diff_test.go tool_test.go lastfile_test.go targets_test.go" -->
## Tests

`parse_test.go` and `apply_test.go` port Codex's cases: every op, context and `@@` chunks, `*** End of File`, moves, the fuzzy matches, pure additions, and the error messages; `TestWrite_UndoesOnFailure` checks that a write failing partway puts every file back. Every case writes through `Targets`. `targets_test.go` swaps a directory or the file for a symlink between the approval and the write (absolute, relative, dangling, to the parent, a directory the patch creates, a deleted file's directory, a move's destination after an earlier write): each patch fails, nothing is written where the symlink leads, and the earlier write is undone; a path without a target fails. `diff_test.go` pins line numbers, context, hunk breaks, and a large rewrite that stays bounded. `lastfile_test.go` covers `LastFile` with quotes in a path and an unfinished path. `tool_test.go` reads a raw patch and a recorded function call's `{"input": patch}` through `ParseArgs`, `Describe`, and `HookInput`, reads a hook's `updatedInput`, and pins the grammar to Codex's file. The engine's tests apply patches end to end (`internal/engine/embedded/patch_test.go`). `TestTargets_TwoSpellingsOfOneFile` moves a file onto itself under another spelling and updates it through two spellings.
<!-- /memoria:section -->
