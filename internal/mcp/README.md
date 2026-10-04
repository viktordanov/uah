<!-- memoria:section id="overview" files="manager.go config.go names.go" -->
# internal/mcp: MCP servers for the embedded engine

<!-- memoria:export id="summary" -->
uah runs the MCP servers in `[mcp_servers]` (Codex's format) on the embedded engine through the official Go SDK: stdio and streamable HTTP servers, their tools offered as `mcp__<server>__<tool>` and called without blocking the agent, Codex's resource tools, prompts as `/mcp__<server>__<prompt>` and resources as `@server:uri` in the composer (Claude Code's), restarts with backoff, tool list changes applied at the next run, Codex's approval modes, OAuth logins with `uah mcp login` kept in the OS keyring and picked up by a running session, and `uah mcp` to list, add, remove, and approve servers.
<!-- /memoria:export -->

This package owns everything MCP that is not engine or UI wiring: the configuration, starting and watching servers, naming and calling tools, OAuth, stored logins, and editing the configuration file for `uah mcp add` and `remove`. The embedded engine, `internal/app`, `cmd/uah`, and the TUI use it through a small surface: `Manager` (`Tools`, `Started`, `Call`, `Status`, `Close`; `ListResources`, `ListResourceTemplates`, `ReadResource`, `Resources`; `Prompts`, `GetPrompt`), `Mentions`, `ResourceBlock`, `WithoutResources`, `PromptText`, `SplitArgs`, `Login`, `Logout`, `AuthStatusOf`, `AddServer`, `RemoveServer`, and `SetApproval`.

1. [Lifecycle of a server](#lifecycle-of-a-server)
2. [How a call flows](#how-a-call-flows)
3. [Resources and prompts](#resources-and-prompts)
4. [Approvals](#approvals)
5. [Authorization](#authorization)
6. [Configuration and Codex](#configuration-and-codex)
7. [Extending](#extending)

Behavior follows Codex (openai/codex rust-v0.156.1) unless the runner (uah-core v0.9.0) forces a difference; the decisions and the validation are in [the MCP design record](../../docs/design/mcp.md).
<!-- /memoria:section -->

<!-- memoria:section id="lifecycle" files="manager.go server.go status.go transport.go stderr.go" -->
## Lifecycle of a server

A `Manager` holds the configured servers and starts nothing until an interactive session opens, a run, `/mcp`, or `uah doctor` asks (`Start`, `Started`, `Tools`, `Status`). Each enabled server then connects on its own goroutine within its `startup_timeout_sec` (default 30 s) and lists its tools, every page, and its prompts when it offers them. A server is `starting`, then `ready`, `failed`, or `needs_login`; `enabled = false` makes it `disabled`, and a server that stopped is `restarting`. `Tools` waits until every server has started or failed and names the allowed tools of every server that listed some. A run calls `Tools` once, as it starts, so a run's tools never change while it runs. A `required` server that did not start fails the run.

A stdio server gets only Codex's basic variables (`HOME`, `PATH`, `USER`, and a few more), then `env_vars` by name, then `env`, and runs in `cwd` or the workspace. Its standard error is logged a line at a time ("MCP server stderr", with the server's name) and never reaches the screen. An HTTP server gets `http_headers`, `env_http_headers`, and the bearer token from `bearer_token_env_var`. These headers go only to the server's origin (its scheme, host, and port), and the client follows a redirect only within that origin, as Codex's does: a redirect to another host, another port, or from https to http fails the request before anything is sent there, so neither the configured headers nor the OAuth token or session ID the SDK sets reach another origin (`serverClient`, `sameOriginRedirect`).

A goroutine watches each connection (`watch`, then `stopped`). When a stdio server exits or an HTTP server goes away, the server becomes `restarting` and `restart` connects it again after `Options.RestartDelay` (1 s), doubling to at most 30 s, at most `MaxRestarts` (5) times in a row; a server that ran for a minute before it stopped starts the count again. Codex leaves such a server failed. The server keeps its last tool list meanwhile, so the tools offered and the prompt cache stay the same; after the last failed attempt, or the fifth restart in a row, it is `failed` with the reason, and its tools still fail with it. A restart that gets a 401 makes the server `needs_login`. An HTTP server that forgot its session (404) gets a new session on the next call. `notifications/tools/list_changed` lists the tools again (`relist`); `retool` names them anew, and the next run gets them. `notifications/prompts/list_changed` lists the prompts again. `Close` stops every server for good; the SDK closes a stdio server's standard input and waits up to 5 s before stopping it. After `Close`, `Start` does nothing, `Tools` fails with `ErrClosed`, and `Status` and `Started` report nothing; the closed state is set under the lock that `Start` checks, so a start that races with `Close` cannot reconnect a server.

A manager belongs to one engine, which `internal/app` builds for each session it sets up. An interactive session connects the servers as it opens: the embedded engine's `StartMCP` calls `Started`, which waits like `Tools` and reports each server with its `required` key, or nothing when the manager closed meanwhile. A late report from a closed session therefore never starts its servers again. Later runs, `/clear`, and subagents on that engine use the same connections. The TUI's `/new` and `/resume` close the session, and with it the engine, before they set up the next one from the configuration, which may name other servers for another workspace: the servers reconnect once, and two connections to a server are never open at once. This follows Codex, where each session has its own MCP connection manager.
<!-- /memoria:section -->

<!-- memoria:section id="calls" files="call.go result.go names.go" -->
## How a call flows

1. The embedded engine's registry offers each tool from `Manager.Tools` under its qualified name: `mcp__<server>__<tool>`, each part cut to `[A-Za-z0-9_]`, at most 64 characters, with 12 hex digits of a SHA-1 when a name is too long or collides.
2. When the model calls one, the engine's translator checks that the arguments are a JSON object, applies the approval mode, and submits a runner remote job (plan `uah.mcp_call`) instead of running the call itself, so the coordinator never waits on a server.
3. The engine's remote job handler calls `Manager.Call` on its own goroutine. A server without `supports_parallel_tool_calls` takes one call at a time; `tool_timeout_sec` (default 300 s) starts when the call is made, so it also bounds the wait for the server's turn and for a restart or reconnect under way (`await`).
4. `send` sends `tools/call` through the SDK. When the request never reached the server, it is sent once more: on a new session after a 404, or after the restart when the connection had already ended. A call in flight when the server stopped fails and is not repeated. A canceled job cancels the call's context, and the SDK tells the server.
5. `convert` turns the result into text and images, as Codex does: structured content replaces the content as JSON text, images become `data:` URLs, and `isError` fails the job with the text. The runner bounds the text (40,000 characters, head and tail) before the model sees it.

A job that was still running when uah stopped fails as interrupted on resume instead of calling the tool twice.
<!-- /memoria:section -->

<!-- memoria:section id="resources" files="resources.go prompts.go mention.go" -->
## Resources and prompts

Resources reach the model through Codex's tools: `ListResources`, `ListResourceTemplates`, and `ReadResource` return what `list_mcp_resources`, `list_mcp_resource_templates`, and `read_mcp_resource` print, as JSON with each entry's `server` beside the server's fields. With a server, one page from a cursor; without one, every ready server that offers resources, all pages, a failing one logged and left out. A request goes through `request` and `send`, so it waits for a restart and retries like a tool call, within `tool_timeout_sec`. The engine's resource tools run them as MCP remote jobs.

Resources reach the user as Claude Code's mentions: `Resources` lists them for the composer's "@" menu, `Mentions` finds the `@server:uri` words of a message whose server is configured, and `ResourceBlock` turns a read resource into a `<resource server uri mimeType>` block, calling `Attach` for an image so the message carries it. `WithoutResources` takes the blocks off a message again for the auto-reviewer, which counts the user's messages as the user's words.

Prompts are Claude Code's slash commands: `Prompts` names each ready server's prompts as tools are named (`mcp__<server>__<prompt>`), `SplitArgs` splits the typed arguments with quotes, `GetPrompt` fills them in order (the last takes the rest; a missing required one is an error with the usage), and `PromptText` turns the result into one user message.
<!-- /memoria:section -->

<!-- memoria:section id="approvals" files="config.go manager.go approve.go" -->
## Approvals

Each tool has Codex's `approval_mode`: its own from `[mcp_servers.<name>.tools.<tool>]`, else `default_tools_approval_mode`, else `auto`. `Tool.NeedsApproval` decides: `approve` never asks, `prompt` always asks, `writes` asks unless the tool is annotated read-only, and `auto` asks unless the annotations say read-only, or both non-destructive and closed-world. The engine asks through the session's approval prompt, which PermissionRequest hooks can answer; headless runs and `approval_policy = "never"` refuse with a reason the model reads. `enabled_tools` and `disabled_tools` decide which tools exist at all.

The prompt for an MCP call offers "Yes, and don't ask again for this tool" beside yes and no, as Codex's MCP prompt offers "Allow and don't ask me again" (`codex-rs/core/src/mcp_tool_call.rs:1430`). The answer is `approval.ApproveTool`, and the engine calls `Manager.AlwaysAllow`: the tool's mode becomes `approve` in the manager at once, so the rest of the run and later runs stop asking, and `SetApproval` writes `[mcp_servers.<server>.tools.<tool>] approval_mode = "approve"` into the file `Options.ServerFile` names. `internal/app` names the file that configures the server, the trusted project file when it has the server and else the user file, as Codex does (`mcp_tool_call.rs:2345-2375`). When the file cannot be written, the session keeps the approval and the log says why.

From the command line, `uah mcp add <name> --approve` writes `default_tools_approval_mode = "approve"`, and `uah mcp approve <name> [tool] --mode <mode>` sets the default or one tool's mode through `SetApproval`; without `--mode` it prints them.
<!-- /memoria:section -->

<!-- memoria:section id="auth" files="auth.go login.go callback.go credentials.go" -->
## Authorization

An HTTP server authorizes with a bearer token (`bearer_token_env_var` or an `Authorization` header) or with OAuth; a stdio server needs neither.

`Login` runs OAuth 2.1 through the SDK's `auth.AuthorizationCodeHandler`: it connects, and the server's 401 starts discovery (protected resource and authorization server metadata), client registration (`oauth.client_id`, else dynamic registration), PKCE, and the token exchange. This package adds the loopback callback on 127.0.0.1 (`/callback`, a port the OS picks unless `oauth.callback_port` or `mcp_oauth_callback_port` sets one), the printed URL, the browser, a 300 s wait, `scopes` and `oauth_resource`, and saving the tokens, including any refresh the SDK does at once.

A running server's `storedAuth` handler sends the stored token and lets the oauth2 package refresh it; each new token is saved back. A 401 never opens a browser: the server becomes `needs_login` with "Run `uah mcp login <name>`", which `/mcp`, `uah doctor`, and `uah mcp list` show. This is the same at startup, on a later call, and on a restart (which is how a 401 on the SDK's background SSE stream, which never asks the handler, ends): a call that gets a 401 fails with that message, and the server's later calls fail at once. `relogin`, from `Tools` and `Status`, reads a `needs_login` server's stored login again; when a usable one differs from the one it loaded (`loginChanged`), the server reconnects with a fresh handler, so a `uah mcp login` in another terminal takes effect at the next run, as Codex checks before each step.

OAuth requests (discovery, registration, the token exchange, and refreshes) follow redirects only within the request's origin too (`oauthClient`), so a 307 or 308 cannot replay a code, refresh token, or client secret to another origin; Codex also refuses cross-origin OAuth discovery redirects. The SDK's other discovery checks, such as refusing private addresses, still apply.

Logins are stored as Codex stores them, under the server's name and a hash of its URL: in the OS keyring (service "uah MCP Credentials", through `github.com/zalando/go-keyring`), or in `~/.uah/mcp-credentials.json` (0600) when `mcp_oauth_credentials_store` is `file`, or `auto` (the default) and the keyring fails. `AuthStatusOf` reports "Bearer token", "OAuth", "Not logged in", or "Unsupported" without starting the server, with at most 5 s of discovery.
<!-- /memoria:section -->

<!-- memoria:section id="configuration" files="config.go configfile.go approve.go" -->
## Configuration and Codex

`ServerConfig` has Codex's keys and meanings, so a Codex `[mcp_servers]` section copies over; unsupported Codex keys are errors rather than ignored. The keys, defaults, and merge rules are in [the configuration reference](../../docs/configuration.md#mcp-servers). Differences from Codex: tool names are at most 64 characters (flat function names instead of namespaces), `auth` accepts only `oauth`, and client ID metadata documents are not offered. A server with another `auth` value (`ErrUnsupportedAuth`) is `failed` with the reason, and the other servers start; `uah mcp list` shows it with the reason, and `uah mcp login` refuses only it. Any other invalid value fails `NewManager`.

`AddServer` and `RemoveServer` edit a configuration file for `uah mcp add` and `remove` without rewriting it, with the editor in [internal/config/tomledit](../config/tomledit/tomledit.go) that the TUI's `/config` also uses: the go-toml parser finds the server's `[mcp_servers.<name>]` tables, those bytes are cut, and a new table is appended. Comments and the other keys stay as they were. The result must parse and validate before it replaces the file atomically with the same permissions; a server written as an inline table is refused. `SetApproval` sets one key the same way, with `tomledit.Set`.
<!-- /memoria:section -->

<!-- memoria:section id="extending" files="manager.go server.go" -->
## Extending

- **Another request to a server.** Use `Manager.request`, which waits for a restart, retries an unsent request, and marks a 401, and add the engine's side as another `op` of the MCP remote job plan.
- **A new transport.** `Manager.transport` returns the SDK transport for a configuration; add the case and its keys in `ServerConfig.validateTransport`.
- **Another credential store.** Implement `CredentialStore` and add a mode to `NewCredentialStore`.
- **Restarts.** `stopped` decides; `restart` and `restartDelay` hold the policy, and `await` is where calls wait.
<!-- /memoria:section -->
