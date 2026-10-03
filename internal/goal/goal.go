// Package goal is Codex's /goal as data: a session's persisted objective,
// its status and usage, the texts that tell the model about it, and the
// goal tools' definitions and results. It does no I/O; the session keeps
// the goal, decides when to continue, and accounts its usage
// (internal/session/goal.go). See docs/design/goal.md.
package goal

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Status is where a goal stands, with Codex's names (ThreadGoalStatus).
type Status string

const (
	// StatusActive continues automatically whenever the session goes idle.
	StatusActive Status = "active"
	// StatusPaused waits for /goal resume: the user paused it, or
	// interrupted a run while it was active.
	StatusPaused Status = "paused"
	// StatusBlocked is stalled: the model reported the same blocker three
	// goal turns in a row, a run failed, or the turns stopped making
	// progress.
	StatusBlocked Status = "blocked"
	// StatusBudgetLimited ran out of its token budget or its automatic
	// continuations; the model is told to wrap up and nothing continues.
	StatusBudgetLimited Status = "budget_limited"
	// StatusComplete is achieved: the model marked it complete.
	StatusComplete Status = "complete"
)

// Valid reports whether s is one of the statuses.
func (s Status) Valid() bool {
	switch s {
	case StatusActive, StatusPaused, StatusBlocked, StatusBudgetLimited, StatusComplete:
		return true
	}

	return false
}

// Label is the status as Codex's TUI words it.
func (s Status) Label() string {
	switch s {
	case StatusActive:
		return "active"
	case StatusPaused:
		return "paused"
	case StatusBlocked:
		return "stalled"
	case StatusBudgetLimited:
		return "limited by budget"
	case StatusComplete:
		return "complete"
	}

	return string(s)
}

// Finished reports whether the goal is done for good: complete, or out of
// budget. A new goal may replace it without asking.
func (s Status) Finished() bool { return s == StatusComplete || s == StatusBudgetLimited }

// MaxObjectiveChars is Codex's limit on an objective
// (MAX_THREAD_GOAL_OBJECTIVE_CHARS).
const MaxObjectiveChars = 4000

// DefaultMaxContinuations is how many automatic continuations a goal gets
// before it stops as budget-limited, unless [goals] max_continuations says
// otherwise. Codex has no such cap; uah adds it so a goal without a token
// budget cannot run forever.
const DefaultMaxContinuations = 50

// Goal is a session's goal. The session keeps it in its sidecar, so it
// survives a restart, a resume, and a compaction.
type Goal struct {
	ID        string `json:"goal_id"`
	Objective string `json:"objective"`
	Status    Status `json:"status"`
	// TokenBudget is the most tokens the goal may use (0: none). Tokens
	// count as Codex counts them: input not read from the cache, plus
	// output, of each model response while the goal is active.
	TokenBudget     int64 `json:"token_budget,omitempty"`
	TokensUsed      int64 `json:"tokens_used"`
	TimeUsedSeconds int64 `json:"time_used_seconds"`
	// Continuations are the runs uah started on its own for the goal, and
	// MaxContinuations the most it may start (0: no limit). uah's.
	Continuations    int `json:"continuations"`
	MaxContinuations int `json:"max_continuations,omitempty"`
	// Reason says why the session, not the model or the user, stopped the
	// goal, such as "3 automatic turns made no progress". uah's.
	Reason    string    `json:"reason,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Settings are the [goals] configuration.
type Settings struct {
	// Disabled turns goals off: no /goal, no tools ([features] goals = false).
	Disabled bool
	// MaxTokenBudget caps a goal's token budget and is the budget of a new
	// goal that names none, as Codex's max_goal_token_budget (0: none).
	MaxTokenBudget int64
	// MaxContinuations is each goal's cap on automatic continuations (0: no
	// limit). Config's unset value is DefaultMaxContinuations.
	MaxContinuations int
}

// CheckObjective trims the objective and checks it as Codex does.
func CheckObjective(objective string) (string, error) {
	objective = strings.TrimSpace(objective)
	if objective == "" {
		return "", errors.New("goal objective must not be empty")
	}
	if utf8.RuneCountInString(objective) > MaxObjectiveChars {
		return "", fmt.Errorf("goal objective must be at most %d characters", MaxObjectiveChars)
	}

	return objective, nil
}

// CheckBudget checks a requested token budget against the cap, as Codex's
// validate_goal_budget (0: none requested).
func CheckBudget(budget, maxBudget int64) error {
	if budget < 0 {
		return errors.New("goal budgets must be positive when provided")
	}
	if budget > 0 && maxBudget > 0 && budget > maxBudget {
		return fmt.Errorf("goal token budget %d exceeds the maximum allowed goal token budget of %d", budget, maxBudget)
	}

	return nil
}

// RemainingTokens is what is left of the budget, or -1 without one.
func (g Goal) RemainingTokens() int64 {
	if g.TokenBudget <= 0 {
		return -1
	}

	return max(g.TokenBudget-g.TokensUsed, 0)
}

// OverBudget reports whether the goal has used its token budget.
func (g Goal) OverBudget() bool { return g.TokenBudget > 0 && g.TokensUsed >= g.TokenBudget }

// OutOfContinuations reports whether the goal has used its automatic
// continuations.
func (g Goal) OutOfContinuations() bool {
	return g.MaxContinuations > 0 && g.Continuations >= g.MaxContinuations
}

// TokenDelta is what one model response adds to a goal's usage: Codex's
// goal_token_delta_for_usage, input not read from the cache plus output.
func TokenDelta(input, cached, output int64) int64 {
	return max(input-cached, 0) + max(output, 0)
}
