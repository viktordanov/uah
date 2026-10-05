package agents

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/instructions"
	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/internal/sessionfile"
)

// Codex's refusals for spawn_agent.
var (
	errDepth    = errors.New("Agent depth limit reached. Solve the task yourself.")                                                              //nolint:staticcheck // Codex's message, word for word
	errForkType = errors.New("Full-history forked agents inherit the parent agent type; omit agent_type, or spawn without a full-history fork.") //nolint:staticcheck // as above
)

// spawn starts a child with its first message and returns at once. With
// fork_context, the child starts from a copy of the parent's history.
func (m *Manager) spawn(ctx context.Context, call engine.AgentCall, a spawnArgs) (spawnResult, error) {
	parentID := call.ParentID
	if err := m.checkDepth(parentID); err != nil {
		return spawnResult{}, err
	}
	if err := m.checkModel(ctx, a.Model, a.ForkContext); err != nil {
		return spawnResult{}, err
	}
	role, rec, err := m.spawnRole(parentID, a)
	if err != nil {
		return spawnResult{}, err
	}
	rec.CallID, rec.Task = call.CallID, a.Message
	c, err := m.start(parentID, session.NewSubagentID(), role, rec, nil) //nolint:contextcheck // children outlive the call that started them
	if err != nil {
		return spawnResult{}, err
	}
	if a.ForkContext {
		if err := m.fork(ctx, c, call); err != nil {
			m.discard(c)

			return spawnResult{}, err
		}
	}
	m.startHooks(ctx, c)
	message := a.Message
	if note := m.firstNote(c); note != "" {
		message += "\n\n" + note
	}
	if _, err := m.submit(c, message, session.SendAfterRun); err != nil {
		m.discard(c)

		return spawnResult{}, err
	}
	if ctx.Err() != nil { // the parent's run stopped meanwhile
		m.closeTree(c)

		return spawnResult{}, fmt.Errorf("the spawn stopped: %w", ctx.Err())
	}

	return spawnResult{AgentID: c.id, Nickname: c.nickname}, nil
}

// forkDepthNote tells a fork at the depth limit what errDepth would.
const forkDepthNote = "You are a forked agent at the agent depth limit: spawn_agent and resume_agent fail here. Solve the task yourself."

// firstNote ends a new child's first message, so its system prompt and prefix stay its parent's
// (one message: the runner may ask the model as soon as the first of two arrives).
func (m *Manager) firstNote(c *child) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch {
	case !c.forked:
		return instructions.SubagentNote
	case m.depth(c.id) >= m.cfg.MaxDepth:
		return forkDepthNote
	}

	return ""
}

// hasNote reports whether a child's messages carry its note. A child
// spawned while the note was in the system prompt has it in none, and its
// next message gets it (noteDue). An unreadable file counts as having it.
func hasNote(dir, id string) bool {
	_, page, err := sessionfile.Read(filepath.Join(dir, id+".session.jsonl"), sessionfile.BeforeFirst, 0)
	if err != nil {
		return true
	}
	for _, it := range page.Items {
		var in sessionfile.Input
		if it.Kind == sessionfile.KindInput && it.Decode(&in) == nil && in.Kind == sessionfile.InputExternal {
			if text, err := in.Text(); err == nil && strings.Contains(text, instructions.SubagentNote) {
				return true
			}
		}
	}

	return false
}

// spawnRole is the new child's role and record. A fork keeps the parent's
// agent type, as in Codex, and none of its settings: the parent's history
// and system prompt already carry them.
func (m *Manager) spawnRole(parentID string, a spawnArgs) (Role, record, error) {
	rec := record{Model: a.Model, Effort: a.Effort, Fork: a.ForkContext}
	if !a.ForkContext {
		role, err := m.role(a.AgentType)
		rec.Role = role.Name

		return role, rec, err
	}
	if a.AgentType != "" {
		return Role{}, rec, errForkType
	}
	m.mu.Lock()
	inherited := ""
	if p, ok := m.children[parentID]; ok {
		inherited = p.role
	}
	m.mu.Unlock()
	role, err := m.role(inherited)
	if err != nil {
		role = Role{Name: inherited}
	}
	rec.Role = role.Name

	return role, rec, nil
}

// checkDepth refuses a spawn or resume from a session at the depth limit.
// Such a session is offered the tools only when it was forked, so its
// tools match its parent's.
func (m *Manager) checkDepth(parentID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.depth(parentID)+1 > m.cfg.MaxDepth {
		return errDepth
	}

	return nil
}

// forker is the engine's Forker, if it has one.
func (m *Manager) forker() (engine.Forker, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ce, ok := m.eng.(childEngine); ok {
		f, ok := ce.Engine.(engine.Forker)

		return f, ok
	}

	return nil, false
}

// fork copies the parent's history, as it was when the model made the
// spawn call, into the child's new session.
func (m *Manager) fork(ctx context.Context, c *child, call engine.AgentCall) error {
	f, ok := m.forker()
	switch {
	case !ok:
		return errors.New("fork_context is not available on this engine")
	case call.CallID == "":
		return errors.New("fork_context needs the spawn call's ID")
	}
	if err := f.Fork(ctx, call.ParentID, c.id, call.CallID); err != nil {
		return fmt.Errorf("failed to fork the context: %w", err)
	}

	return nil
}

// start registers a child within the limit, opens its session, and
// watches it. A resumed child (resumed, its sidecar) opens its earlier
// session, with the settings it saved.
func (m *Manager) start(parentID, id string, role Role, rec record, resumed *session.Sidecar) (*child, error) {
	m.mu.Lock()
	parent, ok := m.parents[parentID]
	if !ok || m.eng == nil {
		m.mu.Unlock()

		return nil, errors.New("subagents are not available in this session")
	}
	if old, ok := m.children[id]; ok && !old.closed { // resumed twice at once
		m.mu.Unlock()

		return old, nil
	}
	if open := m.openIn(m.treeRoot(parentID)); open >= m.cfg.MaxThreads {
		m.mu.Unlock()

		return nil, fmt.Errorf("agent limit reached: %d agents are open; close one with close_agent first", open)
	}
	c := newChild(id, parentID, role.Name, m.nickname(parentID, role, rec.Nickname))
	c.forked, c.callID, c.task = rec.Fork, rec.CallID, rec.Task
	var saved *session.Saved
	if resumed != nil {
		c.status = Status{State: engine.AgentPendingInit}
		saved = cmp.Or(resumed.Settings, &session.Saved{})
	}
	m.children[id] = c
	eng, opts, key := m.eng, m.childOptions(parent, c, role, rec, saved), m.treeRoot(parentID)
	c.model, c.effort = opts.Settings.Model, opts.Settings.Effort
	m.mu.Unlock()
	if f, ok := m.forker(); ok {
		f.SetCacheKey(id, key) // as Codex keys every agent by its tree's session
	}
	m.scope(id, role, rec)

	// Children outlive the call that started them; Close stops them.
	s, err := session.Open(context.Background(), eng, opts) // children outlive the spawning call
	if err != nil {
		m.mu.Lock()
		delete(m.children, id)
		m.mu.Unlock()

		return nil, fmt.Errorf("failed to start the agent: %w", err)
	}
	if !m.adopt(c, s) {
		_ = s.Close()

		return nil, errors.New("the agent was closed before it started")
	}
	rec.Nickname = c.nickname
	_ = writeRecord(opts.SessionsDir, id, rec) // only resume_agent's nickname and role depend on it
	go m.watch(c)
	m.notify(c)

	return c, nil
}

// adopt gives a child its session unless it was closed meanwhile.
func (m *Manager) adopt(c *child, s *session.Session) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c.closed {
		return false
	}
	c.s = s

	return true
}

// find returns the parent's child by ID, closed or not. It holds m.mu.
func (m *Manager) find(parentID, id string) (*child, bool) {
	c, ok := m.children[id]
	if !ok || c.parent != parentID || c.s == nil {
		return nil, false
	}

	return c, true
}

// notFound says a child is unknown, and how to reach one from an earlier
// process; a /review's reviewer is never one.
func (m *Manager) notFound(parentID, id string) error {
	sc, found, err := session.ReadSidecar(m.template().SessionsDir, id)
	switch {
	case id == "" || err != nil || !found:
	case m.isReview(id, sc):
		return errReviewer(id)
	case sc.Parent == parentID:
		return fmt.Errorf("agent with id %s is not loaded; resume it with resume_agent first", id)
	}

	return fmt.Errorf("agent with id %s not found", id)
}

// isReview reports whether a session is a /review's reviewer: one this
// process started, or one whose sidecar says so.
func (m *Manager) isReview(id string, sc session.Sidecar) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	return sc.Review || m.reviews[id]
}

// errReviewer refuses to resume a /review's reviewer whose review is not
// running here: a reviewer is reached as a live agent while its review
// runs, and its findings reached the agent when it ended.
func errReviewer(id string) error {
	return fmt.Errorf("agent with id %s is the reviewer of a /review the user started, which is not running here: it cannot be resumed. "+
		"Its findings came to you with the user's message after it ended", id)
}

// send gives a child another message; with interrupt, it stops the
// child's live run and handles the message at once.
func (m *Manager) send(parentID, id, message string, interrupt bool) (string, error) {
	m.mu.Lock()
	c, ok := m.find(parentID, id)
	closed := ok && (c.closed || c.ending)
	if ok && interrupt {
		c.cancelAsks()
	}
	if ok {
		c.stopStreak = 0
	}
	reserved := ok && !closed && c.review
	if reserved { // before the interrupt: its idle session must not end the review first
		c.reserved++
	}
	m.mu.Unlock()
	if reserved {
		defer func() {
			m.mu.Lock()
			c.reserved--
			m.mu.Unlock()
			select {
			case c.released <- struct{}{}:
			default: // one wake is enough: the review reads the rest
			}
		}()
	}
	switch {
	case !ok:
		return "", m.notFound(parentID, id)
	case closed:
		return "", fmt.Errorf("agent with id %s is closed", id)
	}
	when := session.SendAfterRun
	if c.review {
		// The review ends with its run, so a message after it would
		// never arrive: it goes into the live run.
		when = session.SendNow
	}
	if interrupt {
		if err := c.s.Interrupt(); err != nil {
			return "", fmt.Errorf("failed to interrupt the agent: %w", err)
		}
		when = session.SendNow
	}

	return m.submit(c, message, when)
}

// wait returns when any of the children reaches a final status, with every
// final one's status, or when the timeout passes. An unknown child is
// final as not_found.
func (m *Manager) wait(ctx context.Context, parentID string, ids []string, timeout time.Duration) (map[string]Status, bool, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	// The same children are counted down as up, even if resume_agent
	// replaces one meanwhile.
	var watched []*child
	m.mu.Lock()
	for _, id := range ids {
		if c, ok := m.find(parentID, id); ok {
			c.waiters++
			watched = append(watched, c)
		}
	}
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		for _, c := range watched {
			c.waiters--
		}
		m.mu.Unlock()
	}()
	for {
		m.mu.Lock()
		out := map[string]Status{}
		for _, id := range ids {
			c, ok := m.find(parentID, id)
			switch {
			case !ok:
				out[id] = Status{State: engine.AgentNotFound}
			case c.status.Final():
				out[id] = c.status
				if w := c.unhold(); w != nil {
					m.outboxOf(parentID).push(w) // it waits on the parent's session
				}
			}
		}
		changed := m.changed
		m.mu.Unlock()
		if len(out) > 0 {
			return out, false, nil
		}
		select {
		case <-changed:
		case <-timer.C:
			return map[string]Status{}, true, nil
		case <-ctx.Done():
			return nil, false, fmt.Errorf("the wait stopped: %w", ctx.Err())
		}
	}
}

// closeAgent closes a child and its descendants and returns its status
// before it closed.
func (m *Manager) closeAgent(parentID, id string) (Status, error) {
	m.mu.Lock()
	c, ok := m.find(parentID, id)
	var prev Status
	if ok {
		prev = c.status
	}
	m.mu.Unlock()
	if !ok {
		return Status{State: engine.AgentNotFound}, m.notFound(parentID, id)
	}
	if c.review {
		m.stopReviewer(c) // the review ends as interrupted and closes it

		return prev, nil
	}
	m.closeTree(c)

	return prev, nil
}

// closeTree closes a child and its open descendants, which stop counting
// toward the limit at once; their watchers mark them shut down.
func (m *Manager) closeTree(c *child) {
	m.mu.Lock()
	tree := m.subtree(c)
	for _, k := range tree {
		k.closed = true
		k.cancelAsks()
	}
	m.mu.Unlock()
	for _, k := range tree {
		if s := k.session(m); s != nil {
			_ = s.Close()
		}
	}
}

// discard closes a child that failed to start and removes its sidecar and
// agent record, so resume_agent and uah sessions do not offer a child that
// never ran.
func (m *Manager) discard(c *child) {
	m.closeTree(c)
	dir := m.template().SessionsDir
	_ = session.RemoveSidecar(dir, c.id)
	_ = os.Remove(recordPath(dir, c.id))
	m.mu.Lock()
	delete(m.children, c.id)
	m.mu.Unlock()
}

// resume opens a closed child again, or one from an earlier process, as
// long as its sidecar names this parent; an open child reports its status.
func (m *Manager) resume(_ context.Context, parentID, id string) (Status, error) {
	if err := m.checkDepth(parentID); err != nil {
		return Status{State: engine.AgentNotFound}, err
	}
	m.mu.Lock()
	c, ok := m.children[id]
	if ok && c.parent == parentID && !c.closed {
		status := c.status
		m.mu.Unlock()

		return status, nil
	}
	m.mu.Unlock()
	dir := m.template().SessionsDir
	sc, found, err := session.ReadSidecar(dir, id)
	switch {
	case id == "" || err != nil || !found || sc.Source != session.SourceSubagent || sc.Parent != parentID:
		return Status{State: engine.AgentNotFound}, fmt.Errorf("agent with id %s not found", id)
	case m.isReview(id, sc):
		return Status{State: engine.AgentNotFound}, errReviewer(id)
	case session.InUse(dir, id):
		// Nothing starts: a second run of it would fail to start.
		return Status{State: engine.AgentNotFound}, fmt.Errorf("agent with id %s is in use by another run, so it cannot be resumed now", id)
	}
	rec, _ := readRecord(dir, id)
	role, err := m.role(rec.Role)
	if err != nil {
		role = Role{} // the role file is gone: the default agent with the child's model
	}
	due := !rec.Fork && !hasNote(dir, id)
	c, err = m.start(parentID, id, role, rec, &sc) //nolint:contextcheck // children outlive the call that started them
	if err != nil {
		return Status{State: engine.AgentNotFound}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if due && c.gen == 0 {
		c.noteDue = instructions.SubagentNote
	}

	return c.status, nil
}
