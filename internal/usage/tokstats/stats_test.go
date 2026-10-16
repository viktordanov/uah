package tokstats_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/usage/tokstats"
)

var (
	loc   = time.FixedZone("test", 2*60*60)
	today = time.Date(2026, 10, 16, 15, 0, 0, 0, loc)
)

func at(day, hour int) time.Time { return time.Date(2026, 10, day, hour, 0, 0, 0, loc) }

// tokens is a run's: total is input - cached + output.
func tokens(input, cached, output int64) core.Tokens {
	return core.Tokens{InputTokens: input, CachedInputTokens: cached, OutputTokens: output}
}

func run(t time.Time, session, model, ws, source string, tok core.Tokens) tokstats.Entry {
	return tokstats.Entry{At: t, Session: session, Model: model, Workspace: ws, Source: source, Tokens: tok, Run: true}
}

func sample() []tokstats.Entry {
	return []tokstats.Entry{
		run(at(12, 10), "s1", "astra", "/a", tokstats.SourceTUI, tokens(1000, 800, 100)), // total 300
		run(at(12, 11), "s1", "astra", "/a", tokstats.SourceTUI, tokens(500, 400, 50)),   // 150
		run(at(14, 9), "s2", "sol", "/b", tokstats.SourceExec, tokens(2000, 0, 200)),     // 2200
		run(at(15, 23), "s3", "astra", "/a", tokstats.SourceSubagent, tokens(100, 0, 0)), // 100
		run(at(16, 1), "s1", "astra", "/a", tokstats.SourceTUI, tokens(400, 100, 100)),   // 400
		{At: at(16, 2), Session: "s1", Model: "sol", Workspace: "/a", Source: tokstats.SourceCompaction, Tokens: tokens(300, 0, 50)}, // 350, no run
	}
}

func TestBuild_ByDay(t *testing.T) {
	r := tokstats.Build(sample(), tokstats.Options{By: tokstats.ByDay, Now: today})
	require.Len(t, r.Rows, 5, "every day from the first entry's to today, an empty one too")
	assert.Equal(t, "2026-10-12", r.Rows[0].Key)
	assert.Equal(t, tokstats.Row{Key: "2026-10-12", Runs: 2, Sessions: 1, Tokens: tokens(1500, 1200, 150)}, r.Rows[0])
	assert.Equal(t, tokstats.Row{Key: "2026-10-13"}, r.Rows[1])
	assert.Equal(t, 1, r.Rows[4].Runs, "a compaction call is no run")
	assert.Equal(t, int64(750), r.Rows[4].Total())

	assert.Equal(t, 5, r.Sum.Runs)
	assert.Equal(t, 3, r.Sum.Sessions)
	assert.Equal(t, int64(3500), r.Sum.Total())
	assert.Equal(t, int64(700), r.PerRun)
	assert.Equal(t, int64(1166), r.PerSession)
}

func TestBuild_Days(t *testing.T) {
	d := tokstats.Build(sample(), tokstats.Options{Now: today}).Days
	// Active days' totals: 450, 2200, 100, 750.
	assert.Equal(t, tokstats.Days{Days: 5, Active: 4, Mean: 875, Median: 600, P90: 2200, Max: 2200, Peak: "2026-10-14", Streak: 3}, d)

	d = tokstats.Build(sample(), tokstats.Options{Now: at(17, 9)}).Days
	assert.Equal(t, 3, d.Streak, "today with nothing yet keeps yesterday's streak")
	d = tokstats.Build(sample(), tokstats.Options{Now: at(18, 9)}).Days
	assert.Zero(t, d.Streak)
}

func TestBuild_Range(t *testing.T) {
	r := tokstats.Build(sample(), tokstats.Options{By: tokstats.ByDay, Now: today, Since: at(14, 0), Until: at(16, 0)})
	require.Len(t, r.Rows, 2, "Until is excluded")
	assert.Equal(t, "2026-10-14", r.Rows[0].Key)
	assert.Equal(t, int64(2300), r.Sum.Total())
	assert.Equal(t, 2, r.Days.Days)

	none := tokstats.Build(nil, tokstats.Options{By: tokstats.ByDay, Now: today})
	assert.Len(t, none.Rows, 1, "today alone")
	assert.Zero(t, none.Days.Active)
	assert.Zero(t, none.PerRun)
	past := tokstats.Build(nil, tokstats.Options{By: tokstats.ByDay, Now: today, Until: at(2, 0)})
	require.Len(t, past.Rows, 1)
	assert.Equal(t, "2026-10-01", past.Rows[0].Key, "an empty range keeps its Until")
}

func TestBuild_Groupings(t *testing.T) {
	keys := func(by tokstats.By) []string {
		var out []string
		for _, r := range tokstats.Build(sample(), tokstats.Options{By: by, Now: today}).Rows {
			out = append(out, r.Key)
		}

		return out
	}
	assert.Equal(t, []string{"2026-W42"}, keys(tokstats.ByWeek))
	assert.Equal(t, []string{"2026-10"}, keys(tokstats.ByMonth))
	assert.Equal(t, []string{"sol", "astra"}, keys(tokstats.ByModel), "the largest total first")
	assert.Equal(t, []string{"/b", "/a"}, keys(tokstats.ByWorkspace))
	assert.Equal(t, []string{"exec", "tui", "compaction", "subagent"}, keys(tokstats.BySource))

	weeks := tokstats.Build(sample(), tokstats.Options{By: tokstats.ByWeek, Now: today, Since: at(1, 0)}).Rows
	assert.Equal(t, []string{"2026-W40", "2026-W41", "2026-W42"}, []string{weeks[0].Key, weeks[1].Key, weeks[2].Key}, "weeks start on Monday")
}

func TestBuild_Workspace(t *testing.T) {
	r := tokstats.Build(sample(), tokstats.Options{Now: today, Workspace: "/b", Normal: func(s string) string { return s }})
	assert.Equal(t, 1, r.Sum.Runs)
	assert.Equal(t, int64(2200), r.Sum.Total())
}

func TestBuild_Models(t *testing.T) {
	m := tokstats.Build(sample(), tokstats.Options{Now: today}).Models
	require.Len(t, m, 2)
	assert.Equal(t, "sol", m[0].Model)
	assert.Equal(t, int64(2550), m[0].Total)
	assert.InDelta(t, 72.86, m[0].Percent, 0.01)
}

// Where a clock change skips midnight, every date still has its row.
func TestBuild_DaysAcrossAMidnightClockChange(t *testing.T) {
	santiago, err := time.LoadLocation("America/Santiago")
	require.NoError(t, err)
	now := time.Date(2026, 9, 7, 15, 0, 0, 0, santiago)
	entries := []tokstats.Entry{
		run(time.Date(2026, 9, 5, 10, 0, 0, 0, santiago), "s1", "astra", "/a", tokstats.SourceTUI, tokens(10, 0, 0)),
		run(time.Date(2026, 9, 7, 10, 0, 0, 0, santiago), "s1", "astra", "/a", tokstats.SourceTUI, tokens(20, 0, 0)),
	}
	r := tokstats.Build(entries, tokstats.Options{By: tokstats.ByDay, Now: now})
	var keys []string
	for _, row := range r.Rows {
		keys = append(keys, row.Key)
	}
	assert.Equal(t, []string{"2026-09-05", "2026-09-06", "2026-09-07"}, keys)
	assert.Equal(t, 3, r.Days.Days)
	assert.Equal(t, 2, r.Days.Active)
	assert.Equal(t, 1, r.Days.Streak)
}
