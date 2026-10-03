package state

import (
	"strings"
	"time"

	"github.com/viktordanov/uah/internal/goal"
	"github.com/viktordanov/uah/internal/session"
)

// /goal, Codex's goal command (docs/design/goal.md): the session keeps the
// goal and continues it; the TUI sets, edits, pauses, resumes, and clears
// it, shows it in the footer, and marks the runs uah started for it.

// GoalOp is what EffGoal asks the session to do.
type GoalOp int

const (
	GoalOpSet GoalOp = iota
	GoalOpEdit
	GoalOpPause
	GoalOpResume
	GoalOpClear
)

// EffGoal changes the session's goal; Text is the objective to set or edit.
type EffGoal struct {
	Op   GoalOp
	Text string
}

func (EffGoal) effect() {}

// GoalView is the session's goal as the TUI last saw it.
type GoalView struct {
	Goal goal.Goal
	// At is when the session reported it, so the footer adds only the
	// live run's time after it.
	At time.Time
}

// /goal's words.
const (
	goalWordStatus = "status"
	goalWordClear  = "clear"
	goalWordPause  = "pause"
	goalWordResume = "resume"
	goalWordEdit   = "edit"
)

var goalWords = map[string]GoalOp{goalWordClear: GoalOpClear, goalWordPause: GoalOpPause, goalWordResume: GoalOpResume}

// cmdGoal runs /goal: alone or with status, the goal's summary; clear,
// pause, resume; edit with an objective, or alone to put the objective in
// the composer; anything else sets a new goal.
func cmdGoal(s *State, args string) []Effect {
	word, rest, _ := strings.Cut(args, " ")
	switch strings.ToLower(word) {
	case "", goalWordStatus:
		if rest == "" {
			s.showGoal()

			return nil
		}
	case goalWordClear, goalWordPause, goalWordResume:
		if rest == "" {
			return []Effect{EffGoal{Op: goalWords[strings.ToLower(word)]}}
		}
	case goalWordEdit:
		if strings.TrimSpace(rest) != "" {
			return []Effect{EffGoal{Op: GoalOpEdit, Text: rest}}
		}
		if s.Goal == nil {
			s.notice(session.LevelWarning, "No goal to edit; set one with /goal <objective>")

			return nil
		}

		return []Effect{EffSetDraft{Text: "/goal edit " + s.Goal.Goal.Objective}}
	}

	return []Effect{EffGoal{Op: GoalOpSet, Text: args}}
}

// showGoal prints the goal's summary, or the usage when there is none.
func (s *State) showGoal() {
	if s.Goal == nil {
		s.notice(session.LevelInfo, goal.Usage+"\nNo goal is currently set.")

		return
	}
	g := s.Goal.Goal
	g.TimeUsedSeconds += s.goalLiveSeconds()
	s.notice(session.LevelInfo, "Goal\n"+strings.Join(goal.Summary(g), "\n"))
}

// GoalIndicator is the footer's goal text, "" without a goal.
func (s State) GoalIndicator() string {
	if s.Goal == nil {
		return ""
	}

	return goal.Indicator(s.Goal.Goal, s.goalLiveSeconds())
}

// goalLiveSeconds is the time the live run has added to an active goal
// since the session last reported it.
func (s State) goalLiveSeconds() int64 {
	if s.Goal == nil || s.Goal.Goal.Status != goal.StatusActive || s.Live == nil || s.Now.IsZero() {
		return 0
	}
	since := s.Goal.At
	if s.Live.Started.After(since) {
		since = s.Live.Started
	}

	return max(int64(s.Now.Sub(since).Seconds()), 0)
}

// onGoalEvent folds the session's goal events in; it reports whether ev
// was one.
func (s *State) onGoalEvent(ev any) bool {
	switch e := ev.(type) {
	case session.GoalUpdated:
		s.Goal = &GoalView{Goal: e.Goal, At: e.At}
		s.goalNotice(e)
	case session.GoalCleared:
		if s.Goal != nil {
			s.notice(session.LevelInfo, "Goal cleared")
		}
		s.Goal = nil
	case session.GoalContinued:
		s.Goal = &GoalView{Goal: e.Goal, At: e.At}
		s.Busy = true
	default:
		return false
	}

	return true
}

// goalNotice says what changed: a new or edited goal, a status the user,
// the model, or a guard set, or the goal a resumed session keeps.
func (s *State) goalNotice(e session.GoalUpdated) {
	g := e.Goal
	switch e.Change {
	case session.GoalSet, session.GoalEdited, session.GoalStatus:
		level := session.LevelInfo
		if e.By == "uah" {
			level = session.LevelWarning
		}
		s.notice(level, goal.Line(g))
	case session.GoalRestored:
		switch g.Status {
		case goal.StatusActive:
			s.notice(session.LevelInfo, goal.Line(g)+" It continues when the next run ends; /goal pause pauses it.")
		case goal.StatusPaused, goal.StatusBlocked:
			s.notice(session.LevelInfo, goal.Line(g)+" /goal resume continues it.")
		case goal.StatusBudgetLimited, goal.StatusComplete:
		}
	case session.GoalUsage:
	}
}

// goalNote stands for one of the goal's messages in the transcript, which
// are uah's rather than the user's: a continuation reads as automatic.
// level is LevelDebug for the record of a change the user made, which a
// notice already showed.
func goalNote(text string) (note, level string, ok bool) {
	kind, body := goal.Parse(text)
	switch kind {
	case goal.KindContinuation:
		note = "↻ continuing the goal automatically"
		if _, after, found := strings.Cut(body, "- Automatic continuations: "); found {
			count, _, _ := strings.Cut(after, "\n")
			note += " (" + count + ")"
		}

		return note, session.LevelInfo, true
	case goal.KindBudgetLimit:
		return "goal budget reached; the agent was told to wrap up", session.LevelWarning, true
	case goal.KindObjectiveUpdated:
		return "the agent was told the goal's objective changed", session.LevelInfo, true
	case goal.KindUser:
		return "goal: " + strings.ReplaceAll(body, "\n", " · "), LevelDebug, true
	case goal.KindNone:
	}

	return "", "", false
}

// goalItem is goalNote as the transcript item of message id.
func goalItem(id, text string) (Item, bool) {
	note, level, ok := goalNote(text)
	if !ok {
		return Item{}, false
	}

	return Item{Kind: KindNotice, Key: "msg:" + id, Text: note, Level: level}, true
}

// goalStatusLine is /status's line about the goal, "" without one.
func (s State) goalStatusLine() string {
	if s.Goal == nil {
		return ""
	}
	g := s.Goal.Goal
	g.TimeUsedSeconds += s.goalLiveSeconds()

	return "goal: " + goal.Line(g)
}
