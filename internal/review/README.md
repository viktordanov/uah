<!-- memoria:section id="overview" files="review.go defaults.go" -->
# Auto-review

The auto-reviewer is Codex's "auto-review" (guardian): a model judges an action that needs approval before anyone is asked, and may run read-only commands first. Each session keeps one review conversation, and each review appends only what the session did since the last one. It fails closed, and a circuit breaker hands the choice back to the user after repeated denials.

<!-- memoria:export id="summary" -->
Before a user is asked to approve an action, the auto-reviewer judges it with Codex's review policy, from the user's messages and answers (trusted) and the tool calls and their results without output (untrusted), and may run read-only commands in the read-only sandbox first. A session's reviews continue one conversation, each sending only what is new since the last; the reviewer allows or denies with a reason, denies when the review fails, and leaves the choice to the user after three denials in a row.
<!-- /memoria:export -->

1. [How a review runs](#how-a-review-runs)
2. [The review conversation](#the-review-conversation)
3. [Tests](#tests)

The prompts in `prompts/` are Codex's (rust-v0.156.1, `codex-rs/prompts/templates/guardian`, Apache-2.0), trimmed of what uah's reviewer cannot do (MCP and browser rules). `[review] policy_file` replaces the policy (`prompts/policy.md`) with a file's text, as Codex's `[auto_review] policy` replaces it inline (`codex-rs/config/src/config_toml.rs:560-565`); the framing and the output contract stay, because `Parse` depends on them. `uah prompts init` writes the default policy to `~/.uah/prompts/review.md` as a starting point, and `DefaultPolicy` returns it. The package has no engine wiring; `internal/engine/embedded/autoreview.go` puts it in front of the user, as described in [the permission pipeline](../approval/README.md), keeps each session's transcript and conversation, and `reviewcmd.go` runs the reviewer's commands. The research and cost figures are in the [sandbox plan](../../docs/design/sandbox.md#auto-review-as-researched).
<!-- /memoria:section -->

<!-- memoria:section id="review" files="review.go prompt.go transcript.go conversation.go tools.go verdict.go breaker.go defaults.go prompts/policy.md prompts/policy_template.md prompts/output_contract.md prompts/investigation.md prompts/investigation_tools.md prompts/restrictions_tools.md" -->
## How a review runs

1. The session's `Transcript` is one list in order, numbered across the session as Codex numbers its guardian transcript: `user` entries (the user's messages, and the answers to the agent's questions in the user's own words), tool calls, and each call's short result (`exit 1`), without output.
2. The first review of a conversation sends `Render`: the transcript (`>>> TRANSCRIPT START`…`END`) and the planned action (tool, command, working directory, sandbox mode, requested permissions, justification, the sandbox denial). A later review sends `RenderDelta`: the entries since the last review (`>>> TRANSCRIPT DELTA START`…`END`, or `<no retained transcript delta entries>`) and its action, after Codex's follow-up reminder, a developer message added once ("Use prior reviews as context, not binding precedent…").
3. `Limits` keep either near 5,000 tokens besides the fixed prompt: the first user message (in a full transcript) and then the newest that fit, the last 20 tool calls and results, each entry cut in the middle when too long, and the entries left out or no longer kept counted in an `<omitted … />` marker.
4. The calls go through `internal/llmcall` with Codex's policy (or `Config.Policy`, read from `policy_file`) as the instructions and a per-session prompt cache key. With a `Runner`, the model is offered Codex's `exec_command` (`cmd`, `workdir`) and the template's investigation guidelines and restrictions are Codex's (`prompts/investigation_tools.md`, `prompts/restrictions_tools.md`): read-only commands only when they would flip the decision, such as inspecting what `rm -rf` would delete. Each command's result (exit code, wall time, output cut at 10 KB) goes back, and the model is called again, up to 12 rounds. Without a runner, the reviewer judges from the transcript alone (`prompts/investigation.md`).
5. `Parse` reads strict JSON: `outcome` (allow or deny), `risk_level`, `user_authorization`, and `rationale`. Unknown fields are errors. An answer that does not parse is asked again from the same conversation, up to Codex's attempt limit, within the timeout.

| Result | What happens |
| --- | --- |
| allow | The action runs; the TUI shows "auto-approved (risk): reason" |
| deny | The action does not run; the model gets the reason and is told not to retry |
| failed (error, timeout, bad answer) | Denied, with risk high |
| ask_user | The circuit breaker is open: PermissionRequest hooks and the user decide |

The breaker opens after 3 denials in a row or 10 in the last 50 reviews, as in Codex. Unlike Codex, a failed review counts as a denial. `Reset` closes it; the engine calls it at each new user message.

| Setting | Default | Key |
| --- | --- | --- |
| Model | `codex-auto-review` on openai-codex, else the session's model | `[review] model` |
| Request markers | Always no `service_tier` and `x-openai-subagent: guardian` (header and `client_metadata`), as Codex sends; Codex's billing-classification markers (`x-codex-guardian: reviewer`, `parent_response_id`, and `guardian_credits_requested` on normal requests) only on openai-codex and only when opted in (`embedded/reviewmarkers.go`) | `[review] guardian_markers`, off |
| Effort | low | `[review] effort` |
| Timeout | 90 s | `[review] timeout` |
| Policy | Codex's, `prompts/policy.md` | `[review] policy_file` |
| On or off | on | `approvals_reviewer` (`auto_review` or `user`) |

## The review conversation

`Conversation` is Codex's guardian trunk (openai/codex `main` at b741e48, 2026-10-03: `codex-rs/ext/guardian-reviewer/src/pool.rs`, `conversation.rs`). It holds the last finished review's checkpoint: the conversation after the instructions (each review's user message, the model's output items, the commands' results), the number of transcript entries reviewed (Codex's `TranscriptCursor`), and the last request's input tokens.

- **One at a time, forks for the rest.** A review that finds the conversation free takes it, continues it, and on success makes its result the new checkpoint. A review that starts while another holds it continues a copy of the last checkpoint and is dropped when it ends, as Codex runs a busy trunk's reviews in ephemeral forks of the committed trunk. So the parallel approvals of one response (`internal/engine/embedded/prefetch.go`) neither wait for each other nor change what the next review continues, and their requests still share the checkpoint's prefix in the prompt cache.
- **Starting anew.** A failed review ends the conversation, as Codex discards a reviewer that did not finish; so does another model, effort, or instructions (a policy file, or commands that became available or not), Codex's reuse key; and so does a request that reached `MaxConversationTokens` (100,000 input tokens), where Codex compacts its reviewer. The next review sends the whole transcript again.
- **No conversation.** A `Request` without a `Conversation` starts anew each time, as before.
<!-- /memoria:section -->

<!-- memoria:section id="tests" files="review_test.go prompt_test.go probe_test.go testdata/prompt.golden" -->
## Tests

`review_test.go` pins the outcomes, the fail-closed paths (a bad answer, a timeout), the retry, the breaker, the conversation (a later review sends only the delta after the earlier request as its prefix, with the reminder once), a parallel review forking without changing what the next review continues, a failure starting anew, and the reviewer's commands. `prompt_test.go` checks the rendered prompt and a delta against `testdata/prompt.golden`, the numbered transcript, the dropped-entry count, the template with and without commands, and the budget. `probe_test.go` runs only by hand: `TestProbe` makes two real reviews in one conversation, the second of an `rm -rf` the reviewer may inspect first (`go test -tags probe -run 'TestProbe$' -v ./internal/review/`); `TestProbeReplay` replays a recorded `uah exec --json` session (`UAH_PROBE_STREAM`) through the real reviewer with and without the conversation and logs both totals.
<!-- /memoria:section -->
