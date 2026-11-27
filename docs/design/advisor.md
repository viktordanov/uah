# Advisor

Status: planned 2026-11-24; stage 1 (the tool and the model backend) and stage 2 (the command backend) built 2026-11-24 on `feat/advisor` ([as built](#as-built)). Ledger item 159. Claude Code facts are from its 2.1.331 binary (read for this study) and Anthropic's public [advisor tool docs](https://platform.claude.com/docs/en/agents-and-tools/tool-use/advisor-tool); LiteLLM facts are from its [advisor docs](https://docs.litellm.ai/docs/completion/anthropic_advisor_tool).

The agent gets an `advisor` tool. When it calls it, a stronger model (or any command) reads the conversation so far and answers with advice, which the agent gets as the tool's result. The agent calls it before it commits to an approach, when it is stuck, and before it says it is done.

1. [How Claude Code does it](#how-claude-code-does-it)
2. [Plan](#plan)
3. [Not planned](#not-planned)
4. [Build order and checks](#build-order-and-checks)
5. [Open decisions](#open-decisions)
6. [As built](#as-built)

## How Claude Code does it

- **A server tool.** Claude Code adds `{type: "advisor_20260301", name: "advisor", model: <advisor model>}` to the request's tools, with the beta header `advisor-tool-2026-03-01`. The API runs the advisor inside the same request: when the executor calls `advisor` (no input), the server sends the executor's whole transcript (its system prompt, tool definitions, turns, tool results, and the text of the current turn) to the advisor model, which has its own system prompt, no tools, and no context management. The advisor's thinking is dropped; its text goes back to the executor as an `advisor_tool_result`. The advisor's own prompt is server-side: it is in neither the binary nor the docs.
- **Results.** `advisor_result` (text), `advisor_redacted_result` (encrypted for the client, plain for the executor), or `advisor_tool_result_error` with a code: `max_uses_exceeded`, `too_many_requests`, `overloaded`, `prompt_too_long`, `execution_time_exceeded`, `unavailable`. An error never fails the request; the executor goes on without advice. Its usage is billed separately (`usage.iterations`, `advisor_message`).
- **When to call it** is a section of the system prompt Claude Code adds while the advisor is on: before substantive work (orientation first is fine), before declaring done (with the work saved first), when stuck, and when changing approach; give the advice weight, but say so in another call when evidence contradicts it. Anthropic measured that a nudge after the second turn helps small executors and slightly hurts the strongest.
- **Setup.** `/advisor [model|off]` ("Let Claude consult a stronger model at key moments") and the `advisorModel` setting. The advisor must be at least as capable as the main model (a rank in the model catalog), else it stays off for the main loop and only subagents may use it. A message tells the model when the advisor comes or goes mid-conversation, forked side queries are told never to call it, and turning it off strips its results from the history. The transcript shows "Advisor has reviewed the conversation and will apply the feedback", with the advice when expanded.
- **LiteLLM** runs the same tool for OpenAI and other executors in its own loop: a plain function `advisor` with no parameters, a call to the advisor model with the transcript and no tools, and the advice returned as the function's output. That is the shape uah needs.

## Plan

### The tool

- **`advisor`, no parameters.** A function tool offered while the advisor is on. Its description says when to call it, in uah's own words (the "when" text lives in the description, so it comes and goes with the tool; no separate system-prompt section or toggle messages).
- **What it sends.** The history as the model last saw it: the session's last turn request as sent (after compaction, with any elided outputs as stubs, kept by `lastRequests` for `/context`), then that request's response up to and including the advisor call. The response must hold this call's ID; else the advisor is unavailable for the call (a stale pair). The advisor's own model call never replaces the session's last request. Calls in that response without a result yet (the advisor call and any sibling) get a placeholder output, so the request is valid.
- **After a remote compaction** the request holds a placeholder for the compaction item, which the transport swaps in (`withRemoteItem`). The model backend sends through the same path, so the advisor model gets the item; a command cannot read it, so its transcript keeps the user messages and the ledger the record kept and says that earlier history was compacted.
- **Reasoning** is encrypted: the model backend sends it as it is (checked: gpt-6.1-sol took gpt-6.2-sol's reasoning), and the text and JSON transcripts leave it out.
- **What comes back.** The advice text as the tool's output. An error (timeout, limit, failed command) is an output too, such as `The advisor is unavailable (timeout after 10m); go on without it.`, and never fails the run. A call that got no advice (a timeout, a failed model call or command, a stale transcript) is a failed call, so the transcript, `uah exec`, and the run's stats show it; the limit and `/advisor off` are not failures.
- **Limits.** At most `max_uses` calls per run (default 5); beyond it the output says the limit is reached. A call waits at most `timeout` (default 10m) with no shorter total deadline; a stalled stream is retried as any model call is. Esc and the session's close cancel the model call and stop the command's process group.
- **Alone.** The description asks the model to call it on its own, not beside edits or commands, since a sibling's result is only a placeholder to the advisor.
- **Holding.** The call gets a `CallHold` of its timeout plus 30 s, as `wait_shell` does, so the model is not woken with "Still running" at 5 minutes, and a subagent whose advisor call runs is not stalled.
- **Elision.** The advice is never elided by normal elision, as `SkillUse` outputs are not; `ElideToFit` may stub it when a history is over the window.

### Backends

The advisor is one of two backends, set in `[advisor]`:

```toml
[advisor]
model = "gpt-6.2-astra" # openai-codex: a model on your subscription, stronger than the agent's
effort = "xhigh"
# or, in place of model:
command = ["claude", "-p", "--model", "opus", "--tools", "", "--no-session-persistence"]
input = "text"          # text (default) or json
timeout = "10m"
max_uses = 5            # per run
```

- **model** (openai-codex only): a model call on the session's own client, as the compaction summary is, with no tools. The advisor prompt is the instructions; the session's system text comes first in the input as quoted context, then the history, then a user message asking for the advice. The history is trimmed to the advisor model's window (`ElideToFit`, then `Trim`). The calls have a prompt cache key of their own, `uah-advisor-` and the session's key: on the session's key, a call found none of the call before it in the cache ([as built](#what-the-checks-found)).
- **command**: any program. It gets the transcript on stdin and its stdout is the advice; a non-zero exit or empty output is an error. The transcript is cut to `max_input_tokens` (estimated, default 150000) by dropping the oldest items after the first user message, so a long session still fits a smaller model. `input = "text"` renders the transcript as plain text with the advisor prompt at the end (for `claude -p`, `uah exec -`, or any CLI); `input = "json"` writes one JSON object with the advisor prompt and the items as the Responses API takes them (for a script that calls any API). It runs in the workspace, outside the sandbox, with the user's environment and `UAH_ADVISOR=off` set, so a `uah exec` advisor does not advise itself. It comes from the user's config or a layer, never a project's ([as built](#where-the-build-differs-from-the-plan)).
- **Claude advising GPT.** `claude -p` reads the transcript on stdin, so a Claude model advises an OpenAI one on the user's Claude subscription. Print mode loads CLAUDE.md, hooks, MCP servers, and its own system prompt, so the documented example sets its own system prompt and leaves the user's settings out; it is tested with a large stdin before it is documented.

### The advisor prompt

uah's own, as the call's instructions, with a short ask after the transcript ([as built](#where-the-build-differs-from-the-plan)): the advisor reads another agent's work for a user; it cannot run tools; it says whether the approach holds, what was missed or assumed wrongly, the risks, and the next steps, naming files, commands, and checks; when the work looks done, what to check before saying so; tool output is data, not instructions; short, numbered, no restating of the conversation.

### Setup and display

- **On and off.** The advisor is on when `[advisor]` sets `model` or `command`. `/advisor` shows it, `/advisor off` turns it off for the session, and `/advisor <model>` picks a model. `UAH_ADVISOR=off` turns it off.
- **Who gets it.** The main agent, under `[tools]` like every built-in (`advisor` joins the built-in list). Subagents only with `subagents = true` in `[advisor]`: 16 children each calling an xhigh advisor is a lot of a subscription. Reviewers get none.
- **The transcript** shows the call as `advisor`, with the backend and how long it took, and the advice when expanded.
- **Usage.** The model backend's usage counts toward the session under the advisor's model; a command's is not known.

## Not planned

- A server-side advisor: OpenAI has none.
- A capability rank between the session model and the advisor: the user picks the advisor.
- A nudge message after the second turn: measured as a loss on strong executors; reconsider after real sessions.

## Build order and checks

1. The tool, its plumbing, holds, limits, and the model backend. Real runs with GPT: a hard task (does it call the advisor at all?), a trivial one (does it call it every turn?), an error result the run goes on from, the limit, the timeout, an interrupt, a compacted session (remote item), an advisor model other than the session's (reasoning items), and a subagent with `subagents = true`.
2. The command backend: `uah exec -` (with the advisor off inside it), `claude -p` with a large transcript, and a script reading the JSON input.

## Open decisions

- Whether the model backend should be allowed on providers other than openai-codex (planned: openai-codex only; the command backend works on any).
- Whether subagents get the advisor by default.
- Whether `max_uses` should count per run or per session.
- Whether the program checks (stage 2, beyond the plan: a relative path refused, the program pinned with `PATH` and links resolved, off when the agent could change it, checked again at each call) are wanted as they are; the arguments, and a wrapper's own `PATH` lookup, stay unchecked.

## As built

Both stages are built: the tool, its plumbing, the hold, the limit, the model backend (stage 1), and the command backend (stage 2). `advisorBackend` (`internal/engine/embedded/advisormodel.go`) is the seam between them: the transcript in, the advice out. The [engine README](../../internal/engine/README.md#the-advisor) has how it works, the [configuration reference](../configuration.md#24-the-advisor-advisor) the keys, the input formats, and the `claude -p` and `uah exec -` examples.

### Where the build differs from the plan

- **The advisor prompt is the call's instructions**, not text after the transcript: the input is the session's system text, quoted in a first user message (`<agent_instructions>`) with the names of the agent's tools, then the history without its system message, then a short user message asking for the advice now.
- **The description** gained a sentence after the first real run: on a task of more than a few steps, call it at least before committing to an approach and before saying it is done; a question answerable at once needs no call. It gives reasons rather than bans.
- **Web searches** the agent ran go into the advisor's input as into a turn request, though the advisor has no search tool: the backend takes the `web_search_call` items without it (checked on openai-codex). The searches' results are not in them, as in Codex, so the advisor sees the queries and the messages written after them.
- **The remote compaction item** is the one the agent's request carried, recorded with it, not the compactor's latest, so a compaction while the call runs does not change what the advisor reads.
- **The provider rule** is a provider capability, `Provider.Advisor`, which only openai-codex has. With `model` set on another provider the advisor is off with one warning when the session opens; a model the provider's list lacks also warns.
- **`command`, `input`, and `max_input_tokens`** were read but refused with a "not built yet" error until stage 2.
- **`/advisor on`** turns it back on after `/advisor off`, and `/advisor <model> [effort]` takes an optional effort. On openai-codex `/advisor <model>` turns the advisor on also without an `[advisor]` table. The choice is kept by the session tree's root, so it covers subagents, and a run offers the tool or not as it starts; a call after `/advisor off` returns that the advisor is off.
- **The limit** counts calls that reach the backend: a call refused because the advisor is off, or for a stale transcript, does not use one.
- **A forked child** keeps the tool when subagents do not get it, so its requests keep the parent's prompt cache, and its calls are refused, as with `request_user_input` and the goal tools.
- **The transcript line** is `ADVISE advisor` with the time and the advice's first line; the detailed view shows the advice whole. It does not name the backend, as there is one.
- **Usage** goes to `sessions/<id>.advisor.jsonl`, one line per call that reached the model (model, effort, time, tokens, error), which `uah stats` counts as the source `advisor`; no run's totals hold it.

### What the checks found

Real runs with `uah exec --json` on openai-codex, the main agent on gpt-6.2-astra at low effort unless named, the advisor on gpt-6.2-sol at high unless named:

| Check | Result |
| --- | --- |
| A small task with an edge case (a Python duration parser and its tests), no mention of the advisor | At low effort, no call in two runs. At high effort, two calls: after the first `ls`, before writing (6.2 s), and after the tests passed, before the final answer (6.8 s) |
| A trivial question ("what is 2+2") | No call, at low and at high effort |
| Asked to consult it on a design question | One call (3.4 s, 7,775 input tokens); the answer said what the advisor suggested |
| Another advisor model than the session's: session gpt-6.2-sol at high with a reasoning item in its history, advisor gpt-6.1-sol at xhigh | Accepted: no error on the encrypted reasoning (14.3 s, 374 reasoning tokens) |
| `max_uses = 1`, asked to call it twice | The second call returned the limit's text in 7 ms, without a model call, and the run went on |
| `timeout = "2s"`, advisor at xhigh | `The advisor is unavailable (timeout after 2s); go on without it.` after 2.0 s, and the run went on |
| Esc (SIGINT to `uah exec`) during a call | The call canceled; the run ended interrupted 1.0 s after the signal |
| A remote compaction (`model_auto_compact_token_limit = 20000`, `compact_elide_after_calls = 0`, strategy `remote`) covering a tool output with a codeword, then a call | The advisor (gpt-6.2-sol, the compaction by gpt-6.2-astra) named the codeword and said it saw it in the conversation's summary, not the original output: the item reaches it in place of its placeholder |
| A same-model advisor (gpt-6.2-astra at medium), two calls in a row, on the session's cache key | `cached_tokens` 0 on both (9,104 and 9,211 input tokens), the second too, whose first ~8,000 tokens match the first call's |
| Two calls in a row (gpt-6.2-sol at high), on the session's key, then on a key of the advisor's own | Session's key: 0 cached on both (9,106 and 9,222 input tokens). Own key: 0, then 8,832 of 9,217 cached (96%). The calls now use their own key |
| `subagents = true`, a subagent asked to call it | The subagent's call worked (1.9 s), logged under its own session |
| A hosted web search, then a call | Accepted with the `web_search_call` items and no tools (11.8 s); the advisor named the agent's searches |
| Placeholder results left out (a build without `withPendingResults`) | The backend fails the response: `No tool output found for function call call_…`. The placeholders are needed |

- **The prompt cache.** On the session's cache key, which its turn requests use, an advisor call got no cached tokens in any run, also where its prefix matched the call before it. On a key of its own, the second call of a session was 96% cached, so the calls use `uah-advisor-` and the session's key, as the reviewer uses `uah-review-`. The sessions' local compaction summaries go on the session's key and show the same misses (5 calls, about 1 M input tokens, none cached), while remote compactions, which go out as turn requests, are 88% cached. A first call is never cached: its instructions are the advisor's, not the turn's. A same-model advisor could reuse the turn's cache only if it sent the turn's own instructions and tools with the ask at the end, as remote compaction does; that is not built.
- **A failed stream is retried.** Without the placeholders the backend's `response.failed` was retried as the runner retries any failed stream: 10 attempts over about 3 minutes before the call returned that the advisor is unavailable. The placeholders avoid this case; another rejection would take as long.

### Stage 2: where the build differs from the plan

- **One backend, across files.** A file that sets both `model` and `command`, or mixes their keys (`effort` with `command`, `input` with `model`), is an error. Across files, a later file's backend replaces an earlier one (`config.mergeAdvisor`): a file that sets `command` drops the earlier `model` and `effort`, and one that sets `model` the earlier `command`, `input`, and `max_input_tokens`. So a layer's command replaces the user file's model rather than conflicting with it, and `command = []` drops an earlier command.
- **No command from a project.** A project file's `command`, `input`, and `max_input_tokens` are ignored, trusted or not, with a notice when the session opens (`config.Layers.Notices`), and dropped before the file's backends are checked, so a `model` beside them works. The command runs outside the sandbox with the conversation on stdin; the sandbox keeps `.uah/` read-only, but a checkout or a pull changes the file. Which program and which subscription advise is the user's choice, not the project's. A project's `model` still applies, and replaces the user's command. After the merge, `effort` with `command` is a usage error: the command sets its own effort.
- **`/advisor` with a command.** `/advisor <model>` replaces a configured command for the session tree, on openai-codex only; elsewhere it is an error. `/advisor command` goes back to the command. A model picked on openai-codex holds when the session moves to another provider, where the advisor is then off until `/advisor command`. The status names the command's program and its input format.
- **The text transcript** puts each entry under a line that names it and carries a tag, random for each call, so a tool output that copies such a line cannot pass for an entry.
- **The JSON transcript** is uah's own encoding of Responses API input items, since uah-core exports no encoder: messages with `input_text` or `output_text` parts, function and custom tool calls and their outputs, and the web searches as recorded. It has a schema version, `v: 1`, and carries the advisor prompt, the ask, the agent's system text, model, and tool names, and how many items the trim dropped.
- **Web searches** go into both formats by the search log's anchors, as the transport puts them into a turn request; an anchor may be a reasoning item, so they are placed before reasoning is left out.
- **After a remote compaction** a note stands in place of the placeholder. The user messages the record kept come before it, and its ledger after it.
- **The trim** sizes each entry by what is rendered (an image in JSON as compaction counts one, not by its data URL), drops a call with all its results, and never drops the advisor's own call or a call still running beside it, whose placeholder result follows the advisor's. The text gets a note of how many entries went, after the first user message; the JSON a `dropped` count. Both reserve room for it. When the entries that cannot go (the conversation up to the first user message, and those calls) are still over the limit, the call is unavailable rather than over it.
- **The advice** is the command's stdout, trimmed and cut at 32,000 bytes, with a note when cut: under a tool output's 40,000 characters, which the runner would otherwise cut again in the middle. A failure gives the last line of stderr in the reason and the last 2 KB after it.
- **The process group** is killed on a timeout or a cancel (`Cmd.Cancel`), before its leader is reaped. Once the command has exited the group is not signalled, as a hook's is not: its ID may be reused then, and a kill that is safe after the exit needs a wait that does not reap the leader, which os/exec does not offer. A process left behind that holds the output holds the call for 1 s at most (`WaitDelay`), and goes on. The run's end (`advisorJobs.close`) cancels the calls in flight and waits up to 5 seconds for them, so the group is gone before `uah exec` exits.
- **The advisor log** names a command as `command:<program>`, with zero usage, which `uah stats` skips; a call whose transcript could not be built ran nothing and is not logged. A command whose program `PATH` lacks warns when the session opens.
- **The program is checked and pinned.** A relative program path is a usage error. A program, by path or found in `PATH`, that the sandbox in workspace-write mode lets commands change (`sandbox.Policy.Changeable`: the file or its directory is writable, or a symlink on the way lies in a writable directory), checked by the path found and by its real path, since a link the agent could change picks the program as surely as the file, turns the command backend off with a warning when the session opens, as a model on another provider does (`pinAdvisorProgram`), and `/advisor` says why (`Advisor.Refused`): the agent could change it, and it runs outside the sandbox. So `uah` started in `~` with `claude` under `~/.local/bin` has no command advisor. The program found in `PATH` when the session opens, with its symlinks resolved, is the path the session runs (`Advisor.Program`), under the configured name as `argv[0]`, so a multicall program such as BusyBox still sees its applet's name: the engine runs no bare name, so a later `PATH` lookup cannot pick another, and no link on the way can be swapped. Each call checks the program again against the session's policy with its grants, so a directory granted later does not open it, and refuses it when it leads elsewhere now. A session keeps the version it pinned, also when the program updates itself. The arguments are not checked, so a wrapper (`env`, `sh -c`) that looks a program up in `PATH` at each call is not covered; the docs name the program itself.
- **A shared buffer fix.** `capped`, which keeps the head of a command's output, embedded `bytes.Buffer`; os/exec's `io.Copy` called its `ReadFrom` and skipped the cap. It no longer embeds it, which also makes the review command's output cap work.
- **The transcript line** still does not name the backend.

### Stage 2: what the checks found

Real runs with `uah exec --json` on openai-codex, the main agent on gpt-6.2-astra at low effort, each with an `[advisor] command` and a prompt that asks for a call:

| Check | Result |
| --- | --- |
| A shell script that saves stdin and prints fixed advice (`input = "text"`) | The agent got the advice (11 ms) and followed it. The script saw `UAH_ADVISOR=off` and the workspace as its directory; the 42 KB transcript had the quoted instructions, the tool names, each entry under its tagged line, and the prompt and ask at the end |
| `input = "json"`, a Python script that validates the object | Valid: `v` 1, every key, 9 items (messages, function calls and outputs), each output after its call, no encrypted content; 888 ms |
| A command that writes 61 lines to stderr and exits 3 | `The advisor is unavailable (the command /bin/sh failed with exit status 3: advisor exploded: no login for the advisor service); go on without it.`, then the last 2 KB of stderr; the agent said the advisor was unavailable and the run ended ok |
| A command that never reads stdin and sleeps 1 s, after the agent read 108 KB of files (a transcript well past a pipe's 64 KB, by the request's 50,000 input tokens; its size was not measured) | The advice, in 1.0 s: the writer's EPIPE is not an error |
| `timeout = "3s"`, a command that sleeps and leaves a grandchild that holds stdout | The call returned the timeout's text at 3.0 s; `pgrep -g` found no process of the group, and the grandchild was gone |
| SIGINT to `uah exec` during a command call (a group of three processes) | The call canceled; `uah exec` exited 130 0.14 s after the signal; the group was gone |
| `claude -p` (opus) advising GPT on a small task, with the flags of the [example](../configuration.md#examples-of-an-advisor-command) | Two calls, 11.9 s and 10.0 s, with concrete advice (the regex, the error cases, the second check before finishing), which the agent followed |
| `claude -p` with a 187 KB transcript (four 36 KB files read whole; the size measured by a `tee` in front of claude) | Answered in 11.5 s; the advice named a detail of the files (one was a copy of another). A first try was 81 KB, as the agent capped three outputs |
| Advice over the cap, through a session (fakellm) | The agent read 32,000 bytes and the note; at 40,000 the runner had cut it again in the middle, so the cap is 32,000 |
| `uah exec -` advising (gpt-6.2-sol, `--tools ""`, `--ephemeral`), its own config with `[advisor] model` set | Advice in 9.5 s; the wrapper logged `UAH_ADVISOR=off`. Asked for its tools, the same inner command listed `advisor` without the variable and not with it |
| A program named by its name (`sh`), in the default sandbox | Pinned: the command ran as `/bin/sh` (its `$0`), logged as `command:/bin/sh` |
| A program inside the workspace | Refused when the session opened (now: the advisor is off with a warning) |

The first three rows, the `uah exec -` row, and the documented `claude -p` argv ran again on the final build, in the default workspace-write sandbox rather than `--yolo`, with the same results (the JSON one: 9 items, 46 KB). The other rows ran on earlier builds of stage 2, before the 32,000-byte cap, the trim's changes, `PWD`, and the program checks; the timeout and SIGINT rows ran again after the last change to the process group, with the same results.

- **`claude -p` and CLAUDE.md.** Without `--setting-sources ""`, print mode read `~/.claude/CLAUDE.md` (and in a trusted project, the project's `CLAUDE.local.md`), but not a canary CLAUDE.md in an untrusted temporary workspace. With `--setting-sources ""` it read none, and the subscription login still worked. `--safe-mode` gave the same result.
- **`claude -p` and hooks.** Without `--setting-sources ""` the user settings' hooks ran (SessionStart, UserPromptSubmit, Stop); with it, only the built-in plugins loaded, with no hooks.
- **`claude -p` and MCP.** Without `--strict-mcp-config` the claude.ai connectors connected; with it none did, though the connector list is still fetched.
- **`claude -p`'s init event** (`--output-format stream-json --verbose`) with the documented flags: no tools, no MCP servers, no slash commands, no skills, the four built-in plugins, and `apiKeySource` `none`. Without the flags: 97 tools, the connected MCP servers, 109 slash commands, 71 skills, and one installed plugin more. The documented argv ran as written on the final build, with uah itself started under `env -u` to clear this test's Claude Code variables: pinned to `~/.local/share/claude/versions/2.1.331`, it answered in 8.0 s.
- **`ANTHROPIC_API_KEY`** (checked 2026-11-27 with Claude Code 2.1.332): set in the environment, it takes the place of the subscription login; `claude auth status --text` then shows `API key: ANTHROPIC_API_KEY` and the claude.ai token "not in use". The docs say to unset it.
- **Not checked:** auto memory (the workspaces had no memory directory). The JSON transcript was checked for its shape, not sent to a Responses endpoint.
- **`uah exec`'s flags after `-`** (found 2026-11-27). urfave/cli v3.13.0 stops at a lone `-` and drops what follows, so the flags of the `uah exec -` row, and of the example then documented, never applied: the inner run had its tools (so it could list them), instructions, prepared context, a saved session, and the user's default model and effort. A one-line prompt sent 15,749 input tokens that way, against 4,062 with the flags before `-`. The example now ends with `-`, and `uah exec` and `uah review` refuse arguments after a lone `-` (`argsAfterDash`).
- **The environment.** These runs ran inside a Claude Code session, whose `CLAUDECODE` and `CLAUDE_CODE_*` variables (and `CLAUDE_EFFORT`, `CLAUDE_PID`) a command inherits; the claude runs unset them with `env -u` (a test setup only: the docs say to start uah from a plain shell, since a wrapper's `PATH` lookup is not checked). No `ANTHROPIC_*` variable was set.

