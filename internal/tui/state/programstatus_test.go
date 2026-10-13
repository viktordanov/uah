package state_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/viktordanov/uagent/core"
	uaharness "github.com/viktordanov/uagent/harness"

	"github.com/viktordanov/uah/internal/approval"
	"github.com/viktordanov/uah/internal/codereview"
	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/engine/codexauth"
	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/internal/tui/state"
)

func own(s state.State) state.ProgramStatus { return s.ProgramStatus()[0] }

func ran(status core.Status, answer string) []any {
	return []any{
		core.RunStarted{At: t0, RunID: "r1"},
		core.RunFinished{At: t0, Result: core.Result{Request: core.Request{RunID: "r1"}, Status: status, Answer: answer}},
		session.Idle{At: t0},
	}
}

func TestProgramStatus_FollowsTheSession(t *testing.T) {
	s := opened()
	assert.Equal(t, state.ProgramStatus{State: state.StatusIdle}, own(s))
	assert.Len(t, s.ProgramStatus(), 1, "no subagents, one record")

	s, _ = apply(s, session.InputQueued{At: t0, Input: core.UserInput{ID: "i1", Text: "fix it"}}, core.RunStarted{At: t0, RunID: "r1"}, core.TurnStarted{At: t0, Turn: 1})
	assert.Equal(t, state.ProgramStatus{State: state.StatusWorking, Msg: "Thinking"}, own(s))

	s, _ = apply(s, session.ApprovalRequested{At: t0, ID: "a1", Command: "git push origin main"})
	assert.Equal(t, state.ProgramStatus{State: state.StatusBlocked, Kind: state.BlockedPermission, Msg: "Approve git push origin main"}, own(s))
	s, _ = apply(s, session.ApprovalResolved{At: t0, ID: "a1", Decision: approval.Approve})
	assert.Equal(t, state.StatusWorking, own(s).State, "working again once answered")

	s, _ = apply(s, session.QuestionsAsked{At: t0, ID: "q1", Questions: []engine.Question{{ID: "x", Question: "Which\nbranch?"}}})
	assert.Equal(t, state.ProgramStatus{State: state.StatusBlocked, Kind: state.BlockedQuestion, Msg: "Which branch?"}, own(s))
	s, _ = apply(s, session.QuestionsAnswered{At: t0, ID: "q1"})

	s, _ = apply(s, core.RunFinished{At: t0, Result: core.Result{Request: core.Request{RunID: "r1"}, Status: core.StatusOK, Answer: "Fixed the\n\nbug in main.go."}}, session.Idle{At: t0})
	assert.Equal(t, state.ProgramStatus{State: state.StatusDone, Msg: "Fixed the bug in main.go."}, own(s))

	s, _ = apply(s, state.DraftChanged{Draft: "t"})
	assert.Equal(t, state.ProgramStatus{State: state.StatusIdle}, own(s), "typing takes the result in")
}

func TestProgramStatus_HowARunEnds(t *testing.T) {
	s, _ := apply(opened(), ran(core.StatusInterrupted, "")...)
	assert.Equal(t, state.StatusIdle, own(s).State, "an interrupted run leaves the session idle")

	s, _ = apply(opened(), core.RunStarted{At: t0, RunID: "r1"}, core.RunnerError{At: t0, Message: "the provider answered 500"},
		core.RunnerError{At: t0, Message: "a later error"})
	s, _ = apply(s, ran(core.StatusFailed, "")[1:]...)
	assert.Equal(t, state.ProgramStatus{State: state.StatusError, Msg: "the provider answered 500"}, own(s), "the run's first error")

	s, _ = apply(opened(), ran(core.StatusTimeout, "")...)
	assert.Equal(t, state.ProgramStatus{State: state.StatusError, Msg: "the run ended: timeout"}, own(s))

	s, _ = apply(opened(), core.RunStarted{At: t0, RunID: "r1"}, core.RunnerError{At: t0, Message: "model request failed: " + codexauth.ErrLoginExpired.Error()})
	s, _ = apply(s, ran(core.StatusFailed, "")[1:]...)
	assert.Equal(t, state.StatusBlocked, own(s).State, "an expired login waits for the user")
	assert.Equal(t, state.BlockedAuth, own(s).Kind)

	s, _ = apply(s, core.RunStarted{At: t0, RunID: "r2"})
	assert.Equal(t, state.StatusWorking, own(s).State, "a new run replaces how the last ended")

	long := ""
	for range 100 {
		long += "word "
	}
	s, _ = apply(opened(), ran(core.StatusOK, long)...)
	assert.Equal(t, 300, len([]rune(own(s).Msg)), "a done record says the start of the answer")
}

func TestProgramStatus_Quitting(t *testing.T) {
	s, _ := apply(opened(), session.InputQueued{At: t0, Input: core.UserInput{ID: "i1", Text: "hi"}})
	s.Quitting = true
	assert.Equal(t, state.StatusIdle, own(s).State)
}

func TestProgramStatus_Subagents(t *testing.T) {
	s, _ := apply(opened(), session.InputQueued{At: t0, Input: core.UserInput{ID: "i1", Text: "hi"}},
		engine.AgentUpdated{At: t0, ID: "a1", Nickname: "Ada", Role: "explorer", State: engine.AgentRunning},
		engine.AgentUpdated{At: t0, ID: "a2", Nickname: "Bo", State: engine.AgentRunning},
		engine.AgentActivity{ID: "a1", Event: core.ToolCalled{CallID: "c1", Name: "Bash", Label: "go test ./..."}})
	records := s.ProgramStatus()
	assert.Len(t, records, 3)
	assert.Equal(t, state.ProgramStatus{ID: "agent/Ada", Agent: "a1", State: state.StatusWorking, Title: "Ada (explorer)", Msg: "Bash go test ./..."}, records[1])
	assert.Equal(t, state.ProgramStatus{ID: "agent/Bo", Agent: "a2", State: state.StatusWorking, Title: "Bo", Msg: "Working"}, records[2])

	s, _ = apply(s, engine.AgentUpdated{At: t0.Add(1), ID: "a1", Nickname: "Ada", Role: "explorer", State: engine.AgentCompleted},
		engine.AgentUpdated{At: t0.Add(1), ID: "a2", Nickname: "Bo", State: engine.AgentErrored, Message: "the model is not supported"})
	records = s.ProgramStatus()
	assert.Equal(t, state.StatusDone, records[1].State)
	assert.Equal(t, state.ProgramStatus{ID: "agent/Bo", Agent: "a2", State: state.StatusError, Title: "Bo", Msg: "the model is not supported"}, records[2])

	s, _ = apply(s, state.DraftChanged{Draft: "o"})
	assert.Len(t, s.ProgramStatus(), 1, "typing takes the subagents' ends in")

	s, _ = apply(s, engine.AgentUpdated{At: t0.Add(2), ID: "a1", Nickname: "Ada", State: engine.AgentShutdown})
	assert.Len(t, s.ProgramStatus(), 1, "closing a child whose end was seen brings nothing back")

	s, _ = apply(s, engine.AgentUpdated{At: t0.Add(2), Started: t0.Add(2), ID: "a1", Nickname: "Ada", State: engine.AgentRunning},
		engine.AgentUpdated{At: t0.Add(3), Started: t0.Add(2), ID: "a1", Nickname: "Ada", State: engine.AgentCompleted})
	assert.Len(t, s.ProgramStatus(), 2, "a later end shows again")

	s, _ = apply(s, state.DraftChanged{Draft: "ok"}, engine.AgentUpdated{At: t0.Add(4), ID: "a3", Nickname: "Cy", State: engine.AgentRunning})
	assert.Len(t, s.ProgramStatus(), 2)
	s, _ = apply(s, engine.AgentUpdated{At: t0.Add(5), ID: "a3", Nickname: "Cy", State: engine.AgentShutdown})
	assert.Len(t, s.ProgramStatus(), 1, "a subagent shut down while running has no record")
}

// The message says what the run does without what counts, so a stream of
// bytes, a stall, or a retry's countdown sends no report per frame.
func TestProgramStatus_ASteadyMessage(t *testing.T) {
	s, _ := apply(opened(), session.InputQueued{At: t0, Input: core.UserInput{ID: "i1", Text: "edit"}}, core.RunStarted{At: t0, RunID: "r1"}, core.TurnStarted{At: t0, Turn: 1},
		engine.ModelProgress{At: t0, Phase: engine.PhaseStreaming, Tool: "apply_patch", Target: "/workspace/main.go", ToolBytes: 100})
	assert.Equal(t, "Writing a patch · main.go", own(s).Msg)
	s, _ = apply(s, engine.ModelProgress{At: t0.Add(time.Second), Phase: engine.PhaseStreaming, Tool: "apply_patch", Target: "/workspace/main.go", ToolBytes: 9000},
		state.Tick{Now: t0.Add(45 * time.Second)})
	assert.Equal(t, "Writing a patch · main.go", own(s).Msg, "no bytes, no stall time")

	s, _ = apply(s, engine.Reconnecting{At: t0.Add(46 * time.Second), Attempt: 2, MaxAttempts: 10, Delay: 8 * time.Second, Reason: "EOF"}, state.Tick{Now: t0.Add(47 * time.Second)})
	retry := own(s).Msg
	s, _ = apply(s, state.Tick{Now: t0.Add(49 * time.Second)})
	assert.Equal(t, "Reconnecting", retry)
	assert.Equal(t, retry, own(s).Msg, "no countdown")
}

// A message a hook blocks never starts a run, so its failure is the
// session's end: queued (busy), failed, then idle.
func TestProgramStatus_ALoginExpiredBeforeTheRun(t *testing.T) {
	s, _ := apply(opened(), session.InputQueued{At: t0, Input: core.UserInput{ID: "i1", Text: "hi"}},
		session.InputFailed{At: t0, IDs: []string{"i1"}, Reason: codexauth.ErrLoginExpired.Error()}, session.Idle{At: t0})
	assert.Equal(t, state.StatusBlocked, own(s).State)
	assert.Equal(t, state.BlockedAuth, own(s).Kind)
}

func TestProgramStatus_ADeliveryFailure(t *testing.T) {
	s, _ := apply(opened(), session.InputQueued{At: t0, Input: core.UserInput{ID: "i1", Text: "hi"}},
		session.InputFailed{At: t0, IDs: []string{"i1"}, Reason: "blocked by a UserPromptSubmit hook"}, session.Idle{At: t0})
	assert.Equal(t, state.ProgramStatus{State: state.StatusError, Msg: "not delivered: blocked by a UserPromptSubmit hook"}, own(s))
}

// A nickname comes back once its child closed: the later child's record
// shows, not the earlier one's end.
func TestProgramStatus_ANicknameUsedAgain(t *testing.T) {
	s, _ := apply(opened(),
		engine.AgentUpdated{At: t0, ID: "a1", Nickname: "Ada", State: engine.AgentRunning},
		engine.AgentUpdated{At: t0.Add(1), ID: "a1", Nickname: "Ada", State: engine.AgentCompleted},
		engine.AgentUpdated{At: t0.Add(2), ID: "a1", Nickname: "Ada", State: engine.AgentShutdown},
		engine.AgentUpdated{At: t0.Add(3), ID: "a2", Nickname: "Ada", State: engine.AgentRunning})
	records := s.ProgramStatus()
	assert.Len(t, records, 2)
	assert.Equal(t, state.StatusWorking, records[1].State)

	s, _ = apply(s, engine.AgentUpdated{At: t0.Add(4), ID: "a2", Nickname: "Ada", State: engine.AgentInterrupted})
	assert.Len(t, s.ProgramStatus(), 1, "the later child stopped: the earlier one's end does not come back")
}

// A loaded session's runs and subagents are not news: no done record for
// a run that ended before, and none for a saved run left incomplete.
func TestProgramStatus_HistoryIsSeen(t *testing.T) {
	history := []session.LoadedRun{
		{
			Record: uaharness.RunRecord{Complete: true, Result: core.Result{Request: core.Request{RunID: "r1"}, Status: core.StatusOK, StartedAt: t0, Answer: "old"}},
			Events: []core.Event{engine.AgentUpdated{At: t0, ID: "a1", Nickname: "Ada", State: engine.AgentCompleted}},
		},
		{Record: uaharness.RunRecord{Result: core.Result{Request: core.Request{RunID: "r2"}, StartedAt: t0}}},
	}
	s, _ := apply(opened(), state.HistoryLoaded{SessionID: "sess-2", Runs: history}, session.SessionOpened{ID: "sess-2", Resumed: true, Settings: settings()})
	assert.Equal(t, []state.ProgramStatus{{State: state.StatusIdle}}, s.ProgramStatus())

	s, _ = apply(s, session.InputQueued{At: t0, Input: core.UserInput{ID: "i1", Text: "hi"}},
		session.InputFailed{At: t0, IDs: []string{"i1"}, Reason: "blocked by a hook"}, session.Idle{At: t0})
	assert.Equal(t, state.StatusError, own(s).State, "the first message after a resume fails as any other")
}

// A loaded session's subagent saved as running is not restarted: it has
// no record until it updates.
func TestProgramStatus_HistoryLeavesNoPhantomRun(t *testing.T) {
	history := []session.LoadedRun{{
		Record: uaharness.RunRecord{Result: core.Result{Request: core.Request{RunID: "r1"}, StartedAt: t0}},
		Events: []core.Event{engine.AgentUpdated{At: t0, ID: "a1", Nickname: "Ada", State: engine.AgentRunning}},
	}}
	s, _ := apply(opened(), state.HistoryLoaded{SessionID: "sess-2", Runs: history})
	assert.Len(t, s.ProgramStatus(), 1)
	s, _ = apply(s, state.DraftChanged{Draft: "x"})
	assert.Len(t, s.ProgramStatus(), 1, "typing brings it back neither")

	s, _ = apply(s, engine.AgentUpdated{At: t0.Add(1), Started: t0.Add(1), ID: "a1", Nickname: "Ada", State: engine.AgentRunning})
	assert.Len(t, s.ProgramStatus(), 2, "a fresh update: it runs again")
}

// A resumed child is the latest work of its nickname, though it started
// first.
func TestProgramStatus_AResumedChild(t *testing.T) {
	s, _ := apply(opened(),
		engine.AgentUpdated{At: t0, Started: t0, ID: "a1", Nickname: "Ada", State: engine.AgentRunning},
		engine.AgentUpdated{At: t0.Add(1), Started: t0, ID: "a1", Nickname: "Ada", State: engine.AgentShutdown},
		engine.AgentUpdated{At: t0.Add(2), Started: t0.Add(2), ID: "a2", Nickname: "Ada", State: engine.AgentRunning},
		engine.AgentUpdated{At: t0.Add(3), Started: t0.Add(2), ID: "a2", Nickname: "Ada", State: engine.AgentShutdown},
		engine.AgentUpdated{At: t0.Add(4), Started: t0.Add(4), ID: "a1", Nickname: "Ada", State: engine.AgentRunning})
	records := s.ProgramStatus()
	assert.Len(t, records, 2)
	assert.Equal(t, "a1", records[1].Agent)
	assert.Equal(t, state.StatusWorking, records[1].State)
}

// Closing a child that failed keeps why, until the user types.
func TestProgramStatus_AFailedChildClosed(t *testing.T) {
	s, _ := apply(opened(),
		engine.AgentUpdated{At: t0, ID: "a1", Nickname: "Ada", State: engine.AgentErrored, Message: "the model is not supported"},
		engine.AgentUpdated{At: t0.Add(1), ID: "a1", Nickname: "Ada", State: engine.AgentShutdown})
	assert.Equal(t, state.ProgramStatus{ID: "agent/Ada", Agent: "a1", State: state.StatusError, Title: "Ada", Msg: "the model is not supported"}, s.ProgramStatus()[1])
}

// Only the runner's and the model's errors are the run's: the TUI's own
// error notice during a run does not hide an expired login.
func TestProgramStatus_OnlyTheRunsErrors(t *testing.T) {
	s, _ := apply(opened(), session.InputQueued{At: t0, Input: core.UserInput{ID: "i1", Text: "hi"}}, core.RunStarted{At: t0, RunID: "r1"},
		state.Submit{Text: "/agents nobody"},
		core.RunnerError{At: t0, Message: codexauth.ErrLoginExpired.Error()})
	s, _ = apply(s, ran(core.StatusFailed, "")[1:]...)
	assert.Equal(t, state.StatusBlocked, own(s).State)
	assert.Equal(t, state.BlockedAuth, own(s).Kind)
}

// A start that fails after RunStarted has no RunFinished: its input's
// failure is the end. Input a run left unread fails after its end, which
// stays.
func TestProgramStatus_InputFailuresAroundARun(t *testing.T) {
	s, _ := apply(opened(), session.InputQueued{At: t0, Input: core.UserInput{ID: "i1", Text: "hi"}}, core.RunStarted{At: t0, RunID: "r1"},
		session.InputFailed{At: t0, IDs: []string{"i1"}, Reason: "the engine did not start"}, session.Idle{At: t0})
	assert.Equal(t, state.ProgramStatus{State: state.StatusError, Msg: "not delivered: the engine did not start"}, own(s))

	s, _ = apply(opened(), core.RunStarted{At: t0, RunID: "r1"}, core.RunnerError{At: t0, Message: "the provider answered 500"},
		session.InputSent{At: t0, IDs: []string{"i2"}},
		core.RunFinished{At: t0, Result: core.Result{Request: core.Request{RunID: "r1"}, Status: core.StatusFailed}},
		session.InputFailed{At: t0, IDs: []string{"i2"}, Reason: "the run ended"}, session.Idle{At: t0})
	assert.Equal(t, "the provider answered 500", own(s).Msg, "the run's error stays")

	s, _ = apply(opened(), core.RunStarted{At: t0, RunID: "r1"}, session.InputSent{At: t0, IDs: []string{"i2"}})
	s, _ = apply(s, ran(core.StatusInterrupted, "")[1])
	s, _ = apply(s, session.InputFailed{At: t0, IDs: []string{"i2"}, Reason: "the run ended"}, session.Idle{At: t0})
	assert.Equal(t, state.StatusIdle, own(s).State, "an interrupt stays idle")
}

// A message queued during a run starts its own run at the run's end, with
// no Idle between: when that start fails, the failure is the session's
// end, not the earlier run's unread input.
func TestProgramStatus_AFollowUpFailsToStart(t *testing.T) {
	s, _ := apply(opened(), session.InputQueued{At: t0, Input: core.UserInput{ID: "i1", Text: "first"}}, session.InputSent{At: t0, IDs: []string{"i1"}},
		core.RunStarted{At: t0, RunID: "r1"}, session.InputDelivered{At: t0, ID: "i1"},
		session.InputQueued{At: t0, Input: core.UserInput{ID: "i2", Text: "then"}},
		core.RunFinished{At: t0, Result: core.Result{Request: core.Request{RunID: "r1"}, Status: core.StatusOK, Answer: "done"}},
		session.InputSent{At: t0, IDs: []string{"i2"}},
		session.InputFailed{At: t0, IDs: []string{"i2"}, Reason: codexauth.ErrLoginExpired.Error()}, session.Idle{At: t0})
	assert.Equal(t, state.StatusBlocked, own(s).State)
	assert.Equal(t, state.BlockedAuth, own(s).Kind)
}

// A run started for injected messages alone, such as a subagent's end
// waking the session, fails no input when it cannot start: the session's
// notice says so.
func TestProgramStatus_AnInjectedRunFailsToStart(t *testing.T) {
	s, _ := apply(opened(), ran(core.StatusOK, "done")...)
	s, _ = apply(s, session.Notice{At: t0, Level: session.LevelError, Message: codexauth.ErrLoginExpired.Error(), StartFailed: true}, session.Idle{At: t0})
	assert.Equal(t, state.StatusBlocked, own(s).State)
	assert.Equal(t, state.BlockedAuth, own(s).Kind)

	s, _ = apply(opened(), ran(core.StatusOK, "done")...)
	s, _ = apply(s, session.Notice{At: t0, Level: session.LevelError, Message: "could not save the settings"})
	assert.Equal(t, state.StatusDone, own(s).State, "another error notice is not a run's end")
}

// A steer the run left unread is sent again for a run of its own: when that
// start fails, the failure is the session's end.
func TestProgramStatus_AnUnreadSteerSentAgain(t *testing.T) {
	s, _ := apply(opened(), core.RunStarted{At: t0, RunID: "r1"}, session.InputSent{At: t0, IDs: []string{"i2"}},
		core.RunFinished{At: t0, Result: core.Result{Request: core.Request{RunID: "r1"}, Status: core.StatusOK, Answer: "done"}},
		session.InputSent{At: t0, IDs: []string{"i2"}},
		session.InputFailed{At: t0, IDs: []string{"i2"}, Reason: "the engine did not start"}, session.Idle{At: t0})
	assert.Equal(t, state.StatusError, own(s).State)
}

// A /review while the main agent is idle ends as a run does: done with its
// findings and verdict, or an error, until the user types.
func TestProgramStatus_AReview(t *testing.T) {
	s, _ := apply(opened(), session.ReviewStarted{At: t0, ID: "r1", Hint: "current changes"})
	assert.Equal(t, state.ProgramStatus{State: state.StatusWorking, Msg: "Reviewing"}, own(s))
	s, _ = apply(s, session.ReviewFinished{At: t0, ID: "r1", Output: codereview.Output{
		Findings: []codereview.Finding{{Title: "a bug"}}, OverallCorrectness: codereview.VerdictIncorrect,
	}})
	assert.Equal(t, state.StatusDone, own(s).State)
	assert.True(t, strings.HasPrefix(own(s).Msg, "Review: 1 finding"), own(s).Msg)
	assert.True(t, strings.HasSuffix(own(s).Msg, " · patch is incorrect"), own(s).Msg)

	s, _ = apply(opened(), session.ReviewStarted{At: t0, ID: "r1"}, session.ReviewFinished{At: t0, ID: "r1", Err: codexauth.ErrLoginExpired.Error()})
	assert.Equal(t, state.StatusBlocked, own(s).State)
	assert.Equal(t, state.BlockedAuth, own(s).Kind)

	s, _ = apply(opened(), session.ReviewStarted{At: t0, ID: "r1"}, session.ReviewFinished{At: t0, ID: "r1", Interrupted: true})
	assert.Equal(t, state.StatusIdle, own(s).State)
}

// A resumed child may get another nickname than its line's, when a newer
// child took it: its record follows the nickname it has now.
func TestProgramStatus_AResumedChildRenamed(t *testing.T) {
	s, _ := apply(opened(),
		engine.AgentUpdated{At: t0, Started: t0, ID: "a1", Nickname: "Ada", State: engine.AgentCompleted},
		engine.AgentUpdated{At: t0.Add(1), Started: t0.Add(1), ID: "a2", Nickname: "Ada", State: engine.AgentRunning},
		engine.AgentUpdated{At: t0.Add(2), Started: t0.Add(2), ID: "a1", Nickname: "Bea", State: engine.AgentRunning})
	records := s.ProgramStatus()
	assert.Len(t, records, 3, "both children run, each under its own nickname")
	assert.Equal(t, state.ProgramStatus{ID: "agent/Bea", Agent: "a1", State: state.StatusWorking, Title: "Bea", Msg: "Working"}, records[1])
	assert.Equal(t, state.ProgramStatus{ID: "agent/Ada", Agent: "a2", State: state.StatusWorking, Title: "Ada", Msg: "Working"}, records[2])
}
