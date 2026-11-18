package state_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/internal/tui/state"
	"github.com/viktordanov/uah/internal/usage"
	"github.com/viktordanov/uah/internal/usage/cachestats"
)

// weekly is a snapshot with a weekly window used, resetting at 15:00 on
// the day after t0, and a 5h window at 10%.
func weekly(used float64) usage.Snapshot {
	return usage.Snapshot{Plan: "pro", CapturedAt: t0, Limits: []usage.Limit{{
		ID:        usage.CodexLimitID,
		Primary:   &usage.Window{UsedPercent: 10, Minutes: 300, ResetsAt: t0.Add(2 * time.Hour)},
		Secondary: &usage.Window{UsedPercent: used, Minutes: 7 * 24 * 60, ResetsAt: t0.Add(27 * time.Hour)},
	}}}
}

// notices are the texts of the notices from index from on.
func notices(s state.State, from int) []string {
	var out []string
	for _, it := range s.Items[from:] {
		if it.Kind == state.KindNotice {
			out = append(out, it.Text)
		}
	}

	return out
}

func finished(run string) core.RunFinished {
	return core.RunFinished{At: t0, Result: core.Result{Request: core.Request{RunID: run}, Status: core.StatusOK}}
}

func TestUsage_ReadAfterEachRun(t *testing.T) {
	s, effects := apply(opened(), core.RunStarted{At: t0, RunID: "run-1"}, finished("run-1"))
	assert.Equal(t, []state.Effect{state.EffLoadUsage{Reason: state.UsageAfterRun, MaxAge: usage.CacheFor}}, effects)
	_, ok := s.UsageLeft()
	assert.False(t, ok, "nothing in the footer before a read")

	s, _ = apply(s, state.UsageLoaded{Reason: state.UsageAfterRun, Snapshot: weekly(22), At: t0})
	left, ok := s.UsageLeft()
	require.True(t, ok)
	assert.Equal(t, "weekly 78% left", left, "the tightest window")
}

func TestUsage_Unsupported(t *testing.T) {
	unsupported := fmt.Errorf("%w for ollama", usage.ErrUnsupported)
	s, _ := apply(opened(), state.UsageLoaded{Reason: state.UsageAfterRun, Err: unsupported})
	_, ok := s.UsageLeft()
	assert.False(t, ok)
	assert.Empty(t, notices(s, 0), "silent after a run")
	s, effects := apply(s, core.RunStarted{At: t0, RunID: "run-2"}, finished("run-2"))
	assert.Empty(t, effects, "no more reads once the provider has none")

	n := len(s.Items)
	s, _ = apply(s, state.UsageLoaded{Reason: state.UsageStatus, Err: unsupported})
	assert.Equal(t, []string{"usage is not available for openai-codex"}, notices(s, n), "the session's provider")
}

func TestUsage_StatusRows(t *testing.T) {
	s, effects := apply(opened(), state.Submit{Text: "/status"})
	assert.Contains(t, effects, state.EffLoadUsage{Reason: state.UsageStatus})
	n := len(s.Items)

	s, _ = apply(s, state.UsageLoaded{Reason: state.UsageStatus, Snapshot: weekly(22), At: t0})
	assert.Equal(t, []string{"usage · pro plan · ChatGPT personal\n" +
		"5h     [██████████████████░░] 90% left (resets 14:00)\n" +
		"weekly [███████████████░░░░░] 78% left (resets 15:00 on 25 Sep)"}, notices(s, n))

	// A failed read shows the last snapshot, marked stale after 15 minutes.
	n = len(s.Items)
	s, _ = apply(s, state.UsageLoaded{Reason: state.UsageStatus, Snapshot: weekly(22), Err: errors.New("the usage request returned 502 Bad Gateway"), At: t0.Add(20 * time.Minute)})
	got := notices(s, n)
	require.Len(t, got, 2)
	assert.Equal(t, "usage: the usage request returned 502 Bad Gateway", got[0])
	assert.Contains(t, got[1], "usage · pro plan · ChatGPT personal · stale, read 20m0s ago\n")
}

func TestUsage_Warnings(t *testing.T) {
	s := opened()
	read := func(used float64) []string {
		n := len(s.Items)
		s, _ = apply(s, state.UsageLoaded{Reason: state.UsageAfterRun, Snapshot: weekly(used), At: t0})

		return notices(s, n)
	}
	assert.Empty(t, read(50))
	assert.Equal(t, []string{"Heads up, you have less than 25% of your weekly limit left (resets 15:00 on 25 Sep)"}, read(76))
	assert.Empty(t, read(80), "once per threshold")
	assert.Equal(t, []string{"Heads up, you have less than 5% of your weekly limit left (resets 15:00 on 25 Sep)"}, read(96), "the highest one crossed")
	assert.Empty(t, read(97))
	assert.Empty(t, read(5), "the window reset")
	assert.Len(t, read(91), 1, "and warns again")

	// /status records the thresholds without a warning of its own.
	s = opened()
	s, _ = apply(s, state.UsageLoaded{Reason: state.UsageStatus, Snapshot: weekly(80), At: t0})
	assert.Empty(t, read(80))
}

func TestUsage_LimitReached(t *testing.T) {
	s, _ := apply(opened(), core.RunStarted{At: t0, RunID: "run-1"})
	s, effects := apply(s, core.RunnerError{At: t0, Message: "responses API request failed with status 429: The usage limit has been reached"})
	assert.Equal(t, []state.Effect{state.EffLoadUsage{Reason: state.UsageLimit}}, effects, "read when to try again")
	s, effects = apply(s, core.ModelResponded{At: t0, Failure: "usage_limit_reached: The usage limit has been reached"})
	assert.Empty(t, effects, "once per run")

	full := weekly(100)
	full.Limits[0].Secondary.UsedPercent = 100
	n := len(s.Items)
	s, _ = apply(s, state.UsageLoaded{Reason: state.UsageLimit, Snapshot: full, At: t0})
	assert.Equal(t, []string{"Usage limit reached; try again at 15:00 on 25 Sep."}, notices(s, n))

	// A failure that kept the 429's body needs no read.
	s, _ = apply(s, core.RunStarted{At: t0, RunID: "run-2"})
	n = len(s.Items)
	s, effects = apply(s, core.RunnerError{At: t0, Message: `failed with status 429: {"error":{"type":"usage_limit_reached","resets_at":` + fmt.Sprint(t0.Add(3*time.Hour).Unix()) + `}}`})
	assert.Empty(t, effects)
	assert.Equal(t, []string{
		`failed with status 429: {"error":{"type":"usage_limit_reached","resets_at":` + fmt.Sprint(t0.Add(3*time.Hour).Unix()) + `}}`,
		"Usage limit reached; try again at 15:00.",
	}, notices(s, n))

	// A failed read falls back to the last snapshot, and without one points
	// to ChatGPT.
	n = len(s.Items)
	s, _ = apply(s, state.UsageLoaded{Reason: state.UsageLimit, Err: errors.New("offline")})
	assert.Equal(t, []string{"Usage limit reached; try again at 15:00 on 25 Sep."}, notices(s, n))
	s = opened()
	s, _ = apply(s, state.UsageLoaded{Reason: state.UsageLimit, Err: errors.New("offline")}, session.Idle{At: t0})
	assert.Equal(t, []string{"Usage limit reached; see https://chatgpt.com/codex/settings/usage."}, notices(s, 0))
}

// TestUsage_Command is /usage: a fresh read shown as /status shows it,
// also while the agent works.
func TestUsage_Command(t *testing.T) {
	s, effects := apply(opened(), state.Submit{Text: "/usage"})
	assert.Equal(t, []state.Effect{state.EffLoadUsage{Reason: state.UsageStatus}, state.EffLoadCache{SessionID: "sess-1"}}, effects)
	n := len(s.Items)
	s, _ = apply(s, state.UsageLoaded{Reason: state.UsageStatus, Snapshot: weekly(22), At: t0})
	assert.Contains(t, notices(s, n)[0], "usage · pro plan · ChatGPT personal\n")
	assert.Contains(t, s.Suggestions("/us")[0].Label, "usage", "the menu offers it")
}

func TestUsage_Cache(t *testing.T) {
	_, effects := apply(opened(), state.Submit{Text: "/status"})
	assert.Contains(t, effects, state.EffLoadCache{SessionID: "sess-1"}, "/status shows the cache too")
	_, effects = apply(state.New(t0), state.Submit{Text: "/usage"})
	assert.NotContains(t, effects, state.EffLoadCache{}, "no session, no cache to read")

	s := opened()
	n := len(s.Items)
	sum := cachestats.Summary{
		Requests: 9, Input: 400_000, Cached: 352_000, Output: 9_000, MissedShare: 0.08,
		Missed: []cachestats.Miss{{Cause: cachestats.CauseEffort, Tokens: 28_000}, {Cause: cachestats.CauseIdle, Tokens: 9_000}, {Cause: cachestats.CauseCold, Tokens: 4_000}},
	}
	s, _ = apply(s, state.CacheLoaded{Summary: sum})
	assert.Equal(t, []string{"prompt cache 88% · missed 41k: effort switches 28k, idle 9k, cold start 4k · ≈8% of usage (API-price estimate)"}, notices(s, n))
}
