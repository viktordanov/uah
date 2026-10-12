package agents

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/viktordanov/uah/internal/hooks"
	"github.com/viktordanov/uah/internal/session"
)

// maxStopContinuations stops SubagentStop hooks from keeping a child going
// forever, as the session does for Stop hooks.
const maxStopContinuations = 5

// stopCheck is a completed child's SubagentStop check: how many messages
// it had when it finished, and the status it reports unless a hook keeps it
// going.
type stopCheck struct {
	gen    int
	status Status
}

// checkStop runs the SubagentStop hooks for a child that finished. A hook
// that blocks with a reason sends the reason to the child as its next
// message, as Claude Code does; otherwise the child reports its status.
func (m *Manager) checkStop(c *child, check stopCheck) {
	m.mu.Lock()
	in := m.stopInput(c, check.status.Message)
	ctx := c.asks // closing or interrupting the child ends its hooks too
	m.mu.Unlock()
	d := m.template().Hooks.Run(ctx, in)

	m.mu.Lock()
	if c.stopCheck == check.gen {
		c.stopCheck = 0
	}
	if c.closed || c.gen != check.gen {
		m.mu.Unlock()

		return // closed, or the parent sent a message, or the child woke, meanwhile
	}
	reason := strings.TrimSpace(d.Reason)
	if d.Block && reason != "" && c.stopStreak < maxStopContinuations {
		c.stopStreak++
		m.mu.Unlock()
		if _, err := m.submit(c, reason, session.SendAfterRun); err == nil {
			return
		}
		m.mu.Lock()
	}
	c.status, c.stopStreak = check.status, 0
	m.mu.Unlock()
	m.notify(c)
}

// stopInput is the SubagentStop payload, Claude Code's: the parent's
// session, and the child's ID, type, transcript, and final answer. It
// holds m.mu.
func (m *Manager) stopInput(c *child, answer string) hooks.Input {
	in := m.agentInput(hooks.SubagentStop, c)
	in.StopHookActive, in.LastAssistantMessage = c.stopStreak > 0, answer

	return in
}

// startHooks runs the SubagentStart hooks for a child that is about to get
// its first message. They only observe; the spawn waits for them, so they
// run before any hook of the child's own.
func (m *Manager) startHooks(ctx context.Context, c *child) {
	runner := m.template().Hooks
	if !runner.Has(hooks.SubagentStart, "") {
		return
	}
	m.mu.Lock()
	in := m.agentInput(hooks.SubagentStart, c)
	m.mu.Unlock()
	runner.Run(ctx, in)
}

// agentInput is the payload a subagent event starts from, Claude Code's:
// the parent's session, and the child's ID, type, and transcript. It holds
// m.mu.
func (m *Manager) agentInput(event hooks.Event, c *child) hooks.Input {
	p := m.parents[c.parent].Request
	in := hooks.Input{
		Event: event, SessionID: c.parent, Cwd: p.Workspace, Model: p.Model,
		AgentID: c.id, AgentType: first(c.role, defaultRole),
	}
	if dir := m.tmpl.SessionsDir; dir != "" {
		in.TranscriptPath = filepath.Join(dir, c.parent+".session.jsonl")
		in.AgentTranscriptPath = filepath.Join(dir, c.id+".session.jsonl")
	}

	return in
}
