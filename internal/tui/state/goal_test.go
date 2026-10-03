package state_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/goal"
	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/internal/tui/state"
)

func activeGoal() goal.Goal {
	return goal.Goal{ID: "g1", Objective: "make the build pass", Status: goal.StatusActive, Continuations: 2, MaxContinuations: 50}
}

// TestReduce_GoalCommand: /goal's words map to the session's goal calls,
// alone or with status it shows the goal, and edit alone puts the
// objective in the composer.
func TestReduce_GoalCommand(t *testing.T) {
	s := opened()
	for args, want := range map[string]state.EffGoal{
		"make the build pass":    {Op: state.GoalOpSet, Text: "make the build pass"},
		"clear":                  {Op: state.GoalOpClear},
		"pause":                  {Op: state.GoalOpPause},
		"resume":                 {Op: state.GoalOpResume},
		"edit ship v2 instead":   {Op: state.GoalOpEdit, Text: "ship v2 instead"},
		"clear the cache folder": {Op: state.GoalOpSet, Text: "clear the cache folder"},
	} {
		_, effects := apply(s, state.Submit{Text: "/goal " + args})
		assert.Equal(t, []state.Effect{want}, effects, args)
	}

	got, effects := apply(opened(), state.Submit{Text: "/goal"})
	assert.Empty(t, effects)
	assert.Contains(t, got.Items[len(got.Items)-1].Text, "No goal is currently set.")
	_, effects = apply(opened(), state.Submit{Text: "/goal edit"})
	assert.Empty(t, effects, "nothing to edit")

	busy, _ := apply(s, session.InputQueued{At: t0, Input: core.UserInput{ID: "m1", Text: "work"}})
	_, effects = apply(busy, state.Submit{Text: "/goal pause"})
	assert.Equal(t, []state.Effect{state.EffGoal{Op: state.GoalOpPause}}, effects, "/goal works while the agent works, as in Codex")

	withGoal := func() state.State {
		s, _ := apply(opened(), session.GoalUpdated{At: t0, Goal: activeGoal(), Change: session.GoalSet, By: "user"})
		return s
	}
	_, effects = apply(withGoal(), state.Submit{Text: "/goal edit"})
	assert.Equal(t, []state.Effect{state.EffSetDraft{Text: "/goal edit make the build pass"}}, effects)
	got, _ = apply(withGoal(), state.Submit{Text: "/goal status"})
	summary := got.Items[len(got.Items)-1].Text
	assert.Contains(t, summary, "Status: active\nObjective: make the build pass")
	assert.Contains(t, summary, "Automatic continuations: 2 of 50")
}

// TestReduce_GoalEvents: the goal reaches the footer's indicator, changes
// show as notices (a guard's as a warning), a continuation's message is
// marked automatic, the user's change records only in the detailed view,
// and a clear or another session drops the goal.
func TestReduce_GoalEvents(t *testing.T) {
	s, _ := apply(opened(), session.GoalUpdated{At: t0, Goal: activeGoal(), Change: session.GoalSet, By: "user"})
	require.NotNil(t, s.Goal)
	assert.Equal(t, "Pursuing goal (0s)", s.GoalIndicator())
	assert.Equal(t, "Goal active · Objective: make the build pass", s.Items[len(s.Items)-1].Text)

	s, _ = apply(s, session.GoalContinued{At: t0, Goal: activeGoal()},
		core.RunStarted{At: t0, RunID: "run-1"},
		core.UserMessage{At: t0, ID: "u1", Text: goal.UserSet("make the build pass", "")},
		core.UserMessage{At: t0, ID: "c1", Text: goal.Continuation(activeGoal())},
	)
	assert.True(t, s.Busy)
	last := s.Items[len(s.Items)-1]
	assert.Equal(t, state.KindNotice, last.Kind)
	assert.Equal(t, "↻ continuing the goal automatically (2 of 50)", last.Text)
	record := s.Items[len(s.Items)-2]
	assert.Equal(t, state.LevelDebug, record.Level)
	assert.Equal(t, `goal: User set the goal: "make the build pass"`, record.Text)

	s.Now = t0.Add(90 * time.Second)
	assert.Equal(t, "Pursuing goal (1m)", s.GoalIndicator(), "the live run's time counts")

	blocked := activeGoal()
	blocked.Status, blocked.Reason = goal.StatusBlocked, "the run failed"
	s, _ = apply(s, session.GoalUpdated{At: t0, Goal: blocked, Change: session.GoalStatus, By: "uah"})
	assert.Equal(t, session.LevelWarning, s.Items[len(s.Items)-1].Level)
	assert.Equal(t, "Goal stalled (/goal resume)", s.GoalIndicator())

	s, _ = apply(s, session.GoalCleared{At: t0})
	assert.Nil(t, s.Goal)
	assert.Equal(t, "Goal cleared", s.Items[len(s.Items)-1].Text)
	s, _ = apply(s, session.GoalUpdated{At: t0, Goal: blocked, Change: session.GoalUsage}, session.SessionOpened{At: t0, ID: "sess-2"})
	assert.Nil(t, s.Goal)

	resumed, _ := apply(opened(), session.GoalUpdated{At: t0, Goal: blocked, Change: session.GoalRestored})
	assert.True(t, strings.HasSuffix(resumed.Items[len(resumed.Items)-1].Text, "/goal resume continues it."))
}
