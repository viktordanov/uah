<!-- memoria:section id="overview" files="sandbox.go shell.go" -->
# Sandbox

The sandbox package runs shell commands inside the operating system's sandbox, as Codex does: Seatbelt (`/usr/bin/sandbox-exec`) on macOS and bubblewrap (`bwrap`, which must be installed) on Linux. It decides what a command may write and whether it has network; it does not decide whether a command runs at all, which is [the approver's job](../approval/README.md).

<!-- memoria:export id="summary" -->
Commands run in the operating system's sandbox, as in Codex: Seatbelt on macOS and bubblewrap on Linux. The default mode, workspace-write, lets commands read the whole disk and write only the workspace and temporary directories, without network, and keeps .git, .uah, .agents, and .codex read-only.
<!-- /memoria:export -->

The profile and the bubblewrap layout are adapted from Codex rust-v0.156.1 (Apache-2.0; see `seatbelt/LICENSE-codex`). The decisions are recorded in the [sandbox plan](../../docs/design/sandbox.md), and the keys are in the [configuration reference](../../docs/configuration.md#sandbox-and-approvals).

1. [Modes](#modes)
2. [How a command is sandboxed](#how-a-command-is-sandboxed)
3. [Platforms](#platforms)
4. [Environment](#environment)
5. [Tests](#tests)
<!-- /memoria:section -->

<!-- memoria:section id="modes" files="sandbox.go gitdir.go canwrite.go" -->
## Modes

The mode comes from `--sandbox`, `UAH_SANDBOX`, or `sandbox_mode`. The names are Codex's.

| Mode | Commands can |
| --- | --- |
| `workspace-write` (default) | Read any file. Write the workspace, `/tmp`, `$TMPDIR`, the session's temporary directory, and `writable_roots`, except the protected paths. No network unless `network_access = true` |
| `read-only` | Read any file; write only the session's temporary directory; no network |
| `danger-full-access` | Anything the user can: no sandbox. Only yolo mode (`--yolo`) runs in it; `sandbox_mode` and `--sandbox` refuse the name |

`Policy.Writable` returns the writable roots with symlinks resolved. `Protected` returns the paths that stay read-only inside each root: `.git`, `.uah`, `.agents`, and `.codex` (and `.uagent`, the old project directory, until it is moved), and the directory a worktree's `.git` file points to. They are protected because a sandboxed command could otherwise plant code that runs later outside the sandbox, such as a git hook. So `git commit` needs an escalation.

`Policy.ReadOnly` adds absolute paths that stay read-only in the same way inside any root that holds them, and whose directories between the root and the path cannot be renamed, so a command cannot move one aside and put its own in its place. A writable root inside a ReadOnly path is dropped from `Writable`, so it cannot open part of the path again. Both checks also see other names: Seatbelt compares names without case, so a root spelled `STATE` holds `state/sandbox` (the path is protected under the root's spelling) and a root spelled `SANDBOX` is dropped; a directory that is the same file under another name counts too. The directory of the sandboxing scripts is one (see [below](#how-a-command-is-sandboxed)): app setup and the embedded engine add it, and `Shell` adds its own directory to every policy it wraps.

`CanWrite(path)` is the check `apply_patch` uses: `ResolvePath` resolves the path's symlinks (a dangling one is followed), then `CanWriteResolved` checks the resolved path against the roots and the protected and ReadOnly paths. A protected path also covers its other names: names are compared without case (macOS's default file system and Linux's casefold directories ignore it, so `.GIT` is `.git`), and a path whose existing directories include the protected directory itself under another name, such as another Unicode normalization, is refused by the directory's identity. Seatbelt and bubblewrap see the real file, so this matters only for writes uah makes itself. A caller that writes the resolved path itself, without following symlinks, checks with `CanWriteResolved`, so the path it checked is the path it writes.

### The session's temporary directory

`Policy.TempDir` is the session's private temporary directory. The embedded engine sets it to `sessions/operations/<id>/tmp` (`session.TempDir`), so removing the session removes it, and each subagent has its own. It is writable in both sandboxed modes. Without it, a read-only command could write nothing at all: zsh heredocs (`can't create temp file for here document`), fish's `psub`, and `go test` (`go: creating work dir`) failed. `Shell` creates it with mode 0700 and sets `TMPDIR`, `TMP`, and `TEMP` to it, and zsh's `TMPPREFIX` (where zsh makes heredoc files) to `<dir>/zsh`, also over a `shell_environment_policy` that sets or drops them. macOS's `/bin/bash` 3.2 ignores `TMPDIR` for heredocs, so they still fail there in read-only mode.

It applies in every mode, unsandboxed ones included: a command escalated out of the sandbox, and every command in yolo mode, gets the same `$TMPDIR`, so a file one command writes there is where the next one looks. Nothing is special-cased per tool: Go puts its work directory under `$TMPDIR` by itself, and a cache that cannot be written elsewhere, such as `GOCACHE` in read-only mode, can be pointed at it. The model is told so in the prepared context (`internal/contextprep/sandbox.go`).

The user's own `!` commands (`internal/usershell`) have no session directory and keep the user's `$TMPDIR`.
<!-- /memoria:section -->

<!-- memoria:section id="shell" files="shell.go denied.go sandbox.go" -->
## How a command is sandboxed

`Policy.Wrap(argv)` returns the command line that runs `argv` under the policy. `Shell` builds on it: it writes a small script to `<state>/sandbox/sh-<hash>` that execs the sandbox around the real shell, so `<script> -c <command>` runs `<sandbox> <real shell> -c <command>`. Scripts are named by their content (`writeScript`), so a session reuses one and a changed policy gets a new one.

The scripts run outside the sandbox (they start it), so a sandboxed command must never change one: the next command would run the changed script unsandboxed. Three things keep them out of reach. They live in the state directory (`<state>/sandbox`), never in a run's own directory: `uah exec --ephemeral` keeps its session under `$TMPDIR`, which workspace-write commands write. `Shell` adds the directory to the policy's `ReadOnly` paths, so it stays read-only also where the state directory is inside a writable root (`UAH_HOME` under `$TMPDIR`, a workspace inside uah's home). `Shell` resolves the directory's symlinks and returns the script by that path, so a symlink on the way (a `UAH_HOME` that is a link inside the workspace) cannot be swapped to lead the next command elsewhere. And a script is not trusted because it exists: `Shell` makes the directory private (0700, the user's own), and reuses a script only when it is a regular file of the user's, mode 0700, with one link and exactly the content it would write; anything else is written again (a temporary file renamed over it).

The engine's Bash has two translators: one with the sandboxing script as its shell and one with the real shell. The approver picks one per command. When a sandboxed command fails and `Denied` says the output looks like a sandbox denial (Codex's keywords, plus uah's network errors), the model is told it can ask to run the command outside the sandbox. A resumed session renders each output again, also after a changed policy or a new version gave the sandbox a new script, so the engine tells a sandboxed command by its recorded shell: a script of the run, or an older one whose header `ScriptMode` reads. The output then stays the same, and so does the history a saved compaction covers.

When the platform has no sandbox, `Wrap` returns `ErrUnavailable`. The engine then asks for approval for every command that no rule allows.
<!-- /memoria:section -->

<!-- memoria:section id="platforms" files="seatbelt.go bwrap.go wrap_darwin.go wrap_linux.go wrap_other.go seatbelt/base.sbpl seatbelt/network.sbpl seatbelt/preferences.sbpl" -->
## Platforms

| Platform | File | How |
| --- | --- | --- |
| macOS | `seatbelt.go`, `wrap_darwin.go`, `seatbelt/*.sbpl` | `sandbox-exec -p <profile> -D...`: Codex's base profile, full-disk read, writes under each writable root except its protected paths, and the network rules. Paths go in as `-D` parameters, never into the profile text. Only `/usr/bin/sandbox-exec` is used, never one on `PATH` |
| Linux | `bwrap.go`, `wrap_linux.go` | `bwrap` from `PATH`: the disk read-only, each writable root bound writable with its protected paths bound read-only over it (each directory between the root and a `ReadOnly` path is first bound over itself: a mount point cannot be renamed), new user, PID, and IPC namespaces, a new network namespace (no network) unless the policy allows it, and all capabilities dropped. Where a container forbids mounting `/proc`, the layout leaves it out, as Codex does |
| Other | `wrap_other.go` | No sandbox (`ErrUnavailable`) |

On Linux, a protected name that does not exist yet, such as `.git` in a workspace that is not a repository root, is not protected: bubblewrap can only mount over existing paths. Codex creates an empty directory for it, which breaks git in a subdirectory of a repository, so uah does not. Seatbelt protects missing names.
<!-- /memoria:section -->

<!-- memoria:section id="environment" files="env.go shell.go" -->
## Environment

Commands get the whole environment, as in Codex. `EnvPolicy` is Codex's `[shell_environment_policy]`: `inherit` (`all`, `core`, `none`), `ignore_default_excludes = false` to drop `*KEY*`, `*SECRET*`, and `*TOKEN*`, then `exclude`, `set`, and `include_only`, in Codex's order. When the policy is not the default, the script starts with `env -i` and copies each kept variable as `NAME="$NAME"` when the command runs, so no inherited value is written to disk. With a `TempDir`, the script then sets `TMPDIR`, `TMP`, `TEMP`, and `TMPPREFIX` into it, whatever the policy says about them.
<!-- /memoria:section -->

<!-- memoria:section id="tests" files="seatbelt_test.go bwrap_test.go shell_test.go denied_test.go env_test.go wrap_darwin_test.go wrap_linux_test.go canwrite_test.go" -->
## Tests

| Test | Pins |
| --- | --- |
| `seatbelt_test.go`, `bwrap_test.go` | The profile and the bubblewrap arguments for each mode, against golden files in `testdata` |
| `wrap_darwin_test.go`, `wrap_linux_test.go` | Real sandboxed commands: writes inside and outside the roots, protected paths, network, exit codes, and inherited file descriptors, and a read-only command that writes only its `$TMPDIR`: zsh heredocs, fish's `psub`, `mktemp`, and `go build` work. macOS's `/bin/bash` 3.2 still makes heredoc files outside `$TMPDIR`, so its heredocs fail in read-only mode. Each runs only on its platform |
| `shell_test.go`, `env_test.go`, `denied_test.go` | The script, a changed, symlinked, or open script and an open directory replaced, a script's mode read back from its header after the policy changed (never from a FIFO or another file), the environment policy, the temporary directory's variables over it, and the denial heuristic |
| `TestSeatbeltShellScriptsStayReadOnly`, `TestLinuxShellScriptsStayReadOnly`, `canwrite_test.go` | A sandboxed command cannot replace, remove, or add a script, or rename the scripts' directory or a directory above it, when it is under `$TMPDIR`; `CanWrite` refuses a `ReadOnly` path |

The package has build-tagged halves, so lint runs for both linux and darwin.
<!-- /memoria:section -->
