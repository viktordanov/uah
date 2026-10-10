<!-- memoria:section id="overview" files="instructions.go include.go codex.go environment.go" -->
# Instructions

The instructions package finds instruction files the way Codex does (AGENTS.md, checked against `codex-rs/core/src/agents_md.rs`) and builds the runner's system prompt from them. The runner (uah-core, as unreal-agent-runner before it) reads no instruction files itself, and a system prompt replaces its default one, so uah keeps base instructions in front: uah's default prompt (Codex's prompt for gpt-6.1-sol, at rust-v0.159.1, adapted to uah's tools), or the text of `model_instructions_file`. Codex's `<environment_context>` block ends the prompt. The package also holds Codex's prompt word for word and the runner's own host prompt, which `uah prompts init` writes as `system-codex.md` and `system-runner.md`.

<!-- memoria:export id="summary" -->
uah finds instruction files the way Codex does: the user's AGENTS.md, then one file per directory from the project root down to the workspace (AGENTS.override.md, else AGENTS.md, else a configured fallback such as CLAUDE.md). A line that is only `@path` in a file is replaced by that file's text, as Claude Code's imports are. They are joined, capped at 32 KiB, and placed after the base instructions (uah's default prompt, adapted from Codex's, or the file that Codex's `model_instructions_file` key names), and Codex's environment context (the workspace, shell, date, and time zone) follows them. Skills come from Codex's skill folders.
<!-- /memoria:export -->

The keys are in the [configuration reference](../../docs/configuration.md#instructions-and-skills).

1. [Discovery](#discovery)
2. [Includes](#includes)
3. [The system prompt](#the-system-prompt)
4. [Skills](#skills)
5. [Tests](#tests)
<!-- /memoria:section -->

<!-- memoria:section id="discovery" files="instructions.go" -->
## Discovery

`Discover(workspace, userFiles, options)` lists the files in the order they apply:

1. The user file: `~/.uah/AGENTS.md`, or else `$CODEX_HOME/AGENTS.md` (`~/.codex/AGENTS.md` by default). The first that exists wins.
2. The project root: the nearest ancestor of the workspace that holds one of `project_root_markers` (`.git` by default). `[]` means the workspace only, and so does a workspace with no marker above it.
3. One file per directory from the project root down to the workspace: `AGENTS.override.md`, else `AGENTS.md`, else the first of `project_doc_fallback_filenames` that exists (none by default; `["CLAUDE.md"]` reads Claude Code's files). A fallback name with a path separator is ignored, as in Codex.

Later files are more specific. Empty files are skipped. `ProjectDirs` is exported because skill discovery walks the same directories.
<!-- /memoria:section -->

<!-- memoria:section id="includes" files="include.go instructions.go" -->
## Includes

`Assemble` expands the `@` lines of each instruction file in place, as Claude Code expands a CLAUDE.md's imports, so a global `~/.codex/AGENTS.md` with `@RTK.md` gives the model RTK.md's text without a tool call. The rules (`include.go`):

1. An include is a line that is only `@` and a path with no spaces, outside a fenced code block. `@` inside a sentence, `@ noon`, and a fenced `@x.md` stay as they are.
2. A relative path is resolved against the including file's directory, and `~/` against the home directory. An include in an included file is resolved against that file.
3. The line is replaced by `<include path="/abs/path">`, the file's text with its own `@` lines expanded, and `</include>`.
4. Each file is expanded once. A file already expanded becomes `@x.md (/abs/x.md is included above)`, so a loop ends. An include of one of the loaded instruction files is not expanded, since that file has its own section.
5. Includes nest at most `MaxIncludeDepth` (5) deep. A deeper one is noted with its path.
6. All the files included into one system prompt add at most `MaxIncludeBytes` (16 KiB). The file that passes the cap is cut at a line's end with a note that names it, and the includes after it are noted with their paths.
7. A file that cannot be read is noted with the reason, such as `no such file or directory`.

`project_doc_max_bytes` caps the files with their includes expanded. `File.Includes` lists the files expanded into each instruction file, in order.
<!-- /memoria:section -->

<!-- memoria:section id="prompt" files="instructions.go codex.go codex_prompt.md default_prompt.md default_prompt.diff environment.go" -->
## The system prompt

1. `Assemble` joins the files in order, each under a `## <path>` header with its [includes](#includes) expanded, and skips blank files. It stops before the file that would pass `project_doc_max_bytes` (32 KiB by default); a first file larger than the cap is cut.
2. `HostPrompt(base, instructions, environment)` puts the base instructions first, then `ProjectHeader` (a short "Project instructions" preamble) and the files when there are any, then the environment block. The base is the text of `model_instructions_file`, or `DefaultPrompt` when that key is not set. `HasProject(prompt)` reports whether a system prompt holds the files under `ProjectHeader`; the engine asks it before it tells a new session which instruction files its system prompt holds.
3. `LocalEnvironment` fills Codex's `<environment_context>`: the workspace, the base name of the shell, the local date, and the IANA time zone from `$TZ` or the `/etc/localtime` link (`Etc/UTC` and the UTC date when neither names a zone, as in Codex). `Environment.String` renders it as `codex-rs/core/src/context/world_state/environment.rs` does at rust-v0.156.1, with values XML-escaped.
4. `internal/app/setup.go` reads `model_instructions_file` (`readModelInstructions`: trimmed, and a missing or empty file is an error, as in Codex) and the files (`loadInstructions`), and takes the environment once, when the session opens. It sets the result as the session's `SystemPrompt`, which the session sends with every run, so the prompt cache holds for the whole session. It reports the files as `InstructionsLoaded`, shown by `/status` and `uah doctor`. It passes their paths to the engine (`embedded.Config.InstructionFiles`), which names them in the prepared context's agent files ([context preparation](../contextprep/README.md)) and lists them in `/context`, which finds them by the whole `ProjectHeader`, so a heading in the base does not split it, and counts the environment block as the system prompt.

The runner's context builder always puts its own preamble and the skill list before this prompt. `--no-instructions` or `[instructions] enabled = false` turns discovery off, but a `model_instructions_file` and the environment still apply. A request with no system prompt gets `DefaultPrompt` from the embedded engine. Subagents get the parent's system prompt, byte for byte, then a role's instructions; `SubagentNote` (from Codex's subagent role text: the final answer goes back to the parent agent) follows the task in a child's first message, so the prompt shares the parent's cache. A fork keeps the parent's prompt byte for byte, so its first request reuses the parent's prompt cache.

`DefaultPrompt` (`codex.go`, `default_prompt.md`) is `CodexPrompt` with the nine hunks of `default_prompt.diff`, which [the system prompt record](../../docs/design/system-prompt.md) explains. `WithoutQuestionTool` takes hunk 4's sentences on `request_user_input` out again, back to ledger item 63's text, for a session with the tool turned off (`[tools.experimental_request_user_input] enabled = false`; `internal/app` applies it to the base instructions). `CodexPrompt` (`codex_prompt.md`) is Codex's base instructions for gpt-6.1-sol at rust-v0.159.1, word for word; uah keeps it so the diff can be applied again when Codex's template changes. `RunnerHostPrompt` is unreal-agent-runner's default text (the same in v0.1.1 and v0.2.0), uah's default before `DefaultPrompt`. `uah prompts init` writes the three as `system.md`, `system-codex.md`, and `system-runner.md`, and `model_instructions_file` can name any of them. The [configuration reference](../../docs/configuration.md#codexs-prompt) lists the changes.
<!-- /memoria:section -->

<!-- memoria:section id="skills" files="instructions.go" -->
## Skills

Skills are Codex's `<name>/SKILL.md` folders. The embedded engine (`internal/engine/embedded/skills.go`) reads them with the runner's own parser and offers them through the runner's `SkillUse` tool. The folders, most specific first:

1. `.agents/skills` in each directory from the workspace up to the project root.
2. The runner's `<workspace>/.harness/skills`.
3. `~/.uah/skills`.
4. `$CODEX_HOME/skills` (`~/.codex/skills` by default).
5. uah's system skills in `~/.uah/skills/.system` ([`internal/systemskills`](../systemskills/systemskills.go)), embedded in the binary and written there when a session starts, each with its `SKILL.md` and Codex's `agents/openai.yaml` (the name the `$` menu shows), because `SkillUse` reads a skill from a file, as Codex writes its own under `$CODEX_HOME/skills/.system`. Being last, any skill of the same name replaces one. `uah-customization` explains context preparation and the other customization points.

A name found in a more specific folder wins.
<!-- /memoria:section -->

<!-- memoria:section id="tests" files="instructions_test.go include_test.go default_prompt_test.go environment_test.go" -->
## Tests

`instructions_test.go` pins discovery (override, fallback, root markers, the user file), assembly with the size cap, `include_test.go` the includes (in place, relative and `~/` paths, nesting, a missing file, a loop, a repeat, an instruction file, a fenced line, the depth limit, and the cap), the host prompt with and without a base and an environment, and the embedded Codex prompt. `default_prompt_test.go` applies `default_prompt.diff` to `CodexPrompt` and requires `DefaultPrompt` exactly, so the prompt cannot drift from the documented hunks. `environment_test.go` pins the block against Codex's render test and the time zone rules. `internal/app/setup_test.go` pins the default prompt and the environment from Setup, `internal/app/agents_parity_test.go` the default prompt in the model request and a subagent's, and `internal/app/systemprompt_test.go` `model_instructions_file` from Setup to the model request, a subagent's request, and `/context`. `internal/engine/embedded/skills_test.go` pins the skill folders.
<!-- /memoria:section -->
