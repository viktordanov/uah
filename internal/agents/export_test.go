package agents

import (
	"context"
	"time"

	"github.com/viktordanov/uah/internal/contextusage"
	"github.com/viktordanov/uah/internal/session"
)

// BeforeSubmit runs f before each message goes to a child's session.
func (m *Manager) BeforeSubmit(f func(message string)) { m.beforeSubmit = f }

// Wait is wait_agent without its 10-second minimum, for tests.
func (m *Manager) Wait(ctx context.Context, parentID string, ids []string, timeout time.Duration) (map[string]Status, bool, error) {
	return m.wait(ctx, parentID, ids, timeout)
}

// ChildOptions are the session options a default child of the parent's
// latest run would open with.
func (m *Manager) ChildOptions(parentID string) session.Options {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.childOptions(m.parents[parentID], newChild("child-id", parentID, "", "Ada"), Role{}, record{}, nil)
}

// ContextUsage is /context for a child of this process.
func (m *Manager) ContextUsage(id string) (contextusage.Usage, bool) {
	m.mu.Lock()
	c, ok := m.children[id]
	m.mu.Unlock()
	if !ok || c.s == nil {
		return contextusage.Usage{}, false
	}

	return c.s.ContextUsage()
}

// Waiting reports how many wait_agent calls of the parent are pending on
// the child.
func (m *Manager) Waiting(parentID, id string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := m.find(parentID, id); ok {
		return c.waiters
	}

	return 0
}

// ForkDepthNote is what a fork at the depth limit is told before its task.
const ForkDepthNote = forkDepthNote
