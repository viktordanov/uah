package state

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/cmdparse"
	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/engine/codexauth"
	"github.com/viktordanov/uah/internal/patch"
	"github.com/viktordanov/uah/internal/session"
)

// ProgramStatus is one record of the session's status, which the terminal
// layer sends as OSC 7501 (term.Status, docs/design/program-status.md): ID
// "" is the session's own, "agent/<nickname>" a subagent's.
type ProgramStatus struct {
	ID, State, Kind, Title, Msg string
	// Agent is a subagent's ID, which tells two nicknames apart that the
	// terminal's IDs would make one.
	Agent string
}

// The states and kinds of a ProgramStatus, as the protocol names them.
const (
	StatusIdle    = "idle"
	StatusWorking = "working"
	StatusBlocked = "blocked"
	StatusDone    = "done"
	StatusError   = "error"

	BlockedPermission = "permission"
	BlockedQuestion   = "question"
	BlockedAuth       = "auth"
)

// runEnd is how the session's last run ended, kept until the user takes it
// in: the state it reports (done, error, or blocked on a login) and what
// it says.
type runEnd struct {
	State, Kind, Msg string
}

// workingText is what a run or a subagent does when nothing more precise
// is known.
const workingText = "Working"

// maxDoneMsg is how many characters of the agent's answer a done record
// says.
const maxDoneMsg = 300

// ProgramStatus is the session's status records: its own first, then each
// subagent's that runs, or that ended since the user last typed.
func (s State) ProgramStatus() []ProgramStatus {
	agents := s.agentStatus()

	return append([]ProgramStatus{s.ownStatus(agents)}, agents...)
}

// ownStatus is the session's own record, the most pressing first: an
// approval or a question waiting for the user, then work, its own or a
// subagent's (agents), then how the last run ended, until the user types.
// A run that failed says so while subagents work; one that finished is not
// done until they are, since their ends start the agent's next run.
func (s State) ownStatus(agents []ProgramStatus) ProgramStatus {
	a, asked := s.PendingApproval()
	q, questioned := s.PendingQuestions()
	switch {
	case s.Quitting:
		return ProgramStatus{State: StatusIdle}
	case asked && !a.Answered:
		return ProgramStatus{State: StatusBlocked, Kind: BlockedPermission, Msg: "Approve " + s.shownCommand(a.Command)}
	case questioned && !q.Sent:
		msg := "Answer the agent's question"
		if q.Current < len(q.Questions) {
			msg = oneLine(q.Questions[q.Current].Question)
		}

		return ProgramStatus{State: StatusBlocked, Kind: BlockedQuestion, Msg: msg}
	case s.ReviewRunning():
		return ProgramStatus{State: StatusWorking, Msg: "Reviewing"}
	case s.Busy || s.Live != nil || s.ShellRunning():
		return ProgramStatus{State: StatusWorking, Msg: s.activity()}
	case s.ended != nil && s.ended.State != StatusDone:
		return ProgramStatus{State: s.ended.State, Kind: s.ended.Kind, Msg: s.ended.Msg}
	case s.waitingFor(agents) != "":
		return ProgramStatus{State: StatusWorking, Msg: s.waitingFor(agents)}
	case s.ended != nil:
		return ProgramStatus{State: s.ended.State, Kind: s.ended.Kind, Msg: s.ended.Msg}
	}

	return ProgramStatus{State: StatusIdle}
}

// waitingFor says which subagents run, "Waiting for Ada" or "Waiting for
// 2 subagents", or "" when none does. A resumed one that waits for its
// first message (pending_init) has no work to wait for.
func (s State) waitingFor(agents []ProgramStatus) string {
	running := map[string]bool{}
	for _, it := range s.Agents() {
		if it.Agent != nil && it.Detail == engine.AgentRunning {
			running[it.Agent.ID] = true
		}
	}
	var names []string
	for _, a := range agents {
		if a.State == StatusWorking && running[a.Agent] {
			names = append(names, strings.TrimPrefix(a.ID, "agent/"))
		}
	}
	switch len(names) {
	case 0:
		return ""
	case 1:
		return "Waiting for " + names[0]
	}

	return fmt.Sprintf("Waiting for %d subagents", len(names))
}

// activity is what the live run does, as the wait line says it but with
// nothing that counts (bytes, seconds, a countdown), so a report goes out
// when the run moves on, not on every frame.
func (s State) activity() string {
	l := s.Live
	switch {
	case l == nil:
		return workingText
	case !l.Stopping.IsZero():
		return "Stopping"
	case l.Aside != nil:
		return l.Aside.What
	case l.Retry != nil:
		return "Reconnecting"
	case !l.Turn.IsZero() || s.Writing():
		p := l.Progress
		switch {
		case p.Phase == engine.PhaseSending || p.Phase == engine.PhaseWaiting || p.Phase == engine.PhaseConnecting:
			return "Waiting for the model"
		case p.Tool == patch.ToolName:
			return joinDetail("Writing a patch", cmdparse.Relative(p.Target, s.pathEnv()))
		case p.Tool != "":
			return "Preparing " + p.Tool
		case s.Writing():
			return "Writing"
		}

		return "Thinking"
	}

	return s.toolWait().What
}

// agentStatus is a record for each subagent that runs, or that completed
// or failed since the user last typed; one stopped or shut down has none,
// and neither has one a loaded session saved as running, until it updates.
// A nickname comes back once its child closed: only the child whose work
// started last counts, a resumed one too.
func (s State) agentStatus() []ProgramStatus {
	agents := s.Agents()
	latest := make(map[string]int, len(agents))
	for i, it := range agents {
		if it.Agent == nil {
			continue
		}
		if j, ok := latest[nickname(it)]; !ok || !it.Agent.Started.Before(agents[j].Agent.Started) {
			latest[nickname(it)] = i
		}
	}
	var out []ProgramStatus
	for i, it := range agents {
		a := it.Agent
		if a == nil || latest[nickname(it)] != i {
			continue
		}
		r := ProgramStatus{ID: "agent/" + nickname(it), Agent: a.ID, Title: agentTitle(it)}
		switch {
		case s.staleRun(*a):
			continue
		case working(it):
			r.State, r.Msg = StatusWorking, agentActivity(it)
		case s.seenEnd(*a):
			continue
		case it.Detail == engine.AgentCompleted:
			r.State, r.Msg = StatusDone, "Finished"
		case it.Detail == engine.AgentErrored:
			r.State, r.Msg = StatusError, a.Message
		default:
			continue
		}
		out = append(out, r)
	}

	return out
}

// staleRun reports whether the subagent is as a loaded session saved it,
// running: loading does not restart it, so it runs no more.
func (s State) staleRun(a engine.AgentUpdated) bool {
	at, ok := s.stale[a.ID]

	return ok && at.Equal(a.At)
}

// seenEnd reports whether the user typed since the subagent's work ended:
// seen keeps when that work started, which a shutdown after it keeps and
// new work replaces.
func (s State) seenEnd(a engine.AgentUpdated) bool {
	started, ok := s.seen[a.ID]

	return ok && started.Equal(a.Started)
}

// nickname is a subagent's nickname now: a resumed child may get another
// than the one its line was added with, when a newer child took it.
func nickname(it Item) string { return cmp.Or(it.Agent.Nickname, it.Name) }

func agentTitle(it Item) string {
	role := cmp.Or(it.Agent.Role, it.Label)
	if role == "" {
		return nickname(it)
	}

	return nickname(it) + " (" + role + ")"
}

// agentActivity is the tool call a subagent runs, else "Working".
func agentActivity(it Item) string {
	for _, t := range slices.Backward(it.Sub) {
		if t.Tool == ToolRunning || t.Tool == ToolCalled {
			return oneLine(t.Name + " " + t.Label)
		}
	}

	return workingText
}

// endRun keeps how a run ended for the session's record: done with the
// start of the answer, an error with the run's first error, or blocked on
// a login that expired. An interrupted run leaves the session idle.
func (s *State) endRun(r core.Result) {
	switch r.Status {
	case core.StatusOK:
		s.ended = &runEnd{State: StatusDone, Msg: cutRunes(oneLine(r.Answer), maxDoneMsg)}
	case core.StatusInterrupted:
		s.ended = nil
	default:
		s.ended = failed(cmp.Or(s.runErr, "the run ended: "+string(r.Status)))
	}
	s.runErr = ""
	s.unread, s.pending = s.pending, nil // what the run did not read fails after its end
}

// notePending keeps the messages sent to the runner until it echoes them.
func (s *State) notePending(ids []string) {
	if s.pending == nil {
		s.pending = map[string]bool{}
	}
	for _, id := range ids {
		s.pending[id] = true
		delete(s.unread, id) // sent again, for a run of its own
	}
}

// unreadOnly reports whether every failed message is one the last run was
// sent and did not read: its end says how the run went. A message sent
// after it, such as a queued one whose own run failed to start, or never
// sent, as one a hook blocked, fails as itself.
func (s *State) unreadOnly(ids []string) bool {
	all := len(ids) > 0
	for _, id := range ids {
		all = all && s.unread[id]
		delete(s.unread, id)
		delete(s.pending, id)
	}

	return all
}

// endReview keeps how a /review ended for the session's record, as a run's
// end, when no run of the main agent's says how the session is: done with
// the findings' count and the verdict, or an error. An interrupted one
// leaves the session idle.
func (s *State) endReview(e session.ReviewFinished) {
	if s.Busy || s.Live != nil {
		return
	}
	switch {
	case e.Interrupted:
		s.ended = nil
	case e.Err != "":
		s.ended = failed("review failed: " + e.Err)
	default:
		msg := "Review: " + e.Output.Counts()
		if verdict, _ := e.Output.Verdict(); verdict != "" {
			msg += " · " + strings.TrimLeft(verdict, "✓✗ ")
		}
		s.ended = &runEnd{State: StatusDone, Msg: msg}
	}
}

// failed is a run's end on an error: blocked on a login when the error is
// an expired one, which waits for the user, else an error.
func failed(msg string) *runEnd {
	if strings.Contains(msg, codexauth.ErrLoginExpired.Error()) {
		return &runEnd{State: StatusBlocked, Kind: BlockedAuth, Msg: msg}
	}

	return &runEnd{State: StatusError, Msg: msg}
}

// noteRunError keeps a run's first error, from the runner or the model,
// for the record endRun keeps: a notice of the TUI's own, such as an
// unknown /agents name, is not the run's.
func (s *State) noteRunError(text string) {
	if s.runErr == "" && (s.Busy || s.Live != nil) {
		s.runErr = text
	}
}

// onFirst takes what comes before every other handler: the user taking
// in how the last run ended, then the pointer.
func (s *State) onFirst(ev any) ([]Effect, bool) {
	if s.takeIn(ev) {
		return nil, true
	}

	return s.onPointer(ev)
}

// TakeIn is the shell's word that the composer changed, by a key, a
// paste, a recalled prompt, or the editor: how the last run ended is seen.
type TakeIn struct{}

// takeIn marks how the last run ended as seen when the user types or
// sends, and reports whether ev was only that.
func (s *State) takeIn(ev any) bool {
	switch ev.(type) {
	case TakeIn:
		s.markSeen()

		return true
	case DraftChanged, Submit, Steer:
		s.markSeen()
	}

	return false
}

// settleLoaded takes a loaded session's subagents in: their ends are not
// news, and the ones saved as running are stale until they update.
func (s *State) settleLoaded() {
	s.markSeen()
	for _, it := range s.Agents() {
		if it.Agent != nil && working(it) {
			if s.stale == nil {
				s.stale = map[string]time.Time{}
			}
			s.stale[it.Agent.ID] = it.Agent.At
		}
	}
}

// markSeen is the user typing or sending: how the last run ended, and how
// the subagents that are not running ended, are taken in.
func (s *State) markSeen() {
	s.ended = nil
	for _, it := range s.Agents() {
		if it.Agent != nil && !working(it) {
			if s.seen == nil {
				s.seen = map[string]time.Time{}
			}
			s.seen[it.Agent.ID] = it.Agent.Started
		}
	}
}

func cutRunes(text string, n int) string {
	r := []rune(text)
	if len(r) <= n {
		return text
	}

	return string(r[:n-1]) + "…"
}
