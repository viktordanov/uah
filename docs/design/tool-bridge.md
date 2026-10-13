# Scripted tool calls: the tool bridge

Status: built 2026-10-12, A/B test planned. Ledger item 143.

Inside any of a session's commands, `uah tool` calls the session's MCP tools, so the model can write one script that calls them, filters or joins their results with `jq` or Python, and prints only what it needs. This is "code mode" (pi's codemode, Cloudflare's Code Mode, Anthropic's code execution with MCP) built on the shell uah already has, instead of a JavaScript sandbox.

1. [The problem](#the-problem)
2. [Why the shell](#why-the-shell)
3. [How it works](#how-it-works)
4. [What the model is told](#what-the-model-is-told)
5. [Not in this version](#not-in-this-version)
6. [The A/B test](#the-ab-test)

## The problem

- **One call at a time.** Each MCP call is a model request: the model calls a tool, reads the whole result, and decides the next call. A search, then a lookup of each hit, is a request per lookup.
- **Every result enters the context whole.** A paged search of 300 records, of which the model needs 15 IDs, is read and then re-sent with every later request.
- **What uah already covers:** independent calls run in parallel in one turn, and Bash lets the model filter anything a shell reaches (files, logs, command output). MCP tools are what a shell could not reach.

The prior art reports its gains as examples, not benchmarks: Anthropic's post goes from "150,000 tokens to 2,000 tokens" in one example; Cloudflare and pi publish no measurements.

## Why the shell

A code-mode tool needs a language runtime, a sandbox for it, a way for the script to call tools, and a model that knows the tool. The shell has all four already:

- **The runtime and sandbox** are Bash's: the permission mode's sandbox, output caps, background shells, and kills.
- **The model** knows `bash`, `jq`, `python`, and `xargs` well; there is no new tool shape to learn, and a script can be as long or short as the task needs.
- **The calls** go back to the session, which runs them as the model's own: the tool policy and scope, PreToolUse hooks, the tool's `approval_mode` (which asks the user), and "don't ask again".

## How it works

- **The socket.** A run that offers the bridge opens the session's (`embedded/bridge.go`): a Unix socket, `uah-tools.sock`, in the session's private `$TMPDIR`, mode 0600, which every command of the session gets. A path over 100 bytes, which a socket address cannot hold, gets a short private directory under `/tmp` and a symlink in `$TMPDIR`. The bridge lives with the session: each run updates what it allows, and closing the session closes it.
- **The command.** `uah tool list | describe <name> | call <name> [json | -]` (`cmd/uah/tool.go`) reads `$TMPDIR`, connects through the socket's name from inside that directory (so its path length does not matter), sends one JSON line, and prints the answer: the result on stdout, or the reason on stderr with exit 1. It skips uah's startup and every configuration file, so it is fast and works in a read-only sandbox.
- **Finding uah.** The session's bin directory, `operations/<session>/bin`, holds a `uah` link to the running executable, and the sandbox script puts it first on `PATH` (`sandbox.Policy.BinDir`). So `uah tool` is the same uah as the session, whatever `PATH` holds. The directory is outside the writable roots.
- **Sessions apart.** Each bridge has a random token, which the sandbox script puts in its session's commands' environment as `UAH_TOOL_TOKEN`; a request without it is refused. On Linux, bubblewrap leaves every socket of the user reachable, so the token is what keeps a restricted session, such as a subagent's or another session's, from this one's tools; the scripts that hold the tokens are hidden from every sandboxed command, under bubblewrap and Seatbelt, since with the network on any socket is reachable. Another uah instance, with another state directory, keeps its scripts where this one's sandbox does not hide them, so the bridge also checks the connecting process: it must descend from this uah (the peer's PID, then its parents). Where bubblewrap cannot mount a fresh `/proc` (some containers), the host's stays visible and `/proc/<pid>/root` reaches every file, so no session is offered the bridge there.
- **The sandbox.** Seatbelt counts Unix sockets as network: without the network, the profile allows connecting to the bridge's socket by its exact path and to no other (`sandbox.Policy.Sockets`, `TestSeatbelt_ConnectsOnlyToItsSockets`). Under bwrap, a socket that is a file is reachable in any network namespace.
- **One approval per command.** A script fires its calls together. In the A/B test, 21 lookups of a tool that asks reached the auto-reviewer at once, and one missed the reviewer's deadline, which failed the run. So the first call of a tool from a command asks, the calls behind it wait for its answer, and an approval covers that command's later calls of the tool. Only an answer to an ask is kept: a PreToolUse hook's allowance is for its call's arguments, and a refusal is not kept.
- **A call.** The bridge runs the tool's PreToolUse hooks (a block refuses, an allow skips the approval, `updatedInput` replaces the arguments), then the MCP gate (`approval_mode`, through the run's approval chain: the scope's approvals, the auto-reviewer, then the user; a call that needs the user after its run ended, from a background shell, is refused with the reason), then the server. Resource tools (`list_mcp_resources`, `list_mcp_resource_templates`, `read_mcp_resource`) work the same way, within the servers the policy allows. A command that hangs up cancels its call.
- **Who gets it.** A run that does not offer it closes the session's bridge, so an old token stops working. A run whose engine has MCP servers, with the bridge on (`[tools.bridge] enabled`, `UAH_TOOL_BRIDGE`), that offers Bash with no limit on each command: a `/review`'s reviewer gets none. Subagents get their own, within their scope.
- **What shows.** Each call emits `engine.ToolBridgeCalled`: the TUI shows it as an MCP call's line marked `· uah tool`, and `uah exec` prints `uah tool: <name> (ok, 0.3s)`.

## What the model is told

The bridge must be explained where the model looks, and context preparation can be turned off, so it is told in two places:

- **Bash's description** ends with one sentence while the run offers the bridge: what `uah tool` does, its three forms, and when to use it (a large or paged result, or calls that depend on each other). It is fixed text, so the prompt cache holds.
- **The `harness` block** of the prepared context has a module, `harness/tool-bridge` (`when: {tool_bridge: true}`), with a worked pipeline and the rule of thumb that a direct call is still right for one small lookup.

The MCP tools are still offered directly: the model picks per task.

## Not in this version

- **Tools beyond MCP.** The built-in tools a shell cannot reach are candidates for later: subagents (`spawn_agent` from a loop), and the goal tools. `apply_patch` and the hosted web search stay out: a script edits files with the shell, and the search runs on the provider's side.
- **A per-call log in the session file.** The calls show in the TUI and in `uah exec`'s output; the command's own output is what the session records.
- **Images.** A call's images cannot reach a command; the command says how many there were.

## The A/B test

The question is whether models use the bridge, and whether it saves requests and tokens where MCP results are large, without costing anything elsewhere. The [code-mode study](../ledger.md) (item 143) has the plan:

- **Arms:** A, the bridge off (`UAH_TOOL_BRIDGE=off`); B, the bridge on; C, the bridge on with the MCP tools offered only through it, if B shows the model rarely reaches for it.
- **Tasks:** the benchmark's three MCP tasks on its incidents fixture server, a paged search of about 55 KB a page and a lookup by ID ([MCP tasks](../../tools/agentbench/README.md#mcp-tasks): `mcp-open-sev1`, three pages and 21 lookups; `mcp-timeout-summary`, nine pages, about 500 KB; `mcp-escalation-owner`, two pages, 23 lookups, and a code change); three large-output tasks; six ordinary tasks as guards.
- **Runs:** 8 repeats per arm and task, interleaved, on gpt-6.1-sol at high effort.
- **Metrics:** requests, model time, input tokens (cached and not), output tokens, cost, wall time, pass rate, and how often the model uses `uah tool`.
- **Bar:** 20% or more fewer input tokens or requests on the MCP and large-output tasks, with the same pass rate; no guard task worse by more than 10% or failing.
- **First step:** run arm A alone on the MCP tasks, to see whether MCP results do flood the context: `go run ./tools/agentbench -harness uah -tasks '^mcp-' -repeat 5 -uah-env UAH_TOOL_BRIDGE=off -variant nobridge`.
