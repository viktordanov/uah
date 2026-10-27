# Subscription usage: research and plan

Status: built, 2026-09-24. Ledger item 35 was the spike; item 39 built the recommended design with the default of each open decision (see [As built](#as-built)). Codex facts are from openai/codex at `rust-v0.156.1` (paths under `codex-rs/`, written `C/`). Runner facts are from unreal-agent v0.1.1 (written `R/`). uah paths are relative to the repository root.

**Answer.** Yes, uah can show the ChatGPT subscription's usage as Codex does. The same credentials give it through two channels:

- A read-only `GET https://chatgpt.com/backend-api/wham/usage`. Codex's `/status` uses this endpoint.
- The `x-codex-*` headers on each `/responses` call.

A real read on the owner's machine confirmed both channels (see [Probes](#probes)). The runner exposes neither the headers nor a rate-limit event. For this reason, the recommended first step is the separate GET, behind one small interface. The headers can be added later if uah builds the openai-codex HTTP client itself.

1. [How Codex reads usage](#how-codex-reads-usage)
2. [How Codex shows usage](#how-codex-shows-usage)
3. [What the runner exposes](#what-the-runner-exposes)
4. [Probes](#probes)
5. [Terms and safety](#terms-and-safety)
6. [Options](#options)
7. [Recommended design](#recommended-design)
8. [Open decisions](#open-decisions)
9. [As built](#as-built)

## How Codex reads usage

Codex gets the same `RateLimitSnapshot` from four sources:

1. **The usage endpoint.** `GET {chatgpt_base_url}/wham/usage` (`C/backend-client/src/client/rate_limit_resets.rs:69-80`, path at `:124-129`). `chatgpt_base_url` defaults to `https://chatgpt.com/backend-api/` (`C/core/src/config/mod.rs:4317-4319`). A base URL without `/backend-api` uses `/api/codex/usage` instead (`C/backend-client/src/client.rs:125-140`). The app server calls it for the TUI only when the login is a ChatGPT login (`C/app-server/src/request_processors/account_processor.rs:1107-1138`).
   - Body: `plan_type`, `rate_limit {allowed, limit_reached, primary_window, secondary_window}`, where each window is `{used_percent, limit_window_seconds, reset_after_seconds, reset_at}`. Also `credits {has_credits, unlimited, balance}`, `spend_control`, `additional_rate_limits [{limit_name, metered_feature, rate_limit}]`, and `rate_limit_reached_type {type}`. The types are generated from a "codex-backend" OpenAPI document, version 0.0.1 (`C/codex-backend-openapi-models/src/models/rate_limit_status_payload.rs`, `rate_limit_window_snapshot.rs:14-22`, `rate_limit_status_details.rs:16-34`). Some fields are read outside the generated types "until the backend OpenAPI export includes it" (`C/backend-client/src/types.rs:55-67`).
   - Mapping: the ordinary limit gets ID `codex`. Each additional limit gets its `metered_feature` as ID and its `limit_name` as name (`C/backend-client/src/client.rs:591-665`). Window seconds become minutes, rounded up, and `reset_at` is Unix seconds (`:719-732`, `:789-796`).
2. **Response headers on each model call.** `parse_all_rate_limits` reads `x-codex-primary-used-percent`, `-primary-window-minutes`, `-primary-reset-at`, the same three for `secondary`, `x-codex-credits-has-credits`, `-unlimited`, `-balance`, and `x-<id>-limit-name`. It also reads one more family for each `x-<id>-primary-used-percent` header, for example `x-codex-other-…` (`C/codex-api/src/rate_limits.rs:22-102`, `195-272`). A window whose values are all zero is dropped (`:207-209`). The HTTP stream parses the headers before the first event and emits `ResponseEvent::RateLimits` (`C/codex-api/src/sse/responses.rs:42`, `76-79`).
3. **A `codex.rate_limits` event**, only on the WebSocket transport (`C/codex-api/src/endpoint/responses_websocket.rs:756-758`, parser at `C/codex-api/src/rate_limits.rs:135-169`). The core stores each snapshot and sends it with the next token-count event (`C/core/src/session/turn.rs:2815-2819`).
4. **The 429 error.** When a model call fails with HTTP 429 and `error.type` is `usage_limit_reached`, Codex reads `plan_type` and `resets_at` from the body. It reads the active limit from `x-codex-active-limit` and the reached type from `x-codex-rate-limit-reached-type` (`C/codex-api/src/api_bridge.rs:149-174`, `231`, `294-305`).

The snapshot type is in `C/protocol/src/protocol.rs:2343-2410`: `limit_id`, `limit_name`, `primary` and `secondary` windows (`used_percent`, `window_minutes`, `resets_at`), `credits`, `individual_limit`, `spend_control_reached`, `plan_type`, and `rate_limit_reached_type`.

## How Codex shows usage

- **`/status` card.** One row for each window, with a 20-cell bar and "N% left (resets HH:MM)". The reset time shows "HH:MM on D Mon" when it is not today. Example: `5h limit: [███████████░░░░░░░░░] 55% left (resets 09:25)`, then a `Weekly limit` row and `Credits`. The card starts with "Visit https://chatgpt.com/codex/settings/usage for up-to-date information on rate limits and credits" (`C/tui/src/status/card.rs:57`, snapshot `C/tui/src/status/snapshots/…status_snapshot_includes_credits_and_limits.snap`, bar `C/tui/src/status/rate_limits.rs:23-25`, text `:375`, reset format `C/tui/src/status/helpers.rs:183-190`). Data older than 15 minutes is marked stale (`C/tui/src/status/rate_limits.rs:64-65`, `235-237`).
- **Window names** come from the length, within 5%: `5h`, `daily`, `weekly`, `monthly`, `annual`, else `usage` or `secondary usage` (`C/tui/src/chatwidget/rate_limits.rs:103-146`). They do not come from the position (primary or secondary).
- **Status line.** Two optional items, `FiveHourLimit` and `WeeklyLimit`, show the remaining percent of the primary and secondary windows. Each item is omitted when there is no data (`C/tui/src/bottom_pane/status_line_setup.rs:110-114`, `186-190`).
- **Warnings.** When a window passes 50, 75, 90, or 95% used, a notice appears: "Heads up, you have less than N% of your <label> limit left". Each threshold shows once. The plan decides which thresholds show (`C/tui/src/chatwidget/rate_limits.rs:19`, `30-90`).
- **Refresh.** The TUI reads the endpoint at startup, on `/status`, and on a timer: every 60 s, then every 30 s from 75% used, 15 s from 90%, and 5 s from 99%. It does this only for a ChatGPT login (`C/tui/src/app/background_requests.rs:78-117`, `C/tui/src/app/startup.rs:1096-1099`, `C/tui/src/chatwidget/rate_limits.rs:192-217`, `415-417`).
- **Limit reached.** The error text depends on the plan and ends with "Try again at <time>." For example: "You've hit your usage limit. Upgrade to Pro (…), visit https://chatgpt.com/codex/settings/usage to purchase more credits…". Workspace credit and spend-cap cases have their own text (`C/protocol/src/error.rs:655-760`).

## What the runner exposes

- **Response headers: no.** `openaicodex.NewClient` builds its own `http.Client`, from a clone of `http.DefaultTransport` (`R/harness/llm/clients/openaicodex/client.go:48-52`). `Config` has no field for an HTTP client or a transport (`:19-27`). The adapter keeps the headers in a private `responseAttempt` and uses them only for `Retry-After` (`R/harness/llm/responsesapi/stream.go:53-75`, `retry.go:39-40`).
- **Trace: bodies only.** `Config.Trace` receives `Exchange{RequestBody, StatusCode, ResponseBody}`, with no headers (`R/harness/llm/responsesapi/adapter.go:41-45`, `58-59`, `120-122`). For a 429 the body is the error JSON, so a trace could see `usage_limit_reached` with `resets_at`.
- **Rate-limit events: no.** The runner uses HTTP SSE, and Codex's `codex.rate_limits` event exists only on WebSocket. Since 2026-10-27 uah sends openai-codex turn requests over the WebSocket ([engine README](../../internal/engine/README.md#retries)); the backend sends `codex.rate_limits` before each response there, and uah passes it on in the stream but reads nothing from it yet.
- **The 401 error only.** `Respond` wraps 401 as "codex credentials rejected" (`R/harness/llm/clients/openaicodex/client.go:81-84`). `APIError` has `StatusCode`, `Code`, `Message`, and `Type`, but not `resets_at` (`R/harness/llm/responsesapi/adapter.go:23-29`).
- **uah already builds one openai-codex client itself.** The priority (`/fast`) client in `internal/engine/embedded/providers.go:117-145` makes its own `http.Client` with the same headers and redirect rule. The default tier uses the runner's client (`providers.go:79-93`). `internal/models/providers.go:21-50` already sends a separate request to the same backend with `codexauth.Load` (the model list). A usage GET follows the same pattern.

## Probes

The probes are in `internal/usage/probe_test.go`, behind the `probe` build tag, and were run on 2026-09-24 on the owner's machine. The credentials came from `openaicodex.EnvironmentConfig` and `codexauth.Load`, as the engine reads them. No token or ID was printed.

1. **`GET /backend-api/wham/usage`: HTTP 200 in about 0.4 s.** The response is below; `account_id`, `user_id`, and `email` are redacted:

   ```json
   {
     "account_id": "<redacted>", "user_id": "<redacted>", "email": "<redacted>",
     "plan_type": "pro",
     "rate_limit": {
       "allowed": true, "limit_reached": false,
       "primary_window": {"used_percent": 22, "limit_window_seconds": 604800,
                          "reset_after_seconds": 155809, "reset_at": 1790426679},
       "secondary_window": null
     },
     "additional_rate_limits": null,
     "code_review_rate_limit": null,
     "credits": {"has_credits": false, "unlimited": false, "balance": "0",
                 "approx_local_messages": [0, 0], "approx_cloud_messages": [0, 0],
                 "overage_limit_reached": false},
     "model_usage": {"gpt-6-astra": {"available": true, "available_at": null, "credits_would_enable": false}},
     "promo": null,
     "rate_limit_reached_type": null,
     "rate_limit_reset_credits": {"applicable_available_count": 0, "available_count": 3},
     "spend_control": {"individual_limit": null, "reached": false}
   }
   ```

   Parsed, this is `weekly 78% left (resets 15:44 on 26 Sep)`. The endpoint needed only the headers that the engine already sends: `Authorization: Bearer`, `ChatGPT-Account-ID`, `originator: unreal-agent`, and `User-Agent: unreal-agent`. The Codex user agent and `client_version` were not needed. This Pro account has **only a weekly window, sent as the primary window**. The display must therefore name windows by their length, as Codex does. It must not show "5h" for the primary window.

2. **One tiny `/responses` call** (`gpt-5.6-luna`, low effort, "Say OK.") through `usage.Transport`. The response had these headers: `x-codex-active-limit`, `x-codex-credits-balance`, `x-codex-credits-has-credits`, `x-codex-credits-unlimited`, `x-codex-plan-type`, `x-codex-primary-over-secondary-limit-percent`, `x-codex-primary-reset-after-seconds`, `x-codex-primary-reset-at`, `x-codex-primary-used-percent`, `x-codex-primary-window-minutes`, the four `x-codex-secondary-*` headers (all zero, so dropped), and `x-codex-turn-state`. The headers gave the same snapshot as the GET. `x-codex-plan-type` and `x-codex-primary-over-secondary-limit-percent` are not read by Codex 0.156.1. The prototype reads the plan from the headers.

This call was made twice. The first run's output was hidden by a terminal filter, and running the test again sent a second request. In total, the probes sent one usage GET and two tiny model calls.

## Terms and safety

- **The endpoint is internal and undocumented.** `/backend-api/wham/*` is the private ChatGPT backend. Codex's types for it are generated from an OpenAPI document that is not published, and part of the body is read outside the generated types. OpenAI publishes no documentation or stability promise for it, and its shape can change without notice. Codex itself sends users to https://chatgpt.com/codex/settings/usage for "up-to-date information" (`C/tui/src/status/card.rs:57`).
- **Authentication.** Codex sends `Authorization: Bearer <ChatGPT access token>`, `ChatGPT-Account-ID`, `X-OpenAI-Fedramp: true` for FedRAMP accounts, and `User-Agent: codex_cli_rs/<version> (<os>; <arch>) …`, with originator `codex_cli_rs` (`C/model-provider/src/bearer_auth_provider.rs:31-45`, `C/backend-client/src/client.rs:252-273`, `C/login/src/auth/default_client.rs:40`, `150-164`). The usage GET has no `client_version`; the `/models` list has one.
- **What uah would change.** The runner already sends the same token to the same host for every model call, as `unreal-agent`. A usage read adds one read-only GET for each refresh, with the same identity. It does not send a new kind of credential, write anything, or pretend to be Codex. The prototype never follows redirects, accepts only the ChatGPT host or a loopback server, and keeps the token and the response body out of errors.
- **The risk that remains is policy, not technical.** Whether a third-party harness may call an internal ChatGPT endpoint with a user's own login is a decision for the owner (see [Open decisions](#open-decisions)).

## Options

| Option | How | For | Against |
| --- | --- | --- | --- |
| A. Separate GET | `usage.Fetch(ctx, creds)` to `/wham/usage` with `codexauth.Load` | Works now (probe 1). The runner and engine stay unchanged. Works for both engines, before the first turn, and in `uah usage` and `uah doctor`. Same data as Codex's `/status` | One more request for each refresh. The data is only as fresh as the last read. Internal endpoint |
| B. Transport wrapper | `usage.Transport` around the `http.Client` of the openai-codex client | No extra requests. Fresh after each model call (probe 2) | The runner's client does not accept a transport, so uah must build the default-tier client itself, as it does for the priority client. This changes the engine. The process engine cannot do it. No data before the first call |
| C1. Trace for 429 | Set `openaicodex.Config.Trace` and read `usage_limit_reached` with `ParseLimitReached` | Exact reset time when a limit is hit, with no extra request | Only when the limit is reached. Needs a small `providers.go` change |
| C2. Loopback proxy | Point the runner's base URL at a local reverse proxy (the runner accepts loopback, `client.go:90-104`) | No runner change | A proxy that holds the token in the path of every call. Too heavy and too fragile |
| C3. Swap `http.DefaultTransport` | Replace it before the runner clones it | None | The runner type-asserts `*http.Transport` (`client.go:50`), so a wrapper panics. Global state. Rejected |
| C4. Upstream hook | Add a header observer to the runner | Cleanest long-term | The runner stays unchanged (ledger rule 5) |

## Recommended design

**Build A now and keep B as a later, optional source.** Both write to the same place, so the rest of uah does not see which source was used.

### One small interface

```go
// internal/usage
type Reader interface {
    // Usage returns the latest snapshot, reading the backend when the cached
    // one is older than maxAge. ErrUnsupported for providers without usage.
    Usage(ctx context.Context, maxAge time.Duration) (Snapshot, error)
}
```

- `usage.NewCodexReader(getenv, baseURL)` does `openaicodex.EnvironmentConfig`, `codexauth.Load`, and `Fetch`. It keeps the last snapshot in memory, and it sends one request at a time (a single-flight lock).
- For every other provider, `usage.For(provider, …)` returns a reader that gives `ErrUnsupported`. The callers show nothing in that case.
- Only `internal/app` constructs a reader. The TUI state gets `Snapshot` values in messages. `internal/tui/state` and `render` import only the types, never `Fetch`. The engine is not changed for A.
- Later, B adds `Observe(Snapshot)` to the same cached reader. The engine calls it from `usage.Transport`, and the next `Usage` call returns the fresh value without a request.

### Where the data shows

| Place | What | When it reads |
| --- | --- | --- |
| `uah usage` (new command) | Plan, one row for each window, as in Codex's card: `weekly  [████░░░░] 78% left (resets 15:44 on 26 Sep)`, credits, extra limits. Has `--json`. For other providers: "usage is known only for openai-codex" | Each time it runs |
| `/status` | The same rows, added after the current notices. Uses a new `EffLoadUsage` effect and `UsageLoaded` message, like `EffLoadActivity` and `ActivityLoaded` (`internal/tui/state/effects.go:21-22`, `commands.go:153-167`, `internal/tui/bubble/effects.go:77-88`). Marked "(stale)" after 15 minutes | Each `/status`, with max age 0 |
| Footer | The tightest window, beside the context meter: `78% weekly left · 64% context left` (`internal/tui/render/screen.go:238-240`). Hidden when unknown or for other providers | After each run ends, with max age 60 s |
| Warnings | One notice when a window passes 75%, 90%, and 95% used: "Heads up, you have less than 25% of your weekly limit left (resets 15:44 on 26 Sep)" | Same read as the footer |
| Limit reached | When a run fails with a 429, read usage and add "Try again at <time>" to the error. Show the reset time of the window that reached 100% | On that error |
| `uah doctor` | One `usage` check. `ok`: "pro · weekly 78% left". `warn` from 90% used. `fail` when `limit_reached` or the backend returns 401. Skipped for other providers | Each time it runs |

There is no background timer at first. Codex polls every 60 s, but uah reads only when the user looks (`/status`, `uah usage`, `uah doctor`) or after a run ends, which bounds the extra requests to one for each run.

### What is left to build after the owner accepts

1. `Reader`, `NewCodexReader`, and `For` in `internal/usage` (the prototype already has the parsers, `Fetch`, and `Transport`, with tests).
2. `uah usage` in `cmd/uah` and the doctor check in `internal/app`.
3. `EffLoadUsage`, `UsageLoaded`, the footer item, and the warnings in the TUI.
4. Optional: B, which means building the default-tier openai-codex client in `providers.go` the way `codexPriorityClient` builds its client, with `usage.Transport`.

## Open decisions

Each decision has a default, which the plan above uses.

1. **Call the internal endpoint at all.** Default: yes, read-only, for openai-codex only, with the runner's own identity (`unreal-agent`). The alternative is to show only what the headers give (B), which also uses the private backend, but only through calls the runner already makes.
2. **Identity.** Default: `originator` and `User-Agent` set to `unreal-agent`, as the runner's model calls send them. The alternative is to copy Codex's `codex_cli_rs/<version>`. The probe shows it is not needed, and it would pretend to be Codex.
3. **Footer.** Default: on for openai-codex, showing the tightest window. The alternatives are a config switch (`[tui] show_usage`) or showing it only above 50% used, as Codex's status line items are optional.
4. **Refresh.** Default: on demand and after each run, cached for 60 s, with no timer. The alternative is Codex's poll every 60 s down to 5 s near the limit.
5. **Headers (B).** Default: later. It needs an engine change to build the default-tier client in uah.
6. **Warning thresholds.** Default: 75%, 90%, and 95%. Codex also warns at 50% and filters the thresholds by plan (`usage_notice::warning_threshold`), and uah does not copy that table.
7. **Extra fields.** Default: ignore `code_review_rate_limit`, `model_usage`, `rate_limit_reset_credits`, `spend_control`, and `promo` until a use appears. Codex 0.156.1 uses reset credits and spend control for features that uah does not have.

## As built

Ledger item 39 built option A with every default above. The code is in `internal/usage` (see its [README](../../internal/usage/README.md)).

- **Reader.** `usage.For(provider, opts)` gives a `CodexReader` for openai-codex and, for other providers, a reader whose error wraps `usage.ErrUnsupported` ("usage is not available for ollama"). The Codex reader loads the credentials on each read, sends one request at a time, keeps the last snapshot, and serves it within the max age. It returns the last snapshot with an error. `app.Setup` builds it once per session (`Result.Usage`) beside the model catalog, and `cmd/uah` passes it to the TUI in `bubble.Deps.Usage`. There is no global.
- **Identity.** The GET sends the engine's headers, with `originator` and `User-Agent` set to `unreal-agent` (`uah-core` since uah-core v0.6.0, ledger item 93).
- **`uah usage`** (`--json`). It prints the plan and one line per window, named by its length: `weekly  [███████████████░░░░░]  22% used · 78% left · resets 15:44 on 26 Sep`, then credits and "the usage limit is reached" when they apply. `--base-url` points it at a loopback server, as for `uah models`. For another provider it fails with "usage is not available for <provider>".
- **`/status`.** A notice with the plan and a row per window (bar, "N% left (resets …)"). When the read fails, it shows the error and the last snapshot, marked stale after 15 minutes. It reads with max age 0 through `EffLoadUsage` and `UsageLoaded`, like `EffLoadActivity`.
- **Footer.** The tightest window of the ordinary limit, before the context meter: `weekly 78% left · 64% context left`. It is hidden until the first read and for a provider without usage.
- **Refresh.** On each `/status`, `uah usage`, and `uah doctor`, and after each run ends (`RunFinished`, max age 60 s). There is no timer. Session events can now return effects in the TUI; this read is the first such effect.
- **Warnings.** One notice per window per threshold (75, 90, and 95% used), with the highest threshold crossed since the last read. A window that falls back below a threshold, after its reset, warns again. The read for `/status` records the thresholds without a warning.
- **Limit reached.** The runner reports a failed model call as text only: `responses API request failed with status 429: <message>`, or `responses API error <code>: <message>`, in a `RunnerError` or a model failure. `APIError` drops the body's `type` and `resets_at`. `usage.LimitReachedIn` accepts `usage_limit_reached`, or 429 with "usage limit", and takes `resets_at` from the text when the body survived in it. Otherwise the TUI reads the usage and names the latest reset among the windows at 100%, else the tightest window's reset: "Usage limit reached; try again at 15:44 on 26 Sep." The notice shows once per run. The error text itself was not seen for real; the parser is written for the forms above.
- **Doctor.** A `usage` check after `models`: `ok` with the plan and the tightest window ("pro · weekly 78% left (resets 15:44 on 26 Sep)"), `warn` from 90% used or when the read fails, and `fail` when a limit is reached or the backend rejects the login. It is left out for other providers.
- **Tests.** The reader, `uah usage`, `uah doctor`, and the TUI run against loopback servers with the fixtures; no test calls the backend. One real `uah usage` by hand on 2026-09-24 printed `pro plan (openai-codex)` and `weekly  [███████████████░░░░░]  22% used · 78% left · resets 15:44 on 26 Sep`.

Still open: option B (the headers), which needs uah to build the default-tier openai-codex client; the extra fields (decision 7); and a config switch for the footer (decision 3), which nobody has asked for.
