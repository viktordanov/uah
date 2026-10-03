package session

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/goal"
)

// Codex's /goal (docs/design/goal.md): the session keeps a persisted
// objective and, whenever it goes idle with the goal active, starts a run
// of its own with Codex's continuation message, until the model marks the
// goal complete (update_goal), the user pauses or clears it, an interrupt
// pauses it, or a guard stops it: the token budget, the continuation cap,
// a failed run, or automatic turns that make no progress.

// goalGuardTurns is how many goal turns in a row without progress stop
// the goal, as Codex stops it after three empty or failing turns.
const goalGuardTurns = 3

// ErrGoalsOff means the session has no goals: they are turned off, or it is
// a subagent's session, which never inherits its parent's goal.
var ErrGoalsOff = errors.New("goals are not available in this session")

// GoalChange says why a GoalUpdated came.
type GoalChange string

const (
	// GoalSet: a new goal, from /goal or the model's create_goal.
	GoalSet GoalChange = "set"
	// GoalEdited: the user changed the objective.
	GoalEdited GoalChange = "edited"
	// GoalStatus: the status changed: by the user, the model, or a guard.
	GoalStatus GoalChange = "status"
	// GoalUsage: tokens, time, or continuations were counted.
	GoalUsage GoalChange = "usage"
	// GoalRestored: the session opened with a goal kept in its sidecar.
	GoalRestored GoalChange = "restored"
)

// GoalUpdated reports the session's goal after a change.
type GoalUpdated struct {
	At     time.Time
	Goal   goal.Goal
	Change GoalChange
	// By is who changed the status: "user", "model", or "uah" (a guard).
	By string `json:",omitempty"`
}

// GoalCleared reports that the session has no goal any more.
type GoalCleared struct{ At time.Time }

// GoalContinued reports a run uah started on its own for the goal; its
// message follows as the run's UserMessage.
type GoalContinued struct {
	At   time.Time
	Goal goal.Goal
}

func (e GoalUpdated) OccurredAt() time.Time   { return e.At }
func (e GoalCleared) OccurredAt() time.Time   { return e.At }
func (e GoalContinued) OccurredAt() time.Time { return e.At }

// goalState is the session's goal; the loop owns it.
type goalState struct {
	settings goal.Settings
	// enabled: a root session with goals on.
	enabled bool
	g       *goal.Goal
	// run accounts the live run while it works on the goal; nil otherwise.
	run *goalRun
	// steer goes to the live run once no response or tool call is under
	// way: the budget limit, or an edited objective. It is dropped when
	// the run ends first.
	steer *core.UserInput
	// idleTurns are automatic goal turns in a row without a tool call;
	// failingTurns, goal turns in a row whose commands failed and no tool
	// call succeeded.
	idleTurns, failingTurns int
}

// goalRun is what the session notes about a run that works on the goal.
type goalRun struct {
	id        string
	automatic bool
	since     time.Time
	tools     int
	succeeded bool
	failed    bool
}

// Goal returns the session's goal with the live run's time counted, and
// false when it has none.
func (s *Session) Goal() (goal.Goal, bool) {
	g, err := call[*goal.Goal](s, cmdGoal{})
	if err != nil || g == nil {
		return goal.Goal{}, false
	}

	return *g, true
}

// SetGoal sets a new goal and starts working on it when the session is
// idle, as Codex's /goal <objective>. An unfinished goal must be cleared
// first.
func (s *Session) SetGoal(objective string) (goal.Goal, error) {
	return goalCall(s, cmdGoal{op: goalOpSet, text: objective})
}

// EditGoal changes the objective and keeps the usage, as Codex's /goal
// edit; a finished goal becomes active again.
func (s *Session) EditGoal(objective string) (goal.Goal, error) {
	return goalCall(s, cmdGoal{op: goalOpEdit, text: objective})
}

// PauseGoal pauses the goal; ResumeGoal makes it active again and works
// on it when the session is idle.
func (s *Session) PauseGoal() (goal.Goal, error) {
	return goalCall(s, cmdGoal{op: goalOpStatus, status: goal.StatusPaused})
}

// ResumeGoal makes a paused, stalled, or budget-limited goal active again,
// with a fresh allowance of automatic continuations.
func (s *Session) ResumeGoal() (goal.Goal, error) {
	return goalCall(s, cmdGoal{op: goalOpStatus, status: goal.StatusActive})
}

// ClearGoal removes the goal and reports whether there was one.
func (s *Session) ClearGoal() (bool, error) {
	g, err := call[*goal.Goal](s, cmdGoal{op: goalOpClear})

	return g != nil, err
}

func goalCall(s *Session, c cmdGoal) (goal.Goal, error) {
	g, err := call[*goal.Goal](s, c)
	if err != nil {
		return goal.Goal{}, err
	}

	return *g, nil
}

type goalOp int

const (
	goalOpGet goalOp = iota
	goalOpSet
	goalOpEdit
	goalOpStatus
	goalOpClear
)

// cmdGoal is a goal command from the user; cmdGoalTool a goal tool call
// from the model.
type (
	cmdGoal struct {
		op     goalOp
		text   string
		status goal.Status
	}
	cmdGoalTool struct {
		name, arguments string
	}
)

// openGoal restores the goal the sidecar keeps.
func (s *Session) openGoal(settings goal.Settings, root bool) {
	s.goal.settings, s.goal.enabled = settings, root && !settings.Disabled
	if !s.goal.enabled || s.sessionsDir == "" {
		return
	}
	sc, _, err := ReadSidecar(s.sessionsDir, s.id)
	if err != nil || sc.Goal == nil {
		return
	}
	g := *sc.Goal
	s.goal.g = &g
	s.out <- GoalUpdated{At: time.Now(), Goal: g, Change: GoalRestored}
}

// onGoal runs a user's goal command.
func (s *Session) onGoal(c cmdGoal) (*goal.Goal, error) {
	if c.op == goalOpGet {
		return s.liveGoal(), nil
	}
	if !s.goal.enabled {
		return nil, ErrGoalsOff
	}
	switch c.op {
	case goalOpSet:
		return s.setGoal(c.text)
	case goalOpEdit:
		return s.editGoal(c.text)
	case goalOpStatus:
		return s.setGoalStatus(c.status)
	case goalOpClear:
		return s.clearGoal(true), nil
	}

	return nil, fmt.Errorf("unknown goal command %d", c.op)
}

// liveGoal is a copy of the goal with the live run's time counted.
func (s *Session) liveGoal() *goal.Goal {
	if s.goal.g == nil {
		return nil
	}
	g := *s.goal.g
	if r := s.goal.run; r != nil && r.id == g.ID && g.Status == goal.StatusActive {
		g.TimeUsedSeconds += int64(time.Since(r.since).Seconds())
	}

	return &g
}

func (s *Session) setGoal(objective string) (*goal.Goal, error) {
	objective, err := goal.CheckObjective(objective)
	if err != nil {
		return nil, err
	}
	if g := s.goal.g; g != nil && !g.Status.Finished() {
		return nil, fmt.Errorf("a goal is already %s; change it with /goal edit, or clear it with /goal clear first", g.Status.Label())
	}
	g := s.newGoal(objective, s.goal.settings.MaxTokenBudget)
	s.hold(goal.UserSet(objective, ""))
	s.changeGoal(g, GoalSet, "user")
	s.continueGoal()

	return s.liveGoal(), nil
}

func (s *Session) newGoal(objective string, budget int64) *goal.Goal {
	now := time.Now().UTC()
	s.goal.idleTurns, s.goal.failingTurns = 0, 0
	g := &goal.Goal{
		ID: uuid.NewString(), Objective: objective, Status: goal.StatusActive, TokenBudget: budget,
		MaxContinuations: s.goal.settings.MaxContinuations, CreatedAt: now, UpdatedAt: now,
	}
	s.goal.g = g
	if s.run != nil && (s.state == StateRunning || s.state == StateStarting) {
		s.goal.run = &goalRun{id: g.ID, since: time.Now()} // the live run works on it from now
	}

	return g
}

func (s *Session) editGoal(objective string) (*goal.Goal, error) {
	objective, err := goal.CheckObjective(objective)
	if err != nil {
		return nil, err
	}
	g := s.goal.g
	if g == nil {
		return nil, errors.New("no goal to edit; set one with /goal <objective>")
	}
	if objective == g.Objective {
		return s.liveGoal(), nil
	}
	g.Objective, g.Reason = objective, ""
	if g.Status.Finished() {
		g.Status = goal.StatusActive // Codex's edited_goal_status
		g.Continuations = 0
		s.goal.idleTurns, s.goal.failingTurns = 0, 0
	}
	s.hold(goal.UserSet(objective, ""))
	if g.Status == goal.StatusActive {
		s.steerGoal(goal.ObjectiveUpdated(*g))
	}
	s.changeGoal(g, GoalEdited, "user")
	s.continueGoal()

	return s.liveGoal(), nil
}

func (s *Session) setGoalStatus(status goal.Status) (*goal.Goal, error) {
	g := s.goal.g
	if g == nil {
		return nil, errors.New("no goal is set; set one with /goal <objective>")
	}
	switch {
	case status == goal.StatusActive && g.Status == goal.StatusComplete:
		return nil, errors.New("the goal is complete; set a new one with /goal <objective>, or change it with /goal edit")
	case status == goal.StatusActive && g.OverBudget():
		return nil, errors.New("the goal used its token budget; set a new one with /goal <objective>")
	case status == g.Status:
		return s.liveGoal(), nil
	}
	if status == goal.StatusActive {
		// A resumed goal starts a fresh blocked audit and a fresh allowance.
		g.Continuations, g.Reason = 0, ""
		s.goal.idleTurns, s.goal.failingTurns = 0, 0
	}
	s.hold(goal.UserSet("", status))
	s.setStatus(status, "", "user")
	s.continueGoal()

	return s.liveGoal(), nil
}

// clearGoal removes the goal; user records it for the model.
func (s *Session) clearGoal(user bool) *goal.Goal {
	g := s.goal.g
	if g == nil {
		return nil
	}
	s.goal.g, s.goal.run, s.goal.steer = nil, nil, nil
	if user {
		s.hold(goal.UserCleared())
	}
	s.saveGoal()
	s.emit(GoalCleared{At: time.Now()})

	return g
}

// hold gives the agent a goal message with the next run, as Inject does.
func (s *Session) hold(text string) {
	s.held = append(s.held, core.UserInput{ID: uuid.NewString(), Text: text})
}

// changeGoal saves the goal and reports the change.
func (s *Session) changeGoal(g *goal.Goal, change GoalChange, by string) {
	g.UpdatedAt = time.Now().UTC()
	s.saveGoal()
	s.emit(GoalUpdated{At: time.Now(), Goal: *s.liveGoal(), Change: change, By: by})
}

// setStatus changes the goal's status; reason says why a guard did.
func (s *Session) setStatus(status goal.Status, reason, by string) {
	g := s.goal.g
	g.Status, g.Reason = status, reason
	if status != goal.StatusActive {
		s.accountGoalTime()
		s.goal.run = nil
	}
	s.changeGoal(g, GoalStatus, by)
}

func (s *Session) saveGoal() {
	if s.sessionsDir == "" {
		return
	}
	var saved *goal.Goal
	if s.goal.g != nil {
		g := *s.goal.g
		saved = &g
	}
	s.warnIf(updateSidecar(s.sessionsDir, s.id, func(sc *Sidecar) bool {
		sc.Goal = saved

		return true
	}))
}

// continueGoal starts a run for the active goal when the session is idle
// with nothing waiting, and reports whether it did. A goal out of
// continuations stops as budget-limited instead.
func (s *Session) continueGoal() bool {
	g := s.goal.g
	if !s.goal.enabled || g == nil || g.Status != goal.StatusActive || s.state != StateIdle ||
		s.closeReply != nil || len(s.queue) > 0 || len(s.hooks.checking) > 0 {
		return false
	}
	if g.OutOfContinuations() {
		s.setStatus(goal.StatusBudgetLimited, fmt.Sprintf("used its %d automatic continuations", g.MaxContinuations), "uah")
		s.emit(Notice{At: time.Now(), Level: LevelWarning, Message: fmt.Sprintf("The goal used its %d automatic continuations and stopped; /goal resume gives it more", g.MaxContinuations)})

		return false
	}
	g.Continuations++
	s.changeGoal(g, GoalUsage, "")
	s.emit(GoalContinued{At: time.Now(), Goal: *g})
	s.startRun([]core.UserInput{{ID: uuid.NewString(), Text: goal.Continuation(*g)}})
	s.goal.run = &goalRun{id: g.ID, automatic: true, since: time.Now()}

	return true
}

// goIdle reports the session idle, or continues the goal instead.
func (s *Session) goIdle(userStopped bool) {
	if !userStopped && s.continueGoal() {
		return
	}
	s.emit(Idle{At: time.Now()})
}

// noteGoalRun marks a run that starts while the goal is active; a
// continuation marks its own.
func (s *Session) noteGoalRun() {
	if g := s.goal.g; g != nil && g.Status == goal.StatusActive {
		s.goal.run = &goalRun{id: g.ID, since: time.Now()}
	} else {
		s.goal.run = nil
	}
	s.goal.steer = nil
}

// onGoalRunEvent accounts a run event for the goal.
func (s *Session) onGoalRunEvent(e core.Event) {
	r, g := s.goal.run, s.goal.g
	if r == nil || g == nil || r.id != g.ID {
		return
	}
	switch v := e.(type) {
	case core.ModelResponded:
		if g.Status != goal.StatusActive {
			return
		}
		g.TokensUsed += goal.TokenDelta(v.Usage.InputTokens, v.Usage.CachedInputTokens, v.Usage.OutputTokens)
		if g.OverBudget() {
			s.setStatus(goal.StatusBudgetLimited, "used its token budget", "uah")
			s.steerGoal(goal.BudgetLimit(*g))

			return
		}
		s.changeGoal(g, GoalUsage, "")
	case core.ToolCalled:
		if !slices.Contains(goal.ToolNames, v.Name) {
			r.tools++
		}
	case core.ToolFinished:
		switch {
		case slices.Contains(goal.ToolNames, v.Name):
		case v.OK:
			r.succeeded = true
		case v.Name == "Bash":
			r.failed = true
		}
	}
}

// steerGoal gives the live run a goal message, as Codex injects one into
// the active turn.
func (s *Session) steerGoal(text string) {
	if s.state != StateRunning && s.state != StateStarting {
		return
	}
	s.goal.steer = &core.UserInput{ID: uuid.NewString(), Text: text, Role: core.RoleDeveloper}
	s.sendGoalSteer()
}

// sendGoalSteer sends the held goal message once the run is live. It goes
// as a developer message, which asks for no response of its own and so
// cancels no request: it rides the run's next model request, with the
// output of the calls under way. It is not marked sent, and a run that
// ends first leaves it in the history for the next request.
func (s *Session) sendGoalSteer() {
	if s.goal.steer == nil || s.state != StateRunning || s.run == nil {
		return
	}
	if err := s.run.Send(*s.goal.steer); err == nil {
		s.goal.steer = nil
	}
}

// endGoalRun accounts the ended run: its time, and the guards that stop a
// goal whose turns fail or make no progress.
func (s *Session) endGoalRun(result core.Result, err error, userStopped bool) {
	r, g := s.goal.run, s.goal.g
	s.goal.steer = nil
	if r == nil || g == nil || r.id != g.ID {
		s.goal.run = nil

		return
	}
	s.accountGoalTime()
	s.goal.run = nil
	if g.Status != goal.StatusActive || userStopped {
		s.saveGoal()

		return
	}
	failed := err != nil || result.Status == core.StatusFailed || result.Status == core.StatusDiskLimit || result.Status == core.StatusTimeout
	if r.automatic && r.tools == 0 {
		s.goal.idleTurns++
	} else {
		s.goal.idleTurns = 0
	}
	if r.failed && !r.succeeded {
		s.goal.failingTurns++
	} else {
		s.goal.failingTurns = 0
	}
	switch {
	case failed:
		s.setStatus(goal.StatusBlocked, "the run failed", "uah")
	case s.goal.idleTurns >= goalGuardTurns:
		s.setStatus(goal.StatusBlocked, fmt.Sprintf("%d automatic turns in a row made no tool call", s.goal.idleTurns), "uah")
	case s.goal.failingTurns >= goalGuardTurns:
		s.setStatus(goal.StatusBlocked, fmt.Sprintf("commands failed in %d goal turns in a row", s.goal.failingTurns), "uah")
	default:
		s.changeGoal(g, GoalUsage, "")
	}
}

// accountGoalTime adds the live run's time to the goal.
func (s *Session) accountGoalTime() {
	r, g := s.goal.run, s.goal.g
	if r == nil || g == nil || r.id != g.ID {
		return
	}
	now := time.Now()
	g.TimeUsedSeconds += int64(now.Sub(r.since).Seconds())
	r.since = now
}

// pauseGoalForInterrupt pauses an active goal when the user interrupts,
// as Codex's TUI pauses it on esc.
func (s *Session) pauseGoalForInterrupt() {
	if g := s.goal.g; g != nil && g.Status == goal.StatusActive && s.state != StateIdle {
		s.setStatus(goal.StatusPaused, "interrupted", "user")
	}
}

// goalTool answers the model's goal tool call through the loop.
func (s *Session) goalTool(ctx context.Context, name, arguments string) (string, error) {
	reply := make(chan reply, 1)
	select {
	case s.in <- request{cmd: cmdGoalTool{name: name, arguments: arguments}, reply: reply}:
	case <-ctx.Done():
		return "", ctx.Err()
	case <-s.done:
		return "", ErrClosed
	}
	select {
	case r := <-reply:
		text, _ := r.value.(string)

		return text, r.err
	case <-ctx.Done():
		return "", ctx.Err()
	case <-s.done:
		return "", ErrClosed
	}
}

// goalToolFunc is what runs call for the goal tools; nil without goals.
func (s *Session) goalToolFunc() func(context.Context, string, string) (string, error) {
	if !s.goal.enabled {
		return nil
	}

	return s.goalTool
}

// onGoalTool runs a goal tool call as Codex's handlers do.
func (s *Session) onGoalTool(c cmdGoalTool) (string, error) {
	switch c.name {
	case goal.GetToolName:
		return goal.Result(s.liveGoal(), s.id, false), nil
	case goal.CreateToolName:
		args, err := goal.ParseArgs[goal.CreateArgs](c.arguments)
		if err != nil {
			return "", err
		}
		objective, err := goal.CheckObjective(args.Objective)
		if err != nil {
			return "", err
		}
		budget := s.goal.settings.MaxTokenBudget
		if args.TokenBudget != nil {
			if *args.TokenBudget <= 0 {
				return "", errors.New("goal budgets must be positive when provided")
			}
			budget = *args.TokenBudget
		}
		if err := goal.CheckBudget(budget, s.goal.settings.MaxTokenBudget); err != nil {
			return "", err
		}
		if g := s.goal.g; g != nil && !g.Status.Finished() {
			return "", errors.New(goal.ErrUnfinished)
		}
		g := s.newGoal(objective, budget)
		s.changeGoal(g, GoalSet, "model")

		return goal.Result(s.liveGoal(), s.id, false), nil
	case goal.UpdateToolName:
		args, err := goal.ParseArgs[goal.UpdateArgs](c.arguments)
		if err != nil {
			return "", err
		}
		if args.Status != goal.StatusComplete && args.Status != goal.StatusBlocked && args.Status != goal.StatusPaused {
			return "", errors.New(goal.ErrUpdateStatus)
		}
		g := s.goal.g
		if g == nil {
			return "", errors.New(goal.ErrNoGoal)
		}
		// Only an active goal pauses or blocks; a budget limit takes
		// precedence over both.
		if g.Status != args.Status && (args.Status == goal.StatusComplete || g.Status == goal.StatusActive) {
			s.setStatus(args.Status, "", "model")
		}

		return goal.Result(s.liveGoal(), s.id, args.Status == goal.StatusComplete), nil
	}

	return "", fmt.Errorf("unknown goal tool %q", c.name)
}
