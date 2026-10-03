# Third-party notices

uah is licensed under the Apache License, Version 2.0 (see [LICENSE](LICENSE)). Parts of it are adapted from the projects below; each adapted file says so in its header, with the upstream file it came from.

## OpenAI Codex

<https://github.com/openai/codex>, at `rust-v0.156.1` (the base instructions and the model catalog: `rust-v0.159.1`). Copyright 2025 OpenAI. Licensed under the Apache License, Version 2.0; the full text is in [LICENSE](LICENSE) and [internal/sandbox/seatbelt/LICENSE-codex](internal/sandbox/seatbelt/LICENSE-codex), and Codex's NOTICE is in [internal/sandbox/seatbelt/NOTICE-codex](internal/sandbox/seatbelt/NOTICE-codex).

Adapted in uah:

| uah | From Codex |
| --- | --- |
| `internal/patch/parse.go`, `update.go`, `apply.go`, and their tests | `codex-rs/apply-patch` (the patch grammar, parser, context matching, and applier) |
| `internal/patch/apply_patch.lark`, `internal/patch/tool.go` | The `apply_patch` tool's Lark grammar and description at `rust-v0.159.1`, verbatim (`codex-rs/core/assets/tools/apply_patch.lark`, `codex-rs/core/src/tools/handlers/apply_patch_spec.rs`) |
| `internal/sandbox/seatbelt/base.sbpl`, `network.sbpl`, `internal/sandbox/seatbelt.go` | `codex-rs/sandboxing` (the Seatbelt profiles and their assembly) |
| `internal/sandbox/bwrap.go` | `codex-rs/linux-sandbox/src/bwrap.rs` |
| `internal/sandbox/env.go` | `codex-rs/protocol/src/shell_environment.rs`, `codex-rs/config/src/shell_environment_policy.rs` |
| `internal/sandbox/denied.go` | `codex-rs/sandboxing/src/denial.rs` |
| `internal/goal/prompts/*.md`, `internal/goal/context.go`, `internal/goal/tools.go` | The goal's continuation, budget-limit, and objective-updated templates verbatim, the `user_goal` record and the hidden-context wrapper, and the goal tools' names, descriptions, schemas, and messages, at `main` b741e48 (`codex-rs/ext/goal/templates/goals`, `codex-rs/ext/goal/src/spec.rs`, `tool.rs`, `codex-rs/core/src/context/user_goal.rs`, `internal_model_context.rs`) |
| `internal/review/review.go` | The auto-review (guardian) prompt, `codex-rs/prompts/templates/guardian` |
| `internal/compaction/compaction.go` | The compaction prompt and summary prefix, `codex-rs/prompts/templates/compact` |
| `internal/codereview/prompts/rubric.md`, `exit_success.xml`, `exit_interrupted.xml`, `internal/codereview/codereview.go`, `output.go` | `/review`: the review rubric and the hand-over messages, verbatim (`codex-rs/prompts/templates/review`), the target prompts and hints (`codex-rs/prompts/src/review_request.rs`), and the findings format (`codex-rs/protocol/src/review_format.rs`) |
| `internal/agents/prompt.go` | The multi-agent tool descriptions and schemas, `codex-rs/core/src/tools/handlers/multi_agents_spec.rs` |
| `internal/instructions/codex_prompt.md`, `codex.go` | Codex's base instructions for gpt-6.1-sol, verbatim (`model_messages.instructions_template` in `codex-rs/models-manager/models.json`) |
| `internal/instructions/default_prompt.md`, `default_prompt.diff`, `codex.go` | Codex's base instructions for gpt-6.1-sol, **modified** by uah: the identity, the tool names (`Bash`, `SkillUse`), questions asked in the final message, Codex's terminal wording for visuals, and the Apps and Plugins sections removed. `default_prompt.diff` is the complete change |
| `internal/instructions/codex.go` (`SubagentNote`) | Two lines of the subagent role text, `model_messages.multi_agent.role.subagent` in `codex-rs/models-manager/models.json` |
| `internal/instructions/environment.go` | The `<environment_context>` format (`codex-rs/core/src/context/world_state/environment.rs`, `core/src/context/environment_context.rs`) |
| `internal/tui/render/markdown/table.go` | Table layout: padding, gaps, rules, fitting columns to the width, and the key/value records (`codex-rs/tui/src/markdown_render.rs`, `markdown_render/table_key_value.rs`) |
| `internal/models/bundled.json` | The bundled model catalog, `codex-rs/models-manager/models.json` (a subset of its fields) |
| `internal/engine/codexauth/refresh.go` | The ChatGPT token refresh: the request, the client ID and endpoint, and how a refusal is classified (`codex-rs/login/src/auth/manager.rs`, `login/src/oauth/client.rs`, `login/src/oauth/error.rs`) |
| `internal/cmdparse/parsed.go`, `parse.go`, `summarize.go`, `operands.go`, `format.go`, `script.go`, and `parse_test.go` | The command classifier at `rust-v0.159.1`: `codex-rs/shell-command/src/parse_command.rs` (reads, listings, searches, and its tests), `codex-rs/protocol/src/parse_command.rs` (`ParsedCommand`), and the word-only command check in `codex-rs/shell-command/src/bash.rs`, parsed with `mvdan.cc/sh` instead of tree-sitter; without PowerShell |

Many other parts follow Codex's behavior (configuration keys, rules, approvals, MCP, subagents); those are uah's own code written against Codex's documented behavior and source, and the design records in [docs/design](docs/design) cite the Codex files they follow.

## rust-shlex

<https://github.com/comex/rust-shlex>, 2.0.1, the crate Codex's command parser splits and quotes words with. Copyright 2015 Nicholas Allegra (comex). Licensed under the MIT License or the Apache License, Version 2.0, at your option; uah uses it under the Apache License, Version 2.0 (see [LICENSE](LICENSE)).

| uah | From rust-shlex |
| --- | --- |
| `internal/cmdparse/shlex.go` | `src/bytes.rs` (`split` and `try_join`: POSIX word splitting and the quoting strategies) |

## smithy-go (a benchmark fixture)

<https://github.com/aws/smithy-go>, v1.22.4, as published to the Go module proxy. Copyright Amazon.com, Inc. or its affiliates. Licensed under the Apache License, Version 2.0. It is not part of uah: the agent benchmark copies it whole as the repository of one task, with its `LICENSE` and `NOTICE`, so the agent works in a real codebase of a few hundred files.

| uah | From smithy-go |
| --- | --- |
| `tools/agentbench/testdata/tasks/go-large-repo-bug/repo/` | The whole module at v1.22.4, **modified**: three lines removed from `encoding/httpbinding/path_replace.go` (the `len(path) < newLen` branch of `replacePathElement`), the bug the task plants. The task's `solution/` restores the file as published, and its `check/` holds a copy of `encoding/httpbinding/path_replace_test.go` as published and a test of uah's own |

## unreal-agent

<https://github.com/unreallabsai/unreal-agent>, v0.1.1. uah's runtime, [uah-core](https://github.com/viktordanov/uah-core) (used as a library throughout), derives from unreal-agent v0.2.0 and is distributed under the MIT License below, with this notice kept; uah-core began as the fork <https://github.com/viktordanov/unreal-agent>. Adapted in uah:

| uah | From unreal-agent |
| --- | --- |
| `internal/engine/codexauth/codexauth.go` | `harness/llm/clients/openaicodex/credentials.go` (loading the ChatGPT credentials) |
| `internal/engine/embedded/clients.go` | `harness/llm/clients/openai/client.go`, `openrouter/client.go`, `fireworks/client.go`, `ollama/client.go`, and `openaicodex/client.go` (each provider's Responses client: endpoint, headers, prompt cache key placement, request extensions, the codex base URL check and error wrapping), and `harness/primitives/remote.go` (`newRemoteHTTPClient`'s transport settings) |

```
MIT License

Copyright (c) 2026 Unreal Labs

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```
