# Sandboxing and approvals: plan

Status: decided 2026-09-24; phases 1 and 2 built (see [As built](#as-built-phase-1)). The research and the options are in [sandbox-research.md](sandbox-research.md); Codex facts are from openai/codex at rust-v0.156.1.

The rule for every choice below: do what Codex does, unless uah's runner forces a difference.

1. [Decisions](#decisions)
2. [How a command runs](#how-a-command-runs)
3. [Packages and files](#packages-and-files)
4. [Configuration](#configuration)
5. [Phases](#phases)
6. [Tests](#tests)

## Decisions

| # | Decision | Choice | Codex |
| --- | --- | --- | --- |
| S1 | Mechanism | The embedded engine's own Bash translator. The process engine gets a `SHELL` shim that sandboxes only; it cannot ask for escalation | The same model, per command |
| S2 | Default mode | `workspace-write`: the workspace, `/tmp`, and `$TMPDIR` are writable; the rest of the disk is readable | Workspace-write for trusted projects |
| S3 | Network for commands | Off. An escalated command runs outside the sandbox, with network | `network_access = false` |
| S4 | Protected paths | `.git`, `.uagent`, `.agents`, and `.codex` stay read-only inside writable roots, so `git commit` escalates | `.git`, `.agents`, `.codex` |
| S5 | Rules | Codex's `prefix_rule` syntax in `~/.config/uagent/rules/*.rules` and, for trusted projects, `<workspace>/.uagent/rules/*.rules`. The strictest matching decision wins | The same, in `~/.codex/rules` and `.codex/rules` |
| S6 | What `allow` means | Run without asking and outside the sandbox | The same |
| S7 | Approval policy | `on-request` (default) and `never`. A denial goes back to the model, which re-runs the command with `sandbox_permissions: "require_escalated"` and a `justification`; uah never retries by itself | The same; `untrusted` is internal only |
| S8 | Auto-review | On by default, TUI and headless. It fails closed (deny) and stops after 3 denials in a row, then asks the user (headless: denies) | Opt-in `approvals_reviewer = "auto_review"` (on in the owner's Codex config) |
| S9 | Reviewer model | `codex-auto-review` at low effort on openai-codex; the session model at low effort on other providers | `codex-auto-review`, or `gpt-5.6-luna` with an API key |
| S10 | Approval prompt | Codex's: "Yes, proceed", "Yes, and don't ask again for commands that start with `<prefix>`" (when a prefix rule is proposed), and "No, and tell the agent what to do differently" | The same three |
| S11 | Secrets | Commands inherit the full environment. `[shell_environment_policy]` offers Codex's `inherit`, `exclude`, `include_only`, `set`, and `ignore_default_excludes = false` for the `*KEY*`, `*SECRET*`, `*TOKEN*` filter. The sandbox can read the whole disk | Inherits everything by default (`ignore_default_excludes` defaults to true) |
| S12 | Linux | The system `bwrap`. Without it, uah warns and asks for every command that no rule allows | Bundled or system bwrap |
| S13 | Temporary directory | Each session has a private directory, `sessions/operations/<id>/tmp` (mode 0700), writable in read-only and workspace-write, and `TMPDIR`, `TMP`, and `TEMP` point at it for every command, escalated and yolo ones included, so `$TMPDIR` names one directory in and out of the sandbox. Heredocs, `go test`, and tool caches pointed at it work in read-only mode. Removing the session removes it. Nothing is special-cased per tool | Not in the legacy modes: read-only writes nothing |

## How a command runs

On the embedded engine, uah registers its own Bash tool. Its schema is the runner's plus two optional fields, `sandbox_permissions` (`"use_default"` or `"require_escalated"`) and `justification`, so the model can ask up front, as in Codex.

For each call, `Translate`:

1. Runs `PreToolUse` hooks (M6).
2. Checks the rules. `forbidden`: the call fails with the rule's justification. `allow`: it runs unsandboxed without asking.
3. For a call that does not ask for escalation: runs it sandboxed. The model sees a denial as a normal failure, plus one line: `uah: the sandbox blocked this (workspace-write); re-run with sandbox_permissions "require_escalated" and a justification if it is needed`.
4. For an escalation: asks the approver (auto-review first, then the user if the reviewer is unsure or off), then runs unsandboxed or fails with the reason.

A sandboxed call is a normal shell operation whose shell is `uah __sandbox --mode <mode> --workspace <dir> -- <real shell>`: `sandbox-exec -p <policy>` on macOS, `bwrap <args>` on Linux. Output files, cancellation, and orphan cleanup stay the runner's. Asking blocks the coordinator while the prompt is open, as the agent in Codex waits; the runner's other tool calls keep running.

On the process engine, `SHELL` points at the same `uah __sandbox`, and commands only run sandboxed; there is no way to ask for more than the rules allow.

## Packages and files

| File | Contents |
| --- | --- |
| `internal/sandbox/mode.go` | `Mode` (read-only, workspace-write, danger-full-access), writable roots, protected paths |
| `internal/sandbox/seatbelt.go` | The macOS policy, ported from Codex's `seatbelt_base_policy.sbpl` and write rules (Apache-2.0, with its notice) |
| `internal/sandbox/bwrap.go` | The Linux bubblewrap arguments |
| `internal/sandbox/denied.go` | Codex's denial heuristic (exit code plus "operation not permitted" and similar text) |
| `internal/sandbox/env.go` | `shell_environment_policy` |
| `cmd/uah/sandbox.go` | The hidden `uah __sandbox` entry point |
| `internal/rules/` | A parser for the `prefix_rule` subset of Codex's Starlark rules, and matching with the strictest decision winning |
| `internal/approval/` | The approver: policy, rules, the reviewer, the user, and the circuit breaker. Session events `ApprovalRequested` and `ApprovalResolved` |
| `internal/review/` | The reviewer (built as its own package, not `internal/approval/review.go`): user messages (trusted), the last tool calls without their output (untrusted), the action, and a fixed policy prompt. JSON out: outcome, risk, reason. See [Auto-review: as researched](#auto-review-as-researched) |
| `internal/engine/embedded/bash.go` | The Bash translator with the extra schema fields |
| `internal/tui/state`, `render` | The approval overlay and "auto-approved: <reason>" lines |

## Configuration

```toml
sandbox_mode = "workspace-write"     # read-only, workspace-write, danger-full-access
approval_policy = "on-request"       # on-request, never
approvals_reviewer = "auto_review"   # auto_review or user

[sandbox_workspace_write]
network_access = false
writable_roots = []

[shell_environment_policy]
inherit = "all"                      # all, core, none
ignore_default_excludes = true       # false drops *KEY*, *SECRET*, *TOKEN*
exclude = []
set = {}
```

The names match Codex's `config.toml`, so settings carry over. `--sandbox <mode>` and `--ask <policy>` override them, and the TUI's `/sandbox` shows the mode.

## Phases

| Phase | Scope | Estimate |
| --- | --- | --- |
| 1 | `internal/sandbox`, `uah __sandbox`, the embedded Bash translator (sandboxed calls, denial hint), the process engine shim, configuration, `/sandbox` | 4–5 days |
| 2 | Escalation with the user prompt, `internal/rules`, "don't ask again" writing a prefix rule to `~/.config/uagent/rules/default.rules` | 3–4 days |
| 3 | The auto-reviewer, its events, and the circuit breaker | 2–3 days |

Each phase ships on its own: after phase 1, commands are sandboxed and escalations are denied with a reason.

## As built (phase 1)

- **No `uah __sandbox`.** A per-session script execs `sandbox-exec` or `bwrap` around the real shell, and both engines use it as the shell: the embedded engine in its Bash translator, the process engine as the runner's `SHELL` (uagent v0.4.1's `RunnerBackend.Env`). Inherited environment variables are referenced as `"$NAME"` in it, so no value is written to disk.
- **Seatbelt** follows Codex, plus one fix: every writable root excludes the protected paths of all roots, so a workspace under `$TMPDIR` keeps `.git` read-only. Overhead is about 7–9 ms per command.
- **bubblewrap** follows Codex without its seccomp helper; `--unshare-net` isolates the network. A missing protected name is not protected on Linux (Codex creates an empty mount point on the host, which breaks git in subdirectories of a repository and races with parallel commands). CI installs bwrap and runs the real tests.
- **Denials** use Codex's keywords plus DNS and "network is unreachable" messages, because bwrap's network namespace fails that way.
- **go build** works in workspace-write: Go ignores cache writes it cannot make, so builds are slower rather than broken. `writable_roots = ["~/Library/Caches/go-build"]` restores the cache.

## As built (phase 2)

- **Packages.** `internal/rules` parses `.rules` files with `go.starlark.net` (`prefix_rule`; `host_executable` and `network_rule` load but do nothing) and splits commands with `mvdan.cc/sh/v3/syntax`: plain words and quotes joined by `&&`, `||`, `;`, and `|`. Anything else matches no rule, so the default policy applies, as with Codex's tree-sitter parser. An absolute program path also matches its base name. `internal/approval` holds the approver: `Decide(ctx, Request, Ask) Decision`, where `Request` has the command, cwd, escalation flag, justification, and the model's `prefix_rule`, and `Decision` is deny (with the reason for the model), sandboxed, or unsandboxed.
- **Order in `Translate`.** PreToolUse hooks, then rules (`forbidden` denies with the justification; `allow` runs unsandboxed without asking, also headless), then the policy: an escalation or a `prompt` rule asks, unless the policy is `never` or no one can answer (`uah run`), which deny with a reason. An approved `prompt` rule without escalation runs sandboxed, as in Codex. The unsandboxed shell is a second runner Bash translator whose shell is the real one (with the environment policy); the sandbox hint is added only to commands that ran in the sandbox.
- **Asking.** `engine.Options.Ask` carries the session's asker into each run, so the engine never imports the session. The session emits `ApprovalRequested` and waits for `Session.Resolve(id, answer)`; pending approvals live in a loop-owned map. An interrupt, the run's end, `Close`, or the run's context declines them (`ApprovalResolved` with `decline`). `session.Options.Interactive` is set by the TUI only.
- **"Don't ask again".** It proposes the model's `prefix_rule` when it covers every command and is not on Codex's banned list, else the whole command when it is one simple command, else nothing. The chosen prefix is appended to `~/.config/uagent/rules/default.rules` and applies at once; if the file cannot be written, it still applies for the session and the error is logged.
- **Without a sandbox** on the embedded engine (Linux without bwrap), each command asks unless a rule allows it (S12). `danger-full-access` offers no escalation, but rules still apply.
- **Configuration.** `approval_policy` (and `--ask`, `UAH_ASK`); `[approvals] allow` and `forbid` are command prefixes that become rules. A trusted project may set the policy and adds prefixes, rules files, and writable roots.
- **TUI.** The overlay replaces the queue panel: "Run outside the sandbox?" (or "Run this command?" for a `prompt` rule), the reason, `$ command`, and `y`, `s`, `n`/esc. The answer is recorded as a transcript line.

### Open (defaults taken)

- "No, and tell the agent what to do differently" declines with a fixed reason; the user types the instruction as the next message. Codex opens a text field.
- There is no session cache of approved commands ("don't ask again for this command in this session"); only prefix rules persist.
- `[approvals]` has no `prompt` list; a `prompt` rule needs a `.rules` file.
- A project's `[approvals] allow` and `.rules` `allow` rules run commands unsandboxed; they apply only to trusted projects, as hooks do, but need no separate trust step.

## Tests

- **Policy tables.** The generated Seatbelt profile and bwrap arguments per mode, compared with golden files.
- **Real sandbox runs on macOS.** Each mode against real commands:
  - writes inside and outside the workspace;
  - `.git` writes;
  - network access;
  - output files.
- **Linux.** bwrap in CI; GitHub runners need the AppArmor setting Codex's CI uses.
- **Embedded engine.** `fakellm` scripts through denial, escalation request, approval, and the "don't ask again" rule.
- **Reviewer.** Its prompt as a golden file, and its behaviour on allow, deny, bad JSON, and timeout, against a fake model.

## Auto-review: as researched

Status: the reviewer is built (`internal/review`) and configured; lane A's approver wires it in. Codex facts are from openai/codex at rust-v0.156.1, and those on the review session from `main` at b741e48 (2026-10-03); `CX` is `codex-rs`.

### What Codex does

| Topic | Codex behaviour | Where |
| --- | --- | --- |
| When it runs | Instead of the user prompt, for escalations, network approvals, MCP calls, patches, and permission requests, with `approvals_reviewer = "auto_review"` and approval policy `on-request` or `granular` | `CX/ext/guardian-reviewer/src/routing.rs` |
| Model | `codex-auto-review` (`DEFAULT_APPROVAL_REVIEW_PREFERRED_MODEL`) when the model catalog lists it, which it does on ChatGPT sign-in; `gpt-5.6-luna` with an API key; else the session model. A model may carry its own `auto_review_model_override` | `CX/model-provider/src/provider.rs:124`, `CX/ext/guardian-reviewer/src/model.rs`, `CX/core/tests/suite/guardian_review.rs:779` |
| Effort | `low` when the chosen model supports it, else the model's default | `model.rs` (`select_review_model`) |
| How it runs | A read-only, no-network sub-session. It may run read-only commands (`exec_command`, `write_stdin`, `view_image`, `exec`, `wait`) before answering. Approval policy `never`. The answer is constrained by `final_output_json_schema` | `CX/ext/guardian-reviewer/src/settings.rs` |
| System prompt | `policy_template.md` with `policy.md` in place of `{{ tenant_policy_config }}`, then the output contract. `[auto_review] policy` replaces `policy.md`; a template can be configured too | `CX/prompts/templates/guardian/`, `CX/prompts/src/guardian_instructions.rs`, `CX/core/src/guardian/reviewer_config.rs` |
| User content | An intro that calls everything untrusted, `>>> TRANSCRIPT START`…`END` (user, assistant, tool call and tool result entries, numbered), the session ID, then `>>> APPROVAL REQUEST START`, the retry reason (capped at 512 tokens), and the planned action as pretty JSON (`command`, `cwd`, `justification`, `sandbox_permissions`, `tool`, `tty`) | `CX/guardian-context/src/composition.rs`, `action.rs`, `CX/core/src/guardian/prompt.rs`, snapshot `guardian_review_request_layout.snap` |
| Trust | Only user and developer messages, AGENTS.md, and `request_user_input` answers are trusted; tool output, assistant text, and the action are untrusted evidence | `policy_template.md` |
| Budget | Per entry: 5K tokens for a message, 1K for a tool entry. In total: 20K for messages, 10K for tools, at most 40 recent non-user entries, at least 5 tool entries kept. Bytes / 4 estimates tokens. Later reviews in a session send only the transcript delta | `CX/guardian-context/src/profile.rs` |
| Review session | One reviewer conversation (the trunk) per parent thread, prompt cache key `guardian:<thread>`. A review on an idle trunk appends a user message to it: the first sends the whole transcript (`>>> TRANSCRIPT START`), later ones only the entries after the last reviewed one (`>>> TRANSCRIPT DELTA START`, "Continue the same review conversation"), with the action each time; a developer reminder ("Use prior reviews as context, not binding precedent…") goes in once before the second. A completed review commits a checkpoint (the history and the transcript cursor). A review that finds the trunk busy runs in an ephemeral fork of the last checkpoint and is discarded, so parallel approvals do not wait or change the trunk. A failed review discards the trunk; another model, effort, policy, permissions, or a rewritten parent history makes a new one; the reviewer compacts at its token limit | `CX/ext/guardian-reviewer/src/pool.rs`, `conversation.rs`, `CX/guardian-context/src/cursor.rs`, `composition.rs`, `CX/core/src/guardian/review_session.rs`, `review.rs:180-195` |
| Answer | `{"risk_level","user_authorization","outcome","rationale"}`, only `outcome` required. A missing `risk_level` becomes low on allow and high on deny. Prose around the JSON is tolerated by slicing from the first `{` to the last `}` | `CX/ext/guardian-reviewer/src/assessment.rs` |
| Deadline and retries | 90 s per review, up to 3 attempts inside it. Retried: a parse error, rate limits, overload, 5xx, dropped connections. Backoff 200 ms × 2ⁿ with ±10% jitter | `CX/ext/guardian-reviewer/src/lib.rs:40`, `retry.rs` |
| Failure | Fails closed. A timeout tells the model: "did not finish before its deadline. Do not assume the action is unsafe based on the timeout alone. You may retry once, or ask the user" | `CX/prompts/src/model_messages/guardian.rs` |
| Circuit breaker | Per turn: 3 denials in a row, or 10 in the last 50 reviews, interrupt the turn once. Only model denials count; a failed review resets the run of denials | `CX/ext/guardian-reviewer/src/circuit_breaker.rs`, `review.rs:113` |
| Cost | Not stated in the source. The fixed prompt is about 18 KB (policy template 9.7 KB, policy 8.3 KB), plus the sub-session's permission and environment messages and tool definitions, plus up to 30K transcript tokens on the first review, then deltas on a reused, cached session | — |

### What uah does

- **A conversation per session, with read-only commands.** `review.Reviewer.Review` continues the session's `review.Conversation` (Codex's trunk) through `internal/llmcall` over the adapter the caller gives it, so every provider works. With a sandbox, the model is offered Codex's `exec_command` (`cmd`, `workdir`), which `internal/engine/embedded/reviewcmd.go` runs with `/bin/sh` in the read-only sandbox, without network, with the environment policy, and with a temporary directory of the reviewer's own (`operations/<session>/review-tmp`). A command stops after 10 s; its output is cut at 10 KB; a review makes at most 12 model calls. Without a sandbox, the reviewer has no tools and judges from its context.
- **Prompt.** Codex's template, trimmed of MCP and browser rules (`internal/review/prompts/`, Apache-2.0 notice kept), with Codex's `policy.md` (three mentions of read-only checks removed) and output contract. With commands, the investigation guidelines and the reviewer's restrictions are Codex's; without, they say it cannot run commands. The golden is `internal/review/testdata/prompt.golden`.
- **Context.** One transcript in order, numbered across the session as Codex's is: `user` entries (the user's messages and their answers to the agent's questions, in their own words; trusted), tool calls, and each call's short result (`exit 1`) as an entry of its own (untrusted), then the approval request: the sandbox denial and the action JSON (`tool`, `command`, `cwd`, `sandbox_mode`, `sandbox_permissions`, `justification`, `rule`). Assistant text and tool output are left out, as Claude Code does, because injected text reaches the model that way. The engine keeps the latest 200 entries and the first user message.
- **Deltas.** The first review sends the whole transcript; later ones only the entries since the last finished review and the new action, after Codex's follow-up reminder once, so the earlier requests and answers are the cached prefix. Reviews that start while another holds the conversation each continue a copy of its last finished state, which is dropped, as Codex's ephemeral forks are; the parallel approvals of one response ([agent tuning](agent-tuning.md#parallel-approvals)) keep running at once. A failed review, another model, effort, or instructions, or a request of 100,000 input tokens starts the next review anew with the whole transcript.
- **Budget.** `DefaultLimits`, for a whole transcript and a delta alike: 8 KB per user message, 24 KB for all of them (the first message, then the newest that fit), 1 KB per tool entry, 8 KB and 20 entries for all tool calls and results, 8 KB for the command, justification, and denial each. Cut text keeps its head and tail around Codex's `<truncated omitted_approx_tokens="N" />`; left-out entries are counted in `<omitted transcript_entries="N" … />`.
- **Answer.** Codex's fields. Parsing is strict: one JSON object, unknown fields and values rejected, only a surrounding Markdown fence is removed. A bad answer is asked again from the same conversation, up to 3 times within the deadline; transport errors are retried by the runner's client.
- **Failure.** Any error, timeout, or unparsable answer is a deny with `Failed` set and the reason; a timeout uses Codex's wording. A cancelled context returns an error instead.
- **Breaker.** 3 denials in a row, or 10 in the last 50; failed reviews count. When open, `Review` returns `AskUser` without calling the model, and the approver asks the user (headless: deny). `Reset` closes it; the approver calls it at each new user turn. The breaker is per run, the conversation per session.
- **Configuration.** `approvals_reviewer = "auto_review" | "user"` (default `auto_review`, S8) and `[review] model`, `effort`, `timeout`. Defaults: `codex-auto-review` on `openai-codex`, the session model elsewhere, `low`, `90s`. `app.Resolve` returns them as `Resolved.ApprovalsReviewer` and `Resolved.Review`.
- **Measurement.** Each review is an `engine.AutoReviewed` event with its time, tokens, whether it continued the conversation or a fork, and its commands; `uah exec --json` writes it as `auto_reviewed`.

### Request shape and cost

Responses API requests per review: the system message (the policy, about 3,500 tokens with the command guidelines and `exec_command`'s definition), the conversation so far, one new user message, `reasoning.effort = "low"`, and `prompt_cache_key = "uah-review-<session ID>"`; one more request per round of commands. On the ChatGPT backend the cache hit stops short of the previous request's end by roughly 500 to 900 tokens, so a delta pays for that tail and the new message; a whole transcript pays for everything after the system message.

| | Input tokens | Output tokens | Latency | When |
| --- | --- | --- | --- | --- |
| uah, first review (probe) | 3,749, none cached | 150 to 180 | 4 to 5 s | The first escalation of a session |
| uah, delta review (probe) | 4,232, 3,584 cached | 69 | 4.0 s | A later escalation, no commands |
| uah, delta review with a command (probe) | 8,889 over two calls, 7,168 cached | 355 | 11.0 s | `rm -rf` of a directory, inspected first |
| Codex, first review | the fixed prompt (about 4.5K) plus its sub-session's other messages and tools, plus up to 30K transcript | not stated | not stated | Each escalation, network approval, MCP call, patch, and permission request under auto-review |
| Codex, later reviews | the delta since the last review, on a cached session | | | |

### Review conversation: measured

**Replay of long sessions** (`TestProbeReplay`, `codex-auto-review`, low): two recorded `go-large-repo-guide` sessions (50 tool calls, 7 prompts each), every fifth tool call reviewed as an escalation, once with the whole transcript each time and once in one conversation. All 21 reviews per mode allowed in both.

| Session | Mode | Reviews | Input | Cached | Uncached | Output | Review time |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 1 | whole transcript | 10 | 54,851 | 19,712 (36%) | 35,139 | 662 | 31.8 s |
| 1 | conversation | 10 | 107,531 | 85,760 (80%) | 21,771 (−38%) | 237 | 19.9 s (−37%) |
| 2 | whole transcript | 11 | 55,694 | 25,344 (46%) | 30,350 | 723 | 23.7 s |
| 2 | conversation | 11 | 113,376 | 93,440 (82%) | 19,936 (−34%) | 235 | 19.2 s (−19%) |

The conversation sends about twice the input, but four fifths of it from the cache: at the agentbench prices (uncached $1.25, cached $0.125, output $10 per million) session 1 costs $0.053 a whole transcript at a time and $0.040 in one conversation (−24%). The saving grows with the transcript: a whole transcript re-sends up to its budget (about 8K tokens) uncached each time, a delta only what is new.

**agentbench A/B** (2026-10-04, gpt-6.1-sol at high, auto mode, `-owner-env`, `curl-local-api`, `curl-parallel-endpoints`, and `home-config-surgery`, 4 repeats per arm, run twice: once at `-parallel 12` one arm after the other, once with both arms at once at `-parallel 6` each). The control is the commit before, which only adds the `auto_reviewed` event.

| | Control | Conversation |
| --- | --- | --- |
| Runs passed | 24 of 24 | 24 of 24 |
| Reviews, all allowed | 54 | 56 |
| Reviews that continued the conversation / ran on a fork | — | 11 / 24 |
| Reviewer commands | — | 0 |
| Input tokens per review (mean) | 4,144 | 4,492 |
| Cached per review (mean) | 1,920 (46%) | 1,902 (42%) |
| Uncached per review (mean) | 2,224 | 2,590 |
| Output tokens per review (mean) | 83 | 64 |
| Review time (median / p90) | 2.7 s / 4.1 s | 2.6 s / 3.9 s |
| Run wall time (median) | 55 s | 55 s |

These tasks are short: a session has one to four reviews over a transcript of a few entries, and the four reviews of `curl-parallel-endpoints` run at once, so all but one are forks of an empty conversation. There the conversation changes little. A later review sends 1,742 uncached tokens instead of 1,897 (−8%) and caches 3,304 instead of 2,636. First reviews cost more: `exec_command`'s definition and Codex's investigation text add about 250 tokens. The new system prompt was also cold more often (590 cached tokens per first review against 1,173): every other uah reviewer on the account shares the old prompt's cache. The verdicts did not change (every review allowed in both arms), and no reviewer ran a command. The win is in long sessions, as the replay shows.

### Open decisions (defaults taken)

1. **Failed reviews count toward the breaker.** Default: yes, so a reviewer that keeps failing hands over to the user after 3. Codex resets the count on a failed review.
2. **Read-only investigation.** Done: the reviewer runs Codex's `exec_command` in the read-only sandbox (above). Codex also offers `write_stdin`, `view_image`, `exec`, and `wait`; uah's commands cannot outlive their 10 s, so it offers only `exec_command`.
3. **Transcript delta and session reuse.** Done: one conversation per session with Codex's deltas and forks (above). Unlike Codex, uah starts anew at 100,000 input tokens instead of compacting the reviewer, and a compaction or rewind of the session does not start anew: the reviewer's transcript is built from the session's events, not its history, so it only grows.
4. **Assistant text and tool output left out.** Default: out, as the research recommended. Codex includes them as untrusted evidence under its budget.
5. **Budget sizes.** Default: `DefaultLimits` above (about 8K context tokens at most). Codex allows 20K message and 10K tool tokens.
6. **API-key `openai` provider.** Default: the session model. Codex uses `gpt-5.6-luna` there.
7. **A configurable policy.** `Request.Policy` replaces the default policy, as Codex's `[auto_review] policy` does, but there is no configuration key yet. Default: Codex's policy.
8. **Project files set the reviewer.** Default: a trusted project's `.uagent/config.toml` may set `approvals_reviewer` and `[review]`, like every other key. Making them user-file-only would stop a repository from turning auto-review on.
9. **No structured output.** The runner's `llm.Request` has no response-format field, so the JSON is asked for in the prompt only; Codex passes `final_output_json_schema`. Default: prompt plus strict parsing and retries.
10. **Effort on models without reasoning.** Default: `low` is always sent. Codex sends `low` only when the model lists it; a provider that rejects the field needs `[review] effort` or a runner-side check.
11. **Timeout.** Default: Codex's 90 s, which now also bounds the reviewer's commands. The research suggested 30 s; `[review] timeout` changes it.

## As built (phase 3)

The reviewer (`internal/review`) is wired in the embedded engine (`autoreview.go`): each run builds it on the session's own model client, puts it in front of the session's asker, and resets its circuit breaker on each user message. Allow runs the action, deny refuses it with the reviewer's reason, and ask_user (breaker open) passes to PermissionRequest hooks and the user; headless runs deny then. The context comes from the session's events: one ordered transcript of the user's messages and answers, the tool calls, and their results without output; each session keeps one review conversation, and each review sends only what is new (see [what uah does](#what-uah-does)). MCP calls that need approval go through the same path. Each verdict is an `engine.AutoReviewed` event, shown as a line in the TUI and in `uah run` progress.

