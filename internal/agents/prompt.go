package agents

import (
	"fmt"
	"strings"
)

// The tool descriptions, parameter schemas, and spawn_agent guidance are
// adapted from openai/codex rust-v0.156.1
// (codex-rs/core/src/tools/handlers/multi_agents_spec.rs), Copyright 2025
// OpenAI, licensed under the Apache License, Version 2.0
// (http://www.apache.org/licenses/LICENSE-2.0). Changes: the `items`
// inputs are left out, the forked-workspace and upload wording is dropped
// because children share the parent's workspace, wait_agent promises no
// completion notification, and both say what costs or keeps the cache.
const spawnGuidance = `Spawn a sub-agent for a well-scoped task. Returns the spawned agent id plus the user-facing nickname when available. The agent works in the background in your workspace, with your sandbox and approvals. Spawned agents inherit your current model by default. Do not set the ` + "`model`" + ` field unless the user explicitly asks for a different model.

Do not spawn sub-agents unless the user or applicable AGENTS.md/skill instructions explicitly ask for sub-agents, delegation, or parallel agent work.
Requests for depth, thoroughness, research, investigation, or detailed codebase analysis do not count as permission to spawn.
Agent-role guidance below only helps choose which agent to use after spawning is already authorized; it never authorizes spawning by itself.

### When to delegate vs. do the subtask yourself
- First, quickly analyze the overall user task and form a succinct high-level plan. Identify which tasks are immediate blockers on the critical path, and which tasks are sidecar tasks that are needed but can run in parallel without blocking the next local step. As part of that plan, explicitly decide what immediate task you should do locally right now. Do this planning step before delegating to agents so you do not hand off the immediate blocking task to a submodel and then waste time waiting on it.
- Use a subagent when a subtask is easy enough for it to handle and can run in parallel with your local work. Prefer delegating concrete, bounded sidecar tasks that materially advance the main task without blocking your immediate next local step.
- Do not delegate urgent blocking work when your immediate next step depends on that result. If the very next action is blocked on that task, the main rollout should usually do it locally to keep the critical path moving.
- Keep work local when the subtask is too difficult to delegate well and when it is tightly coupled, urgent, or likely to block your immediate next step.

### Designing delegated subtasks
- Subtasks must be concrete, well-defined, and self-contained.
- Delegated subtasks must materially advance the main task.
- Do not duplicate work between the main rollout and delegated subtasks.
- Avoid issuing multiple delegate calls on the same unresolved thread unless the new delegated task is genuinely different and necessary.
- Narrow the delegated ask to the concrete output you need next.
- Agents share your workspace: for code-edit subtasks, decompose work so each delegated task has a disjoint write set, and ask the agent to list the file paths it changed in its final answer.

### After you delegate
- Call wait_agent very sparingly. Only call wait_agent when you need the result immediately for the next critical-path step and you are blocked until it returns.
- Do not redo delegated subagent tasks yourself; focus on integrating results or tackling non-overlapping work.
- While the subagent is running in the background, do meaningful non-overlapping work immediately.
- Do not repeatedly wait by reflex.
- Close agents with close_agent when you no longer need them; open agents count toward the limit.

### Parallel delegation patterns
- Run multiple independent information-seeking subtasks in parallel when you have distinct questions that can be answered independently.
- Split implementation into disjoint codebase slices and spawn multiple agents for them in parallel when the write scopes do not overlap.
- Delegate verification only when it can run in parallel with ongoing implementation and is likely to catch a concrete risk before final integration.`

// spawnDescription is the spawn_agent description: the roles, then the
// guidance.
func spawnDescription(roles []Role) string {
	if len(roles) == 0 {
		return spawnGuidance
	}
	var b strings.Builder
	b.WriteString("Available agent types (agent_type):\n")
	for _, r := range roles {
		fmt.Fprintf(&b, "- `%s`: %s\n", r.Name, oneLine(r.Description))
	}
	b.WriteString("\n")
	b.WriteString(spawnGuidance)

	return b.String()
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// The tools' parameter schemas, Codex's v1.
const (
	spawnSchema = `{"type":"object","properties":{` +
		`"message":{"type":"string","description":"Initial plain-text task for the new agent."},` +
		`"agent_type":{"type":"string","description":"Agent type override for the new agent. Omit to inherit the parent agent type with a full-history fork; otherwise, ` + "`default`" + ` is used. The types are listed in the tool description."},` +
		`"fork_context":{"type":"boolean","description":"True forks the current thread history into the new agent; false or omitted starts with only the initial prompt. A forked agent reuses your prompt cache only with your model and reasoning effort, so omit model and reasoning_effort when forking."},` +
		`"model":{"type":"string","description":"Model override for the new agent. Omit unless an explicit override is needed."},` +
		`"reasoning_effort":{"type":"string","enum":["low","medium","high","xhigh","max","ultra"],"description":"Reasoning effort override for the new agent. Omit to inherit the parent effort."}` +
		`},"required":["message"],"additionalProperties":false}`
	sendSchema = `{"type":"object","properties":{` +
		`"target":{"type":"string","description":"Agent id to message (from spawn_agent)."},` +
		`"message":{"type":"string","description":"Message to send to the agent."},` +
		`"interrupt":{"type":"boolean","description":"True interrupts the current task and handles this message immediately; false or omitted queues it."}` +
		`},"required":["target","message"],"additionalProperties":false}`
	waitSchema = `{"type":"object","properties":{` +
		`"targets":{"type":"array","items":{"type":"string"},"description":"Agent ids to wait on. Pass multiple ids to wait for whichever finishes first."},` +
		`"timeout_ms":{"type":"number","description":"Timeout in milliseconds. Defaults to 30000, min 10000, max 3600000. Prefer longer waits (minutes) to avoid busy polling."}` +
		`},"required":["targets"],"additionalProperties":false}`
	closeSchema = `{"type":"object","properties":{` +
		`"target":{"type":"string","description":"Agent id to close (from spawn_agent)."}` +
		`},"required":["target"],"additionalProperties":false}`
	resumeSchema = `{"type":"object","properties":{` +
		`"id":{"type":"string","description":"Agent id to resume."}` +
		`},"required":["id"],"additionalProperties":false}`
)

// userAgentNote ends the descriptions of the tools that reach a running
// agent: a /review's reviewer is the user's (reviewagent.go).
const userAgentNote = " An agent a /review started (role review) is the user's: leave it alone unless the user asks you to act on it."

// The other tools' descriptions, Codex's v1, with userAgentNote.
const (
	sendDescription   = "Send a message to an existing agent. Use interrupt=true to redirect work immediately. You should reuse the agent by send_input if you believe your assigned task is highly dependent on the context of a previous task." + userAgentNote
	waitDescription   = "Wait for agents to reach a final status. Completed statuses may include the agent's final message. Returns empty status when timed out. Other work continues while you wait. It returns as soon as an agent finishes, so a long timeout costs nothing: prefer the default or a longer timeout to short polls, which waste turns."
	closeDescription  = "Close an agent and any open descendants when they are no longer needed, and return the target agent's previous status before shutdown was requested. Completed agents remain open and count toward the concurrency limit until closed. Don't keep agents open for too long if they are not needed anymore. Closing a /review's reviewer stops the review." + userAgentNote
	resumeDescription = "Resume a previously closed agent by id so it can receive send_input and wait_agent calls. Agents from an earlier session of this conversation must be resumed before other calls reach them."
)
