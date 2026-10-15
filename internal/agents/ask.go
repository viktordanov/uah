package agents

import (
	"context"
	"strings"
	"time"

	"github.com/viktordanov/uah/internal/approval"
)

// askFor asks a child's approvals through its parent's session, with the
// child's nickname in the justification. The prompt stays open after the
// parent's run ends; it ends when the child is interrupted or closed.
func (m *Manager) askFor(c *child) approval.Ask {
	return func(ctx context.Context, p approval.Prompt) approval.Answer {
		m.mu.Lock()
		ask := m.parents[c.parent].Ask
		asks := c.asks
		m.mu.Unlock()
		if ask == nil {
			return approval.DeclineBecause("no one can approve commands of agent " + c.nickname + " in this session")
		}
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		stop := context.AfterFunc(asks, cancel) //nolint:contextcheck // the child's interrupt or close also ends the prompt
		defer stop()
		p.Justification = strings.TrimSpace("agent " + c.nickname + ": " + p.Justification)
		m.asking(c, 1)
		defer m.asking(c, -1)

		return ask(ctx, p)
	}
}

// asking counts a child's open approval in or out, and restarts its stall
// clock: the time the user takes is not the child's.
func (m *Manager) asking(c *child, n int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c.asking += n
	c.active = time.Now()
	if c.quiet != nil {
		c.quiet.Reset(m.cfg.StallAfter)
	}
}
