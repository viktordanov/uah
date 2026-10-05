package agents

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/hooks"
	"github.com/viktordanov/uah/internal/session"
)

// nicknames are given to children of roles without nickname candidates.
var nicknames = []string{
	"Ada", "Babbage", "Curie", "Darwin", "Euler", "Faraday", "Gauss", "Hopper",
	"Hypatia", "Kepler", "Lovelace", "Maxwell", "Noether", "Pascal", "Turing", "Volta",
}

// child is a spawned or resumed session. The manager's lock guards its
// fields.
type child struct {
	id, parent, role, nickname string
	s                          *session.Session
	status                     Status
	// started is when the child's current work began; opened when its
	// session opened in this process.
	started, opened time.Time
	closed          bool
	// forked is a child started with fork_context.
	forked bool
	// callID and task are the spawn call's ID and message; model and
	// effort the child's settings.
	callID, task, model, effort string
	// gen counts the messages sent. sending are the ones being submitted,
	// pending the ones submitted that the session has not queued yet, and
	// early the ones it queued before their submit returned, so an Idle from
	// before a message does not end the child's work.
	gen, sending int
	// stopSent is an interrupt that came while a message was being sent:
	// the session had no run to stop yet, so settle stops the run the
	// message starts once the last send returns.
	stopSent bool
	// notified is the gen the parent was last told about (completionNote).
	notified int
	// waiters counts the parent's wait_agent calls pending on this child;
	// their result tells the parent, so no notification is sent.
	waiters int
	// held takes back the parent's held notification; waited is the gen a wait returned (unhold).
	held           func()
	waited         int
	// told is the gen whose notification went into the parent's live run,
	// so a wait that returns that status need not repeat its answer.
	told int
	pending, early map[string]bool
	// last is the last run's result; failed is a run that did not start;
	// cause is the last error the run reported, such as the provider's.
	last          *core.Result
	failed, cause string
	// answers are the answers of the runs since the child was last idle:
	// a message sent while it works starts another run, and its status
	// keeps every answer, not only the last.
	answers []string
	// asks ends the child's open approvals when it is interrupted or
	// closed; cancel ends it and a new one follows.
	asks   context.Context
	cancel context.CancelFunc
	// noteDue is the role note a resumed child's next message ends with:
	// the child was spawned while the note was in the system prompt
	// (hasNote).
	noteDue string
	// stopStreak counts SubagentStop hooks that kept the child going.
	stopStreak int
	// review marks a /review's reviewer (reviewagent.go), and stopReview
	// stops its review.
	review     bool
	stopReview context.CancelFunc
	// reserved counts the sends to a reviewer that are under way, from
	// before their interrupt; ending is set once its review decided to end,
	// after which no send reaches it.
	reserved int
	ending   bool
	// released wakes the review when a reservation ends.
	released chan struct{}
	// log are the session's events since it opened, and subs the views
	// that follow them (see watch.go).
	log  []core.Event
	subs []chan core.Event
}

func newChild(id, parent, role, nickname string) *child {
	c := &child{
		id: id, parent: parent, role: role, nickname: nickname, started: time.Now(), opened: time.Now(), status: Status{State: engine.AgentRunning},
		pending: map[string]bool{}, early: map[string]bool{},
	}
	c.asks, c.cancel = context.WithCancel(context.Background())

	return c
}

// cancelAsks ends the child's open approvals. It holds m.mu.
func (c *child) cancelAsks() {
	c.cancel()
	c.asks, c.cancel = context.WithCancel(context.Background())
}

// session is the child's session, nil before it has one.
func (c *child) session(m *Manager) *session.Session {
	m.mu.Lock()
	defer m.mu.Unlock()

	return c.s
}

// nickname picks the preferred name, or the first candidate, that no open
// child of the parent has. It holds m.mu.
func (m *Manager) nickname(parentID string, role Role, preferred string) string {
	used := map[string]bool{}
	for _, c := range m.children {
		if c.parent == parentID && !c.closed {
			used[c.nickname] = true
		}
	}
	if preferred != "" && !used[preferred] {
		return preferred
	}
	candidates := role.NicknameCandidates
	if len(candidates) == 0 {
		candidates = nicknames
	}
	for round := 1; ; round++ {
		for _, n := range candidates {
			if round > 1 {
				n = fmt.Sprintf("%s %d", n, round)
			}
			if !used[n] {
				return n
			}
		}
	}
}

// submit sends a child a message, reaching its live run when says
// (session.Session.Send), and marks it running. It returns the message's ID.
func (m *Manager) submit(c *child, message string, when session.When) (string, error) {
	m.mu.Lock()
	c.gen++
	c.sending++
	if c.status.Final() {
		c.started = time.Now()
	}
	c.status = Status{State: engine.AgentRunning}
	if c.noteDue != "" {
		message, c.noteDue = message+"\n\n"+c.noteDue, ""
	}
	s := c.s
	m.mu.Unlock()
	m.notify(c)
	if m.beforeSubmit != nil {
		m.beforeSubmit(message)
	}
	in, err := s.Send(message, when)
	m.mu.Lock()
	if err == nil && !c.early[in.ID] {
		c.pending[in.ID] = true
	}
	delete(c.early, in.ID)
	m.mu.Unlock()
	m.settle(c)
	if err != nil {
		return "", fmt.Errorf("failed to send the agent the message: %w", err)
	}

	return in.ID, nil
}

// steerQueued sends a child's queued messages now, as ctrl+enter on an
// empty composer does for the main agent. A child a user interrupt left
// idle with its queue starts work again, so it is marked running.
func (m *Manager) steerQueued(c *child) error {
	m.mu.Lock()
	c.sending++
	s := c.s
	m.mu.Unlock()
	n, err := s.SteerQueued()
	m.settle(c)
	m.mu.Lock()
	resumed := n > 0 && c.status.Final()
	if resumed {
		c.gen++
		c.started = time.Now()
		c.status = Status{State: engine.AgentRunning}
	}
	m.mu.Unlock()
	if err != nil {
		return fmt.Errorf("failed to send the agent its queued messages: %w", err)
	}
	if resumed {
		m.notify(c)
	}

	return nil
}

// settle ends one send. When it was the last and an interrupt came during
// the sends, it stops the run they started: the session was idle when the
// interrupt came, and interrupting an idle session does nothing.
func (m *Manager) settle(c *child) {
	m.mu.Lock()
	c.sending--
	stop := c.sending == 0 && c.stopSent && !c.closed
	if c.sending == 0 {
		c.stopSent = false
	}
	s := c.s
	m.mu.Unlock()
	if stop {
		go func() { _ = s.Interrupt() }() // never wait on a child's loop from the caller's
	}
}

// watch follows a child's events until its session closes.
func (m *Manager) watch(c *child) {
	for e := range c.s.Events() {
		m.mu.Lock()
		changed, check := m.observe(c, e)
		c.record(e)
		m.mu.Unlock()
		if check != nil {
			go m.checkStop(c, *check)
		}
		if changed {
			m.notify(c)
		}
		m.forward(c, e)
	}
	m.mu.Lock()
	c.closed = true
	c.status = Status{State: engine.AgentShutdown}
	c.endViews()
	c.log = nil // a closed child is not viewed; its runs stay on disk
	m.mu.Unlock()
	m.notify(c)
}

// observe folds one event into the child and reports whether its status
// changed, or the stop check to run before it does. It holds m.mu.
func (m *Manager) observe(c *child, e core.Event) (bool, *stopCheck) {
	switch e := e.(type) {
	case session.InputQueued:
		if c.pending[e.Input.ID] {
			delete(c.pending, e.Input.ID)
		} else {
			c.early[e.Input.ID] = true // or the session's own, such as a Stop hook's
		}
	case session.InputFailed:
		c.failed = e.Reason
	case core.RunnerError:
		c.cause = e.Message
	case core.RunFinished:
		r := e.Result
		c.last, c.failed = &r, ""
		if r.Status == core.StatusOK && strings.TrimSpace(r.Answer) != "" {
			c.answers = append(c.answers, r.Answer)
		}
	case session.Notice:
		if e.Level == session.LevelError {
			c.failed = e.Message
		}
	case session.Idle:
		if c.sending > 0 || len(c.pending) > 0 || c.closed {
			return false, nil
		}
		if c.review { // its review ends it (endReviewer), not a run's end
			c.last, c.failed, c.cause, c.answers = nil, "", "", nil
			clear(c.early)

			return false, nil
		}
		status := c.final()
		c.last, c.failed, c.cause, c.answers = nil, "", "", nil
		clear(c.early)
		if status.State == engine.AgentCompleted && !c.review && m.tmpl.Hooks.Has(hooks.SubagentStop, "") {
			return false, &stopCheck{gen: c.gen, status: status}
		}
		c.status, c.stopStreak = status, 0

		return true, nil
	}

	return false, nil
}

// final is the status an idle child reports. It holds m.mu.
func (c *child) final() Status {
	switch {
	case c.last != nil && c.last.Status == core.StatusOK:
		return Status{State: engine.AgentCompleted, Message: strings.Join(c.answers, "\n\n")}
	case c.last != nil && c.last.Status == core.StatusInterrupted:
		return Status{State: engine.AgentInterrupted}
	case c.last != nil && c.cause != "":
		return Status{State: engine.AgentErrored, Message: readable(c.cause)}
	case c.last != nil:
		msg := strings.TrimSpace(fmt.Sprintf("the run ended with status %s. %s", c.last.Status, c.last.Answer))

		return Status{State: engine.AgentErrored, Message: msg}
	case c.failed != "":
		return Status{State: engine.AgentErrored, Message: readable(c.failed)}
	}

	return Status{State: engine.AgentCompleted}
}

// notify wakes the waiters and tells the parent. Updates reach the parent
// in the order their states were taken, through its outbox, so a busy
// parent never holds up the child.
func (m *Manager) notify(c *child) {
	m.mu.Lock()
	defer m.mu.Unlock()
	close(m.changed)
	m.changed = make(chan struct{})
	current := m.children[c.id] == c // not a closed child a resume replaced
	parent := m.parents[c.parent]
	update := engine.AgentUpdated{
		At: time.Now(), ID: c.id, Nickname: c.nickname, Role: c.role, State: c.status.State, Started: c.started,
		CallID: c.callID, Task: c.task, Model: c.model, Effort: c.effort, Forked: c.forked,
	}
	if c.status.State == engine.AgentErrored {
		update.Message = c.status.Message
	}
	note := m.completionNote(c, current)
	// The notification goes before the update: once the parent's session
	// shows the child ended, a message sent after that carries the note.
	if note != "" && parent.Inject != nil {
		gen := c.gen
		m.outboxOf(c.parent).push(func() {
			withdraw, live := parent.Inject(note)
			m.hold(c, gen, withdraw, live)
		})
	}
	if parent.Emit != nil && current {
		m.outboxOf(c.parent).push(func() { parent.Emit(update) })
	}
}

// outboxOf is a parent's outbox. It holds m.mu.
func (m *Manager) outboxOf(parentID string) *outbox {
	o, ok := m.outboxes[parentID]
	if !ok {
		o = &outbox{}
		m.outboxes[parentID] = o
	}

	return o
}

// completionNote is Codex's <subagent_notification> for a child that just
// reached a final status, once per message it was sent; "" otherwise. A
// child the parent closed itself, or one a pending wait_agent returns, is
// not reported, since the parent learns it anyway.
// It holds m.mu.
func (m *Manager) completionNote(c *child, current bool) string {
	if !current || c.review || !c.status.Final() || c.status.State == engine.AgentShutdown || c.notified == c.gen {
		return ""
	}
	c.notified = c.gen
	if c.waiters > 0 {
		return "" // a pending wait_agent returns this status
	}
	note, err := engine.SubagentNotification(c.id, c.status)
	if err != nil {
		return ""
	}

	return note
}

// hold keeps the withdraw of gen's notification, or uses it if a wait
// returned that status; a notification that went into the parent's live
// run is not held.
func (m *Manager) hold(c *child, gen int, withdraw func(), live bool) {
	m.mu.Lock()
	returned := c.waited >= gen
	switch {
	case live:
		c.told, withdraw = gen, nil
	case !returned:
		c.held = withdraw
	}
	m.mu.Unlock()
	if returned && withdraw != nil {
		withdraw()
	}
}

// unhold takes the held notification's withdraw, as a wait returns the status. It holds m.mu.
func (c *child) unhold() func() {
	w := c.held
	c.waited, c.held = c.gen, nil

	return w
}

// forward passes a child's tool events to the parent, for its detailed
// view, through the parent's outbox.
func (m *Manager) forward(c *child, e core.Event) {
	switch e.(type) {
	case core.ToolCalled, core.ToolStarted, core.ToolFinished:
	default:
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if emit := m.parents[c.parent].Emit; emit != nil {
		activity := engine.AgentActivity{At: e.OccurredAt(), ID: c.id, Event: e}
		m.outboxOf(c.parent).push(func() { emit(activity) })
	}
}
