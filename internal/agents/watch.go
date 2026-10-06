package agents

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/session"
)

// Bounds of a child's event log and of a view's backlog.
const (
	maxLogged  = 20000
	watchQueue = 4096
)

var _ session.AgentWatcher = (*Manager)(nil)

// WatchAgent follows one of the parent's children, found by ID or
// nickname: its runs from before this process, the events since it opened,
// and the ones that follow.
func (m *Manager) WatchAgent(parentID, ref string) (*session.AgentWatch, error) {
	m.mu.Lock()
	c := m.byRef(parentID, ref)
	if c == nil || c.s == nil {
		m.mu.Unlock()

		return nil, fmt.Errorf("no agent %q in this session (see /agents)", ref)
	}
	events := slices.Clone(c.log)
	next := make(chan core.Event, watchQueue)
	if c.closed {
		close(next)
	} else {
		c.subs = append(c.subs, next)
	}
	opened, closed, dir := c.opened, c.closed, m.tmpl.SessionsDir
	m.mu.Unlock()

	// A closed child dropped its log, so its runs since it opened come
	// from disk too.
	var history []session.LoadedRun
	if runs, err := session.Load(filepath.Dir(dir), c.id); err == nil {
		history = slices.DeleteFunc(runs, func(r session.LoadedRun) bool { return !closed && !r.Record.Result.StartedAt.Before(opened) })
	}

	return &session.AgentWatch{
		ID: c.id, Nickname: c.nickname, History: history, Events: events, Next: next, Closed: closed,
		Stop:        func() { m.unwatch(c, next) },
		Send:        func(text string, when session.When) error { _, err := m.submit(c, text, when); return err },
		SteerQueued: func() error { return m.steerQueued(c) },
		Interrupt:   func() { m.interruptTree(c) },
	}, nil
}

// interruptTree stops the live runs of a child and its descendants, as the
// parent's interrupt does for all of its children.
func (m *Manager) interruptTree(c *child) {
	m.mu.Lock()
	stop := m.subtree(c)
	for _, x := range stop {
		x.cancelAsks()
		x.stopSent = x.sending > 0 // settle stops the run a send is starting
	}
	m.mu.Unlock()
	for _, x := range stop {
		if s := x.session(m); s != nil {
			go func() { _ = s.Interrupt() }() // never wait on a child's loop from the caller's
		}
	}
}

// byRef finds the parent's latest child with the ID, or the nickname
// (any case). It holds m.mu.
func (m *Manager) byRef(parentID, ref string) *child {
	var found *child
	for _, c := range m.children {
		if c.parent != parentID || (c.id != ref && !strings.EqualFold(c.nickname, ref)) {
			continue
		}
		if found == nil || (found.closed && !c.closed) || c.opened.After(found.opened) {
			found = c
		}
	}

	return found
}

// unwatch ends a view's subscription.
func (m *Manager) unwatch(c *child, next chan core.Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if i := slices.Index(c.subs, next); i >= 0 {
		c.subs = slices.Delete(c.subs, i, i+1)
		close(next)
	}
}

// record logs a child's event and passes it to its views. A view that
// fell a whole queue behind is closed rather than holding up the child.
// It holds m.mu.
func (c *child) record(e core.Event) {
	c.log = append(c.log, e)
	// Trimmed in steps of a quarter, so a long child does not copy the
	// whole log on every event.
	if len(c.log) > maxLogged+maxLogged/4 {
		c.log = slices.Clone(c.log[len(c.log)-maxLogged:])
	}
	c.subs = slices.DeleteFunc(c.subs, func(next chan core.Event) bool {
		select {
		case next <- e:
			return false
		default:
			close(next)

			return true
		}
	})
}

// endViews closes the child's views once its session closed. It holds
// m.mu.
func (c *child) endViews() {
	for _, next := range c.subs {
		close(next)
	}
	c.subs = nil
}
