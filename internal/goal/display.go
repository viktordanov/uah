package goal

import (
	"fmt"
	"strconv"
	"strings"
)

// Usage is the command's synopsis, as Codex's GOAL_USAGE, with uah's
// status.
const Usage = "Usage: /goal [<objective>|clear|edit|pause|resume|status]"

// Indicator is the footer's text for the goal, as Codex's status line
// words it: "Pursuing goal (12.5K / 50K)", "Goal paused (/goal resume)".
// live is the time the current run has added and the goal does not count
// yet.
func Indicator(g Goal, liveSeconds int64) string {
	switch g.Status {
	case StatusActive:
		if g.TokenBudget > 0 {
			return fmt.Sprintf("Pursuing goal (%s / %s)", Tokens(g.TokensUsed), Tokens(g.TokenBudget))
		}

		return fmt.Sprintf("Pursuing goal (%s)", Elapsed(g.TimeUsedSeconds+liveSeconds))
	case StatusPaused:
		return "Goal paused (/goal resume)"
	case StatusBlocked:
		return "Goal stalled (/goal resume)"
	case StatusBudgetLimited:
		switch {
		case g.OverBudget():
			return fmt.Sprintf("Goal unmet (%s / %s tokens)", Tokens(g.TokensUsed), Tokens(g.TokenBudget))
		case g.OutOfContinuations():
			return fmt.Sprintf("Goal unmet (%d / %d continuations)", g.Continuations, g.MaxContinuations)
		}

		return "Goal abandoned"
	case StatusComplete:
		if g.TokenBudget > 0 {
			return fmt.Sprintf("Goal achieved (%s tokens)", Tokens(g.TokensUsed))
		}

		return fmt.Sprintf("Goal achieved (%s)", Elapsed(g.TimeUsedSeconds))
	}

	return ""
}

// Summary is the goal in lines for /goal status: Codex's goal summary,
// with uah's continuations and the reason the session stopped it.
func Summary(g Goal) []string {
	lines := []string{
		"Status: " + g.Status.Label(),
		"Objective: " + g.Objective,
		"Time used: " + Elapsed(g.TimeUsedSeconds),
		"Tokens used: " + Tokens(g.TokensUsed),
	}
	if g.TokenBudget > 0 {
		lines = append(lines, "Token budget: "+Tokens(g.TokenBudget))
	}
	if g.MaxContinuations > 0 {
		lines = append(lines, fmt.Sprintf("Automatic continuations: %d of %d", g.Continuations, g.MaxContinuations))
	} else {
		lines = append(lines, fmt.Sprintf("Automatic continuations: %d", g.Continuations))
	}
	if g.Reason != "" {
		lines = append(lines, "Stopped: "+g.Reason)
	}
	switch g.Status {
	case StatusActive:
		lines = append(lines, "Commands: /goal edit, /goal pause, /goal clear")
	case StatusPaused, StatusBlocked:
		lines = append(lines, "Commands: /goal edit, /goal resume, /goal clear")
	case StatusBudgetLimited, StatusComplete:
		lines = append(lines, "Commands: /goal edit, /goal clear")
	}

	return lines
}

// Line is the goal in one line, for a notice or `uah exec`: Codex's
// goal_usage_summary after the status, "Goal active · Objective: … Time:
// 2m. Tokens: 12.5K/50K."
func Line(g Goal) string {
	parts := []string{"Goal " + g.Status.Label() + " · Objective: " + g.Objective}
	if g.TimeUsedSeconds > 0 {
		parts = append(parts, "Time: "+Elapsed(g.TimeUsedSeconds)+".")
	}
	if g.TokenBudget > 0 {
		parts = append(parts, "Tokens: "+Tokens(g.TokensUsed)+"/"+Tokens(g.TokenBudget)+".")
	}
	if g.Reason != "" {
		parts = append(parts, "Stopped: "+g.Reason+".")
	}

	return strings.Join(parts, " ")
}

// Elapsed is Codex's compact time: 59s, 30m, 1h 30m, 2d 23h 42m.
func Elapsed(seconds int64) string {
	seconds = max(seconds, 0)
	if seconds < 60 {
		return strconv.FormatInt(seconds, 10) + "s"
	}
	minutes := seconds / 60
	if minutes < 60 {
		return strconv.FormatInt(minutes, 10) + "m"
	}
	hours, rest := minutes/60, minutes%60
	if hours >= 24 {
		return fmt.Sprintf("%dd %dh %dm", hours/24, hours%24, rest)
	}
	if rest == 0 {
		return strconv.FormatInt(hours, 10) + "h"
	}

	return fmt.Sprintf("%dh %dm", hours, rest)
}

// Tokens is Codex's compact count: 950, 12.5K, 50K, 1.2M.
func Tokens(n int64) string {
	unit := func(v float64, suffix string) string {
		return strings.TrimSuffix(strconv.FormatFloat(v, 'f', 1, 64), ".0") + suffix
	}
	switch {
	case n >= 1_000_000:
		return unit(float64(n)/1e6, "M")
	case n >= 1_000:
		return unit(float64(n)/1e3, "K")
	}

	return strconv.FormatInt(n, 10)
}
