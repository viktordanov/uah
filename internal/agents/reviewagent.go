package agents

import (
	"context"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/session"
)

// reviewRole is the role a /review's reviewer shows as among the parent's
// agents.
const reviewRole = "review"

// reviewTask is the reviewer's task as the main agent sees it.
const reviewTask = "/review started by the user: leave it alone unless the user asks you to act on it"

// addReviewer makes a /review's reviewer one of the parent's agents while
// it runs (the owner's decision, unlike Codex's review thread): listed
// with role review and the task that says it is the user's, so the main
// agent can message, wait on, or stop it with the agent tools when the
// user asks, instead of looking for its session. It takes no place in the
// limit, sends no completion notification (the review's own hand-over
// does), runs no SubagentStop hook, and the parent's interrupt leaves it
// to the session, which stops the review itself. Its events reach the
// child through awaitReview (reviewAgent), not watch.
func (m *Manager) addReviewer(req session.ReviewRequest, rs *session.Session, stop context.CancelFunc) *child {
	m.mu.Lock()
	c := newChild(rs.ID(), req.ParentID, reviewRole, m.nickname(req.ParentID, Role{NicknameCandidates: []string{"Reviewer"}}, ""))
	c.review, c.stopReview, c.task = true, stop, reviewTask
	c.model, c.effort = req.Settings.Model, req.Settings.Effort
	c.s = rs
	m.children[c.id] = c
	m.mu.Unlock()
	m.notify(c)

	return c
}

// endReviewer gives the reviewer its final status when the review ends,
// and closes it: wait_agent returns that status, and send_input says it is
// closed.
func (m *Manager) endReviewer(c *child, a session.ReviewAnswer, err error) {
	m.mu.Lock()
	switch {
	case a.Interrupted:
		c.status = Status{State: engine.AgentInterrupted}
	case err != nil:
		c.status = Status{State: engine.AgentErrored, Message: readable(err.Error())}
	default:
		c.status = Status{State: engine.AgentCompleted, Message: a.Text}
	}
	c.closed = true
	c.cancelAsks()
	c.endViews()
	c.log = nil
	m.mu.Unlock()
	m.notify(c)
}

// reviewAgent is the reviewer as one of the parent's agents, for
// awaitReview: its events update the child, and a message the main agent
// is sending it keeps the review going past an idle session.
type reviewAgent struct {
	m *Manager
	c *child
}

// observe folds one of the reviewer's events into its child, as watch
// does for a spawned one.
func (a reviewAgent) observe(e core.Event) {
	if a.c == nil {
		return
	}
	a.m.mu.Lock()
	changed, _ := a.m.observe(a.c, e)
	a.c.record(e)
	a.m.mu.Unlock()
	if changed {
		a.m.notify(a.c)
	}
}

// end decides that the review ends at an idle session, unless a message
// to the reviewer is under way: then the session's next run answers it.
// Once it returns true, send_input refuses the reviewer, so no message is
// lost between the decision and the review's end.
func (a reviewAgent) end() bool {
	if a.c == nil {
		return true
	}
	a.m.mu.Lock()
	defer a.m.mu.Unlock()
	if a.c.reserved > 0 || a.c.sending > 0 || len(a.c.pending) > 0 {
		return false
	}
	a.c.ending = true

	return true
}

// stopReviewer stops a review the main agent closed: the review ends as
// interrupted, and the session reports it so.
func (m *Manager) stopReviewer(c *child) {
	m.mu.Lock()
	stop := c.stopReview
	m.mu.Unlock()
	if stop != nil {
		stop()
	}
}
