<!-- memoria:section id="overview" files="toolbridge.go" -->
# toolbridge

How a command reaches its session's tools. The session listens on a Unix socket in its private `$TMPDIR` (`SocketName`), and `uah tool`, run inside any of the session's commands, sends one request and prints the answer. The [design](../../docs/design/tool-bridge.md) has the reasons and the A/B plan; [internal/engine](../engine/README.md#the-tool-bridge) serves the requests.

<!-- memoria:export id="summary" -->
The protocol and client of the tool bridge: one JSON line each way over the session's Unix socket in `$TMPDIR`, for `uah tool list`, `describe`, and `call`.
<!-- /memoria:export -->

- **The token.** Each request carries the session's token, `UAH_TOOL_TOKEN` in its commands' environment: on Linux any command of the user can reach any session's socket, so the token is what keeps a restricted session, such as a subagent's, from another session's tools.
- **The protocol.** A `Request` names an op (`list`, `describe`, or `call`), a tool's name, and, for a call, its arguments as a JSON object. A `Response` carries the tools, one tool's description and input schema, or a call's text and image count, or `Error`: the tool's own error, a refusal by the policy, a hook, or the user, or a bad request. Each is one JSON line (`WriteMessage`, `ReadMessage`), at most 64 MiB.
- **The client.** `Do` connects from inside the session's `$TMPDIR` by the socket's name, since a socket address holds about 100 bytes and the full path may be longer; it changes the process's directory for the dial, so only a short-lived command such as `uah tool` calls it. No socket there is `ErrNoSession`: the command does not run in a session that offers the bridge.
- **Tests.** The engine's `bridge_test.go` drives a session's bridge through `Do`, and `cmd/uah`'s `TestExecScriptsCallMCPTools` runs `uah tool` from the model's command in the sandbox.
<!-- /memoria:section -->
