# Agent tuning: uah against Codex

Status: a running record, added to with each benchmark run.

This record collects every measurement made with the agent benchmark ([`tools/agentbench`](../../tools/agentbench/README.md)), what each experiment changed, and what was decided. It is the source for the performance part of release notes. Each experiment's raw results are in [`tools/agentbench/history`](../../tools/agentbench/history), one JSON line per run (local paths are shortened to `~` and `$TMPDIR`).

## Contents

1. [How runs are measured](#how-runs-are-measured)
2. [Where the time goes](#where-the-time-goes)
3. [Baseline](#baseline)
4. [Experiment 1: freeform `apply_patch`](#experiment-1-freeform-apply_patch)
5. [Experiment 2: async prompts](#experiment-2-async-prompts)
6. [Experiment 3: wake policies](#experiment-3-wake-policies)
7. [Experiment 4: cutting turns](#experiment-4-cutting-turns-quick-round)
8. [The full suite, 10 repeats](#the-full-suite-10-repeats)
9. [Lean mode rules](#lean-mode-rules)
10. [Parallel approvals](#parallel-approvals)
11. [Network commands escalated up front](#network-commands-escalated-up-front)
12. [Adaptive effort in chats](#adaptive-effort-in-chats)
13. [Leaving subagents alone](#leaving-subagents-alone)
14. [Decisions](#decisions)
15. [Still running and next](#still-running-and-next)
16. [For release notes](#for-release-notes)

## How runs are measured

- Each run gives `uah exec` or `codex exec` one coding task in a fresh copy of a small repository and checks the result automatically; 35 tasks, each valid only if its check fails on the untouched repository and passes on a reference solution.
- Both harnesses use the same model and effort (gpt-6.1-sol, high), the ChatGPT login, auto mode (auto-review for escalations), the workspace-write sandbox, and the owner's AGENTS.md, RTK.md, and skills. uah gets a throwaway home and state directory per run; Codex runs with `--ephemeral --ignore-user-config`.
- Numbers are medians over a task's repeats; totals are sums of per-task medians. The same task varies by 20–30% between runs, so a difference under about 10% on one task is noise, and a sustained 20%+ over many tasks is real.
- Codex's JSON stream has no per-request timing, so its model time and request count are inferred from the gaps between its commands.

## Where the time goes

Mining the owner's real sessions (243 uah runs, 12.2 hours; 382 Codex tasks) before any benchmark:

- The model is 89% of wall time; tools alone 5%; uah's own overhead is negligible (dispatch 10 ms, wake 4 ms).
- Patch text was 37% of all output tokens, reasoning 38%; high-effort turns were half of all model time.
- 37% of runs spent the first turn on a ritual (loading the always-on skill, reading RTK.md).
- Async tool calls worked as designed (42% of tool time overlapped the model), but the model rarely had useful work to overlap.

So the gains come from fewer and cheaper model turns, not from more tool parallelism.

## Baseline

2026-10-01, 35 tasks × 2 harnesses × 3 repeats, 210 runs ([raw](../../tools/agentbench/history/2026-10-01-baseline.jsonl)).

| | uah 1.6.1 | Codex 0.159.3 |
| --- | ---: | ---: |
| Passed | 104/105 | 103/105 |
| Median wall per task | 120 s | 104 s |
| Total wall | 252 min | 216 min |
| Model requests | 808 | 664 |
| Input tokens | 16.6M | 18.3M |
| Output tokens | 404k (62% patch text) | 342k |
| Estimated cost | $8.64 | $8.51 |

- uah's patches dominated its output: one 5-edit revision of a 38 KB markdown file took 397 s (Codex 107 s) because the model rewrote the whole file in one JSON-escaped patch.
- uah made 125 "patch, then verify in a new turn" pairs against Codex's 43, and spent 20 requests on the startup ritual.
- uah was already faster on the local-API, home-config, branch-review and slow-build tasks, and used fewer input tokens on most tasks.

## Experiment 1: freeform `apply_patch`

uah offered `apply_patch` as a JSON function (`{"input": "*** Begin Patch\n…"}`), so every newline and quote was escaped. Codex rust-v0.159.1 offers only a freeform tool: a Responses API custom tool with a Lark grammar, whose input is the raw patch. The runner fork gained custom tools (v0.4.0) and uah switched behind a flag. 10 edit-heavy tasks × 3, against a control run at the same time ([raw](../../tools/agentbench/history/2026-10-02-exp1-freeform-prompts.jsonl)).

| | JSON function | Freeform | Change |
| --- | ---: | ---: | ---: |
| Total wall | 112 min | 85 min | −24% |
| Output tokens | 201k | 149k | −26% |
| Patch tokens | 148k | 90k | −39% |
| Requests | 229 | 215 | −6% |
| Passed | 30/30 | 30/30 | |

Faster on 8 of 10 tasks; the markdown revision went from 397 s to 106 s (Codex 107 s). With the freeform tool offered, the model also chose scripts over patches where that was faster.

Against Codex on the same 10 tasks: Codex 1785 s, uah before 2158 s, uah freeform 1710 s; output tokens 50.5k / 64.2k / 49.7k.

## Experiment 2: async prompts

The 6 slow tasks (test suites and builds of 10–60 s) × 3, against a control (same raw file as experiment 1).

| | Default prompt | Runner's own prompt | Default + "wait, don't poll" |
| --- | ---: | ---: | ---: |
| Total wall | 37.1 min | 33.7 min (−9%) | 35.6 min (−4%) |
| Model time | 34.5 min | 28.7 min (−17%) | 32.4 min (−6%) |
| Requests | 164 | 149 | 148 |

Prompts alone did not stop the model from polling a running command (`ps`, `sleep 20`, `git status`); the runner woke it on every finished call and for "still running" placeholders, and it filled the wait with cheap commands.

## Experiment 3: wake policies

Five runner-level wake policies, each behind a switch in the runner fork (v0.5.0-rc.1). Quick round: the 6 slow tasks once each ([raw](../../tools/agentbench/history/2026-10-02-wake-quick.jsonl)). Proper round: the three best plus Codex's 30 s foreground cap, 6 slow tasks × 3, with a fresh control on the freeform-only build, plus an edit check on 4 tasks ([raw](../../tools/agentbench/history/2026-10-02-wake-proper.jsonl)).

Proper round, slow tasks (totals of medians):

| | Codex | uah control | No placeholder wakes | Foreground 5 min | Debounce 2 s | Foreground 30 s |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| Wall | 663 s | 650 s | 652 s | 662 s | 686 s | 660 s |
| Model time | 515 s | 594 s | 483 s (−19%) | 495 s | 570 s | 505 s |
| Requests | 35 | 53 | 40 (−25%) | 43 | 45 | 41 |
| Output tokens | 14.3k | 14.9k | 12.7k | 12.5k | 14.9k | 12.9k |
| Estimated cost | $0.44 | $0.42 | $0.34 (−19%) | $0.38 | $0.36 | $0.40 |

- **No placeholder wakes**: a turn's results are delivered when every call it issued has finished; the model is never woken only to hear that a call is still running. On the 50 s suite, requests went from 11 to 6 (Codex 5).
- Wall time did not move: the model already overlapped the long tests, so the gain is model work and cost.
- Edit check (4 tasks × 3): control 459 s, no placeholder wakes 471 s, same requests and tokens; neutral. Codex 517 s.
- Wake-when-all-done never triggered on these tasks; a prompt rewrite for async made things worse (+7% wall).

### Wake variations, 8 repeats

5 tasks (the 50 s and 27 s suites, two slow packages, the slow algorithm, a bug hunt) × 8 repeats per variant, 120 runs, all passed ([raw](../../tools/agentbench/history/2026-10-02-wake-abc.jsonl)).

| | A: hold, 5 min valve | B: 60 s valve | C: release quick results after 10 s |
| --- | ---: | ---: | ---: |
| Wall | 598 s | 594 s | 635 s (+6%) |
| Model time | 451 s | 448 s | 556 s (+23%) |
| Requests | 37.5 | 37.5 | 45 |
| Estimated cost | $0.29 | $0.29 | $0.36 |

With 8 repeats, a task's middle half spans about ±10% of its median (the 50 s suite: 128–145 s), so differences under 10% between variants are noise.

## Experiment 4: cutting turns (quick round)

Three switches, 8 tasks × 1 run each against a control, all passed ([raw](../../tools/agentbench/history/2026-10-02-turn-cutting-quick.jsonl)). One run per task, so these are directions, not results.

| | Wall | Requests | Output tokens | Cost |
| --- | ---: | ---: | ---: | ---: |
| Control | 962 s | 60 | 26.0k | $0.58 |
| Lower effort for turns that only react to tool results | 845 s (−12%) | 56 | 21.4k (−18%) | $0.55 |
| Primed first turn (layout, git status, AGENTS.md includes) | 942 s (−2%) | 56 | 24.9k | $0.54 (−7%) |
| Automatic compile check after an edit | 1104 s (+15%) | 65 | 29.5k | $0.65 |

Lower effort costs about 3 points of prompt-cache hits (the effort level appears to be part of what the cache matches), which gives back part of the saving.

## The full suite, 10 repeats

35 tasks × 10 repeats × 5 groups, 1,750 runs at 65 concurrency, all groups interleaved ([raw](../../tools/agentbench/history/2026-10-02-big-35x10.jsonl)). uah is the build with freeform `apply_patch` and the wake default (runner fork v0.5.1); totals are sums of per-task medians.

| | Codex 0.159.3 | uah default | uah + lower effort | uah + lower effort + primed first turn | uah + corrected preamble |
| --- | ---: | ---: | ---: | ---: | ---: |
| Passed | 348/350 | 344/350 | 342/350 | 342/350 | 344/350 |
| Wall | 4172 s | 4223 s (+1%) | 3431 s (−18%) | **3270 s (−22%)** | 4232 s |
| Model time | 3949 s | 3994 s | 3192 s | 3054 s | 4014 s |
| Requests | 222 | 256 | 237 | 231 | 246 |
| Output tokens | 114k | 111k | 86k | 81k | 113k |
| Estimated cost | $2.90 | $2.50 (−14%) | $2.10 | **$2.00 (−31%)** | $2.50 |
| Cached input | 85.7% | 87.2% | 86.8% | 86.4% | 86.0% |
| Faster than Codex on | | 13 of 35 tasks | | 33 of 35 tasks | |

- uah's default (freeform patch and the wake policy) is level with Codex on time and 14% cheaper; with lower effort for follow-up turns and a primed first turn it is 22% faster and 31% cheaper, and faster on 33 of 35 tasks.
- The corrected preamble changed nothing measurable.
- Pass rates: the extra failures are concentrated in two tasks, each failing the same way in every harness. The branch review misses the swallowed `Record` error (Codex 8/10, uah 5–6/10), and the findings report keeps a red herring (Codex 10/10, uah 7–9/10). Both are review-quality misses, not regressions from the switches; the branch review is uah's weak spot against Codex.

## Lean mode rules

Adaptive effort was called Lean mode during these experiments; the setting is now `adaptive_effort`, a session setting like the effort (`--adaptive-effort`, `/adaptive`, `/config`).

Which follow-up requests should go lower? Four rules, each with the primed first turn, at 1 step (E−1) and 2 steps (E−2), against Lean off: 35 tasks × 5 repeats, gpt-6.1-sol at high effort in auto mode, uah only ([raw](../../tools/agentbench/history/2026-10-02-lean-rules.jsonl)). Totals are sums of per-task medians; the cache columns are over all runs' requests after the first, split by whether the request's effort was the one before it (agentbench's `same_effort_*` and `changed_effort_*`).

- **r0:** lower for any request whose input since the model's last output is only tool results.
- **r1:** lower only when every one of those results is a plain confirmation: an applied patch, a passing test or build, or a short command that is not a read, listing, search, or dump.
- **r2:** r1, and one level above E when the same command failed in each of the last two turns.
- **r3:** r2, and never lower in a reading-heavy session (a review, an investigation, a report, or no edit in 4 turns).

| | Passed | Wall | Model time | Requests | Output tokens | Cost | Cached input | Same effort cached | Changed effort cached (requests) |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| Lean off | 172/175 | 5168 s | 4934 s | 242 | 113.7k | $2.53 | 86.0% | 85.5% | — |
| **1 step, r0** | 172/175 | **3928 s (−24%)** | 3703 s | 224 | **80.4k (−29%)** | **$2.01 (−21%)** | 85.4% | 87.8% | 79.1% (295) |
| 1 step, r1 | 172/175 | 4775 s (−8%) | 4546 s | 238 | 102.4k | $2.70 (+6%) | 79.1% | 81.0% | 74.1% (424) |
| 1 step, r2 | 171/175 | 4937 s (−4%) | 4710 s | 241 | 107.1k | $2.85 (+12%) | 78.6% | 81.2% | 73.3% (434) |
| 1 step, r3 | 173/175 | 4837 s (−6%) | 4606 s | 235 | 104.4k | $2.67 (+5%) | 80.1% | 83.4% | 71.5% (347) |
| 2 steps, r1 | 172/175 | 4692 s (−9%) | 4457 s | 241 | 100.0k | $2.64 (+4%) | 80.0% | 83.1% | 73.5% (415) |
| 2 steps, r2 | 171/175 | 4720 s (−9%) | 4491 s | 237 | 99.7k | $2.70 (+6%) | 78.8% | 82.2% | 71.3% (422) |
| 2 steps, r3 | 171/175 | 4959 s (−4%) | 4724 s | 245 | 106.4k | $2.69 (+6%) | 80.6% | 83.4% | 71.5% (334) |

- r0 wins clearly: −24% wall, −29% output tokens, and −21% cost against Lean off, at the same pass rate.
- **The cache finding.** A request whose effort differs from the one before it hits the prompt cache less: about 71–74% of its input cached under r1 to r3, and 79% under r0, against about 85–88% at the same effort. The finer rules switch the effort between requests more often (415 to 434 changes, against 295 under r0), so they lose more of the cache than the lower effort saves, and cost more than Lean off. r0's runs keep long stretches of follow-ups at one effort, and change only at a user message.
- No rule won back the branch review's or the findings report's misses; the pass counts are within one run of each other.
- 2 steps with r0 is being measured against 1 step.

### One step or two

R0 at 1 and 2 steps against Lean off: 12 tasks (half reading or judgment work: bug hunt, investigation, branch review, findings report, overview; half edits) × 3 repeats, 108 runs ([raw](../../tools/agentbench/history/2026-10-02-lean-steps.jsonl)).

| | Lean off | 1 step | 2 steps |
| --- | ---: | ---: | ---: |
| Passed | 35/36 | 34/36 | 34/36 |
| Wall | 2041 s | 1480 s (−27%) | 1330 s (−35%) |
| Requests | 91 | 85 | 85 |
| Output tokens | 48.5k | 32.9k (−32%) | 28.9k (−40%) |
| Estimated cost | $0.99 | $0.83 (−16%) | $0.74 (−25%) |

The reading tasks alone took 1197 s with Lean off, 861 s at 1 step and 735 s at 2 steps. The failures are spread: both of 2-steps' are the branch review, which also fails with Lean off; 1 step missed the spec and the findings report once each. Three repeats cannot separate a one- or two-run difference in pass rate, so the speed is the result and the quality reads as "no visible loss". Both levels stay: 1 step as the safe one, 2 steps as the aggressive one.

### Escalation on failure

Should a follow-up after a failure think harder? The rule, behind a switch (`UAH_EXPERIMENTS=lean-escalate`): a follow-up carries a failure when one of its tool results failed (a command that exited nonzero or did not run, a refusal, a tool error, or an `apply_patch` that did not apply), and each one in a row takes a step back up, to E at most. At 2 steps the first goes at E−1 and the second at E; at 1 step the first goes at E. A follow-up without a failure, or a user message, goes back down. The same 12 tasks × 3 repeats, at high effort, 144 runs ([raw](../../tools/agentbench/history/2026-10-02-lean-escalate.jsonl)).

| | Passed | Wall | Estimated cost | Cached input | Escalated requests |
| --- | ---: | ---: | ---: | ---: | ---: |
| 1 step | 34/36 | 1676 s | $0.88 | 86.3% | — |
| 1 step, escalation | 34/36 | 1663 s | $0.99 (+13%) | 82.4% | 24 |
| 2 steps | 35/36 | 1339 s | $0.73 | 85.4% | — |
| 2 steps, escalation | 35/36 | 1324 s | $0.77 (+5%) | 81.0% | 23 |

The escalations fell mostly on the test-fixing, race, slow-suite, bug-hunt, branch-review and findings-report tasks. The failures are the same with and without it (the branch review, and once the spec at 1 step). Requests whose effort changed hit the cache 67–80%, against about 88% for the rest, so escalation changed the effort more often and cost more. Dropped: no quality gain, cache cost.

## Parallel approvals

The coordinator translated a response's calls one at a time, and each escalated call waited there for its auto-review, so four escalations in one response were reviewed one after another, about 3.5 s each. Now the approvals of a response's calls start together when the response is stored, and each call's translation takes its own decision ([engine README](../../internal/engine/README.md#approvals-for-parallel-calls)). The control is uah before the change, the variant `parapprove` uah after it: 2 tasks × 5 repeats, gpt-6.1-sol at high effort in auto mode, uah only, 20 runs ([raw](../../tools/agentbench/history/2026-10-02-parallel-approvals.jsonl)). `curl-parallel-endpoints`, added for this, snapshots four slow, independent endpoints of a local API, so the model asks for four escalated curl calls in one response; `curl-local-api` pages through an API, so its escalations come mostly one per response.

The wait columns are medians per run. The approval waits are agentbench's `approval_wait_ms`, every call's wait from being issued to starting, summed; the critical path sums, over the responses, the longest wait among each response's calls, which is what the run waited.

| Task | Wall ctl | Wall var | Ratio | Approval waits ctl | var | Critical path ctl | var | Escalations in one response |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| curl-parallel-endpoints | 64.8 s | 49.4 s | 0.76 | 31.1 s | 12.8 s | 11.9 s | 3.5 s | 4 |
| curl-local-api | 97.1 s | 85.3 s | 0.88 | 15.3 s | 13.0 s | 13.0 s | 10.6 s | 1–3 |

- All 20 runs passed. On four escalations in one response, the run waited for one review instead of four: 3.5 s against 11.9 s, and the wall time fell by a quarter.
- On `curl-local-api` most responses carry one escalation, so there is little to overlap; its difference is within the task's run-to-run spread.
- In a smoke run with a prompt that did not mention the sandbox, the model first tried the four calls in the sandbox, at once, then asked once to run them all in one escalated command. The task's prompt says the calls need to run outside the sandbox, as a user who knows the sandbox would, and then the model asked for the four escalations together in every run.

## Network commands escalated up front

The sandbox has no network unless `network_access` is set: on macOS seatbelt denies every socket, and on Linux bwrap gives the command a network namespace of its own, so localhost is out of reach as well. The `Bash` tool's description said only "no network access"; Codex, in its on-request permissions prompt, tells the model to rerun a command with `require_escalated` after it fails with a likely network error, and the model does that unprompted. So it tried its curl calls in the sandbox, read the failures, and then asked for the escalations: one wasted request per run, and the [parallel approvals](#parallel-approvals) only showed when the task's prompt said the sandbox has no network. The change adds one sentence to the sandbox note in `Bash`'s description, only when the sandbox has no network (read-only and workspace-write; yolo has no note, and `network_access = true` gets none):

> A command that needs the network, localhost included, fails in the sandbox, so run it with require_escalated from the first try.

It goes in the tool description, which follows the permission mode from request to request, rather than in the system prompt, which is fixed for the session's prompt cache and cannot know the mode.

Two rounds, gpt-6.1-sol at high effort in auto mode, uah only. The control is uah v1.7.4 (`main`); `full` is the sentence above plus "send independent ones as separate calls in the same response"; `short` is the sentence alone. `curl-parallel-nohint`, added for this, is `curl-parallel-endpoints` without its prompt's hint that the sandbox has no network, so only the tool description tells the model. Round 1 ran the control and `full` side by side: the network tasks × 8 (`curl-local-api` × 16) and four Go fix tasks × 6, 128 runs ([raw](../../tools/agentbench/history/2026-10-03-netprompt.jsonl)). Round 2 ran all three side by side: `curl-parallel-nohint` and `curl-local-api` × 8 and the Go tasks × 6, and `short` alone on the other two network tasks × 8, 136 runs ([raw](../../tools/agentbench/history/2026-10-03-netprompt-short.jsonl)). The table pools both rounds. Wasted tries are curl calls run in the sandbox that failed for want of network (runs with any in brackets); "together" counts runs with two or more escalations in one response; the waits are medians per run, the critical path as in [parallel approvals](#parallel-approvals).

| Task | Arm | Passed | Wall | Requests | Escalations per run | Wasted tries | Together | Approval wait | Critical path | Output tokens |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| curl-parallel-nohint | control | 16/16 | 60.3 s | 6 | 3.6 | 64 (16 runs) | 14/16 | 11.3 s | 3.0 s | 1286 |
| | full | 16/16 | 45.2 s | 5 | 3.8 | 0 | 15/16 | 11.9 s | 3.3 s | 897 |
| | **short** | 8/8 | **44.8 s (−26%)** | 5 | 4.0 | **0** | 8/8 | 12.3 s | 3.6 s | 920 |
| curl-local-api | control | 24/24 | 85.8 s | 7 | 4.0 | 23 (23 runs) | 7/24 | 13.3 s | 10.1 s | 1807 |
| | full | 24/24 | 75.1 s | 6 | 3.7 | 0 | 4/24 | 12.1 s | 9.7 s | 1613 |
| | **short** | 8/8 | **79.2 s (−8%)** | 6 | 4.1 | **0** | 1/8 | 15.5 s | 11.1 s | 1738 |
| curl-parallel-endpoints (hint in the prompt) | control | 8/8 | 46.3 s | 5 | 4.0 | 0 | 8/8 | 16.9 s | 4.8 s | 949 |
| | full | 8/8 | 47.5 s | 5 | 4.0 | 0 | 8/8 | 14.4 s | 3.5 s | 962 |
| | short | 8/8 | 47.0 s | 4.5 | 4.0 | 0 | 8/8 | 13.4 s | 3.2 s | 964 |
| home-config-surgery (escalation, no network) | control | 8/8 | 64.4 s | 7 | 1.0 | 0 | 0/8 | 2.5 s | 2.5 s | 1380 |
| | full | 8/8 | 62.4 s | 7 | 1.0 | 0 | 0/8 | 3.2 s | 3.2 s | 1284 |
| | short | 8/8 | 59.4 s | 7 | 0.6 | 0 | 0/8 | 3.4 s | 3.4 s | 1275 |

On the four Go tasks with no network (`go-cli-exit-codes`, `go-data-race`, `go-fix-failing-tests`, `go-vet-fixes`) no arm escalated anything, and all 120 runs passed. Wall times, as sums of per-task medians:

| Go tasks | Control | Full | Short |
| --- | ---: | ---: | ---: |
| Round 1 (× 6) | 406.2 s | 444.8 s (+9%) | — |
| Round 2 (× 6, side by side) | 416.0 s | 432.3 s (+4%) | 412.3 s (−1%) |

- **The sentence does what it says.** With it, no run tried a network command in the sandbox: 0 wasted tries in 56 runs on the two tasks without a hint, against 87 in the control's 40. On `curl-parallel-nohint` the model sends the four escalated curl calls in its first network response, one request fewer, and the run takes 45 s instead of 60 s (−26%), the same as the task with the hint (47 s). In the control the four calls went together too, in 14 of 16 runs, once the sandbox had failed them, so the gain is the wasted request; the reviews overlap as before.
- **`curl-local-api` gains less.** The model guesses the API's paths (`/items`, `/`) before reading the README, so some escalations still fail, now with a 404 outside the sandbox instead of a refused connection inside it; the wasted sandbox tries are gone, and the wall time is −8% (short) and −12% (full), within the task's spread (53 to 139 s in the control).
- **The second clause cost time elsewhere.** "Send independent ones as separate calls in the same response" gave no gain on the network tasks over the short sentence, and on the Go tasks `full` was slower in both rounds (+5% over 48 runs per arm, p ≈ 0.006 in a permutation test on log wall time per task), with about 5% more output tokens and 5% more tool calls. `short` measured level with the control (−1%, 24 runs per arm). uah's prompt already asks for independent calls in parallel, and the model followed it here.
- No needless escalations: none on the Go tasks, and on `home-config-surgery`, whose escalation is a write under `~`, the count and pass rate did not change.

Kept: the short sentence ([ledger](../ledger.md) 106).

## Adaptive effort in chats

The cost model, its charts, and the projections are in [Adaptive effort costs](adaptive-effort-costs.md).

The provider keeps a prompt cache per effort. A controlled test sent the same 33k prefix twice: at the same effort, 99.5% of it was cached; at another effort, 0%. With adaptive effort (R0), each later user turn of a chat therefore misses twice:

1. Its first request (the opener) goes at E. E's cache holds only what the previous opener sent, so the opener re-bills all of the previous turn's tool work.
2. Its first follow-up goes at the lowered effort and misses the new message and one response. On turn 1 it misses everything, because that cache is empty.

The suite had only one-message tasks, so (1) was never measured. Five chat tasks (`chat-*`, 6 or 7 messages each, with real read, edit, and test work) and `go-large-repo-guide` (7 messages) were run with adaptive effort off, at 1 step, at 2 steps, and with everything at medium: 6 tasks × 5 repeats × 4 groups, 120 runs. All were gpt-6.1-sol at high effort in auto mode, uah only ([raw](../../tools/agentbench/history/2026-10-02-multiturn.jsonl); [requests](../../tools/agentbench/history/2026-10-02-multiturn-requests.jsonl), from which `go run ./tools/agentbench -turns -out <requests file>` rebuilds the [per-turn report](../../tools/agentbench/README.md#the-per-turn-report)). Totals are sums of per-task medians, for the main agent; the prices are $1.25, $0.125, and $10 per million uncached input, cached input, and output tokens.

| | Off | 1 step (R0) | 2 steps (R0) | All medium, off |
| --- | ---: | ---: | ---: | ---: |
| Passed | 30/30 | 30/30 | 30/30 | 30/30 |
| Wall | 6040 s | 4139 s (−31%) | 3302 s (−45%) | 3834 s (−37%) |
| Uncached input | 839k | 1074k (+28%) | 909k (+8%) | 575k (−31%) |
| Cached input | 15.43M | 11.43M | 9.69M | 10.45M |
| Output tokens | 157k | 103k (−34%) | 81k (−49%) | 97k (−38%) |
| Estimated cost | $4.57 | $3.85 (−16%) | $3.15 (−31%) | $2.96 (−35%) |

- As recorded, off passed 29/30 and 1 step 28/30. All three failures were in `chat-go-jobqueue`: its check restored the original `store/memory_test.go`, which deleted a helper the agent had added there and used from another test. That overlay is removed, and all 20 of the task's diffs pass the corrected check.
- **Miss (1) is the previous turn's work.** In 249 of 260 later turns at 1 or 2 steps, the opener's uncached input was within 20% of the context's growth since the previous opener; the mean was 9.9k tokens. It grows with the size of a turn, not with the size of the context: by context at the opener, the misses averaged 7.1k under 32k, 7.4k at 32k to 64k, 14.1k at 64k to 128k, and 15.9k above.
- **At 1 step, miss (1) costs about what the lower effort saves on the same turns.** Paired with the control turn by turn, the extra uncached input over the suite costs about $0.50, and the output saved is worth about $0.53. With the runs' requests kept the same, R0 at 1 step costs 2% more than off. The measured −16% comes from shorter sessions: with less output, every later request re-reads a smaller context. At 2 steps the saving is 1.2 to 4 times the miss.
- **Miss (2) is small after turn 1**, about what off misses. On turn 1 the empty cache adds about 8k tokens per task.
- The reasoning tokens the API reports are small for this model, 3k to 10k per turn over the 6 tasks at high, against 20k to 26k of output. The lower effort saves mostly visible output (patches and text) and time.

**Where the misses are.** All four groups ran the same 190 user messages (30 sessions), with about 5.5 model requests per message and no subagents.

- A later opener's median miss is 517 tokens with adaptive effort off and 9,704 at 1 step. That happens 5.3 times per session, and over the 30 sessions it adds 2.0M uncached tokens at 1 step and 1.6M at 2 steps.
- A later first follow-up after the switch misses about what off's first follow-up misses at the same place: about 5k of new content, the answer and the tool results.
- Turn 1's first follow-up misses 11k against off's 4.9k, because the lowered effort's cache is empty.

Over the 30 sessions, against off, at the bench prices:

| | Switching misses | Output saved (reasoning) | Shorter sessions | Net |
| --- | ---: | ---: | ---: | ---: |
| 1 step | +$2.44 | −$2.72 (−$1.56) | −$3.17 | −$3.45 (−15%) |
| 2 steps | +$1.87 | −$3.91 (−$2.00) | −$4.79 | −$6.83 (−30%) |
| All medium | 0 | −$3.13 (−$1.76) | −$4.57 | −$7.70 (−34%) |

"Shorter sessions" is the rest of the difference: with less output, every later request re-reads a smaller context. At 1 step, the switching misses take 90% of the output saving. All-medium was the cheapest and passed 30/30, so on these chats high effort bought nothing the checks can see. The checks are functional, though, and 30 of 30 cannot rule out a loss of a few points. Whether high effort pays needs tasks where medium sometimes fails, graded by more than pass or fail (mutation tests, recall of planted findings, a blind pairwise judge), at about 10 repeats.

**Replays.** `-turns` replays each run's main-agent requests under other rules, with a cache model. Each effort keeps the longest prompt sent at it, and a request finds cached what that prompt covers, in 128-token blocks. A request moved to the other effort has its output scaled by the measured follow-up output ratio: high over medium 1.47, high over low 1.95. The model reproduces the runs: $3.76 against a measured $3.85 at 1 step, with the same cached share, 91.6%. The replay keeps each run's requests, so it does not count the shorter sessions a cheaper rule also brings.

| Rule, replayed on the adaptive runs | 1 step | 2 steps |
| --- | ---: | ---: |
| Off | $3.69 | $3.31 |
| R0, as now | $3.76 (+2%) | $3.02 (−9%) |
| A later user message lowered too when its miss at E is over 8k | **$3.30 (−11%)** | **$2.66 (−20%)** |
| The same, over 16k | $3.44 (−7%) | $2.79 (−16%) |
| R0 below 64k of context, off above | $3.79 (+3%) | $3.14 (−5%) |
| R0 below 128k of context, off above | $3.79 (+3%) | $3.07 (−7%) |

- Lowering a later user message keeps the session on one cache after turn 1 and removes miss (1). Over 8k, it keeps E for a message after a turn with little tool work, whose miss is small.
- Turning adaptive effort off above a context size helps nothing. It gives up the output saving, which is largest there, and keeps miss (1) below the size.
- The benchmark sends a follow-up as soon as the turn before it ends. When a person pauses for longer than the cache keeps an idle prefix, both caches expire. Then R0's first follow-up misses the whole context a second time, and the sticky rule misses it once, as off does. So in a real chat the rule gains more than this.

Next: the sticky rule behind an experiment switch, checked for quality on these chats and on the reading and judgment tasks of [One step or two](#one-step-or-two). All-medium passed 30/30 here, but the branch review has been sensitive to effort. R0 stays until then: in chats it is still −16% (1 step) and −31% (2 steps) in cost, and −31% and −45% in wall time.

## Leaving subagents alone

The owner's sessions showed parents micromanaging their subagents: 46 waits, 21 of them timed out; 36 messages, 22 of them status checks, "hurry", or re-asks; and 194 parent requests while children worked. The causes and the changes are in [the subagents record](subagents.md#round-4-leaving-agents-alone): the completion notification now reaches the parent's live run, a wait lasts 4.5 minutes and says a timeout is normal, a child's status keeps every answer, a wait does not repeat an answer the parent was told, and the tool text says to leave a running agent alone. agentbench now counts the main agent's subagent use (`agent_use`), and has three tasks that ask for subagents (`go-subagents-*`).

v1.9.5 (A) against the change as built (C), 4 tasks × 8 runs each, in the same window: gpt-6.1-sol at high effort in auto mode, owner's environment, adaptive effort at 2 steps ([raw](../../tools/agentbench/history/2026-10-06-subagentcalm.jsonl)).

| | A | C | Change |
| --- | ---: | ---: | ---: |
| Passed | 31/32 | 31/32 | |
| Wall (sum of task medians) | 1177 s | 1193 s | +1% |
| Input tokens | 12.57M | 11.19M | −11% |
| Output tokens | 138k | 133k | −4% |
| Estimated cost | $5.14 | $4.83 | −6% |
| Waits (timed out) | 90 (39) | 68 (1) | |
| Messages and interrupts to a working agent | 2 | 1 | |
| Parent requests while agents worked | 140 | 95 | −32% |

An earlier window, with the change before its last step (B), gave the same against 36 more runs of A: the pass rate level or better, wall time level, input −10%, and no timed-out waits against 33. Kept.

## Decisions

| Date | Decision | Ledger |
| --- | --- | --- |
| 2026-10-02 | `apply_patch` is a freeform tool only, as in Codex; old sessions' JSON calls are still read and replayed | [90](../ledger.md) |
| 2026-10-02 | No placeholder wakes is the default, with a 5-minute safety valve (a call still running after 5 minutes wakes the model with its output so far); debounce, wake-when-all-done and both foreground variants were removed | [91](../ledger.md) |
| 2026-10-02 | The prompt stays Codex's adapted prompt; prompt-only async changes did not pay off | |
| 2026-10-02 | The wake valve stays at 5 minutes (A); a 60 s valve (B) measured the same and releasing quick results early (C) was worse | [91](../ledger.md) |
| 2026-10-02 | Automatic checks after an edit are dropped: the model still ran the tests after each edit, so the check added work | |
| 2026-10-02 | The corrected preamble is dropped, from uah and the runner fork (v0.5.2): it changed nothing measurable | |
| 2026-10-02 | Lower effort for follow-up turns and the primed first turn become Lean mode, a setting off by default (`lean = "off" | "1-step" | "2-steps"`, `/config`): a request after tool results only goes one or two effort levels below the user's, never below low | [92](../ledger.md) |
| 2026-10-02 | Lean mode keeps r0, every follow-up after tool results lower; r1 to r3 and their classifier are removed: changing the effort between requests more often cost more cache than the lower effort saved ([Lean mode rules](#lean-mode-rules)) | [92](../ledger.md) |
| 2026-10-02 | Escalation on failure is dropped: a step back up per failing follow-up in a row brought no quality gain and cost cache ([Escalation on failure](#escalation-on-failure)) | [92](../ledger.md) |
| 2026-10-02 | Lean mode is renamed adaptive effort and becomes a session setting like the effort: the session keeps it in its sidecar, `--adaptive-effort` wins, `adaptive_effort` is the default for new sessions, and `/adaptive` or `/config` changes the current session from its next model request | [92](../ledger.md) |
| 2026-10-02 | The approvals of one response's calls run at once: hooks, auto-reviews, and prompts start when the response is stored, an interrupt ends them, and "don't ask again" settles the other open prompts it covers ([Parallel approvals](#parallel-approvals)) | [94](../ledger.md) |
| 2026-10-03 | Without network in the sandbox, `Bash`'s description tells the model that a network command, localhost included, fails there and should ask for `require_escalated` from the first try; the clause asking for separate calls in one response is left out, since it slowed tasks without network ([Network commands escalated up front](#network-commands-escalated-up-front)) | [106](../ledger.md) |
| 2026-10-06 | Subagents are left alone: the completion notification goes into the parent's live run, `wait_agent` waits 4.5 minutes (at least 1) and says a timeout is normal, a child's status keeps every answer, a wait does not repeat an answer the parent was told, and the tool text says to message a running agent only with news from the user, an answer, or a failure ([Leaving subagents alone](#leaving-subagents-alone)) | [140](../ledger.md) |

## Still running and next

- Nothing is running. Next: adaptive effort's sticky rule for later user messages ([Adaptive effort in chats](#adaptive-effort-in-chats)), and a decision model for effort routing against adaptive effort ([ledger](../ledger.md)).

## For release notes

- uah's `apply_patch` is now a freeform tool, as in Codex: on edit-heavy tasks −24% wall time and −26% output tokens, and uah is now faster than Codex on those tasks (1710 s against 1785 s over 10 tasks).
- The model is no longer woken just to hear that a command is still running: on long builds and test suites, −25% model requests and −19% model time and cost, with a 5-minute safety valve for commands that never end.
- With both, uah matches Codex on wall time on slow tasks and is cheaper (estimated $0.34 against $0.44 on the 6 slow tasks). Over the full suite (35 tasks × 10 repeats), uah's default is level with Codex on wall time and 14% cheaper.
- New setting, adaptive effort (`/adaptive`, `/config`, `--adaptive-effort`, `adaptive_effort`; off by default): the model thinks one or two effort levels less on turns that only follow tool results, and a new session starts with the workspace's context. A session keeps it, as it keeps its effort. On the full suite (35 × 10) at 1 step, uah is 22% faster and 31% cheaper than Codex, and faster on 33 of 35 tasks. On 12 tasks × 3, against off, 1 step cut wall time by 27% and cost by 16%, and 2 steps by 35% and 25%, at the same pass rate. Raising the effort again after failures was measured and left out: no quality gain, and it cost prompt-cache hits.
- Escalations the model asks for together are reviewed together: four auto-reviews in one response now take about one review's time (3.5 s against 11.9 s), −24% wall time on that task, and an interrupt during reviews stops them at once.
- Network commands go out of the sandbox on the first try: the `Bash` tool now tells the model that the sandbox blocks the network, localhost included, so it asks for the escalation up front instead of after a failed run. On a task of four API calls whose prompt does not mention the sandbox, −26% wall time and one model request fewer; no change on tasks without network.
- Subagents are left alone: the main agent now hears that one finished during its own turn, waits instead of polling, and no longer messages running agents for status. On four subagent tasks × 8 against v1.9.5: no timed-out waits (39 of 90 before), 32% fewer parent requests while agents work, 11% fewer input tokens, the same pass rate and wall time.
- The agent benchmark has 40 tasks, including a 27,000-line open-source repo, follow-up prompts, and a session long enough to compact ([ledger P7](../ledger.md), [agentbench](../../tools/agentbench/README.md)); its smoke run passed all 9 runs.
