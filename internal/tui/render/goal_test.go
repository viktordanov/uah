package render_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/viktordanov/uah/internal/goal"
	"github.com/viktordanov/uah/internal/session"
)

// TestFooterShowsTheGoal: the compact and the detailed footer carry
// Codex's goal indicator while the session has a goal.
func TestFooterShowsTheGoal(t *testing.T) {
	g := goal.Goal{Objective: "ship", Status: goal.StatusActive, TokenBudget: 50_000, TokensUsed: 12_500}
	s := apply(base(), session.GoalUpdated{At: t0, Goal: g, Change: session.GoalSet})

	assert.Contains(t, footer(s), "Pursuing goal (12.5K / 50K)")
	s.Details = true
	assert.Contains(t, footer(s), "Pursuing goal (12.5K / 50K)")

	g.Status = goal.StatusPaused
	s = apply(base(), session.GoalUpdated{At: t0, Goal: g, Change: session.GoalStatus})
	assert.Contains(t, footer(s), "Goal paused (/goal resume)")
	assert.NotContains(t, footer(base()), "goal")
}
