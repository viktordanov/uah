package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/usage/tokstats"
)

// A date is local midnight, and --until includes it: the next midnight; a
// time is itself, and --until includes it too.
func TestParseDate(t *testing.T) {
	loc := time.FixedZone("test", 3*60*60)
	since, err := parseDate("2026-10-01", false, loc)
	require.NoError(t, err)
	assert.Equal(t, time.Date(2026, 10, 1, 0, 0, 0, 0, loc), since)
	until, err := parseDate("2026-10-01", true, loc)
	require.NoError(t, err)
	assert.Equal(t, time.Date(2026, 10, 2, 0, 0, 0, 0, loc), until)
	at, err := parseDate("2026-10-01T08:00:00Z", true, loc)
	require.NoError(t, err)
	assert.Equal(t, time.Date(2026, 10, 1, 8, 0, 0, 1, time.UTC), at)
	none, err := parseDate("", true, loc)
	require.NoError(t, err)
	assert.True(t, none.IsZero())
	_, err = parseDate("yesterday", false, loc)
	assert.Error(t, err)

	santiago, err := time.LoadLocation("America/Santiago") // clocks skip 2026-09-06's midnight
	require.NoError(t, err)
	since, err = parseDate("2026-09-06", false, santiago)
	require.NoError(t, err)
	assert.Equal(t, "2026-09-06 01:00", since.Format("2006-01-02 15:04"), "the day's first instant")
	until, err = parseDate("2026-09-05", true, santiago)
	require.NoError(t, err)
	assert.Equal(t, since, until, "the day after's first instant")
}

func TestStatsView_Text(t *testing.T) {
	loc := time.FixedZone("test", 3*60*60)
	v := statsView{now: time.Date(2026, 10, 16, 15, 0, 0, 0, loc), by: tokstats.ByDay}
	assert.Equal(t, "Mon Oct 12", v.keyText("2026-10-12"))
	assert.Equal(t, "Fri Oct 17 2025", v.keyText("2025-10-17"), "another year's date says it")
	assert.Equal(t, "today", v.marker("2026-10-16"))
	v.by = tokstats.ByWeek
	assert.Equal(t, "Oct 12 – 18", v.keyText("2026-W42"))
	assert.Equal(t, "Sep 28 – Oct 4", v.keyText("2026-W40"))
	assert.Equal(t, "Dec 29 2025 – Jan 4 2026", v.keyText("2026-W01"), "week 1 holds January 4")
	assert.Equal(t, "this week", v.marker("2026-W42"))
	v.by = tokstats.ByMonth
	assert.Equal(t, "October 2026", v.keyText("2026-10"))
	assert.Equal(t, "this month", v.marker("2026-10"))
	assert.Empty(t, v.marker("2026-09"))

	santiago, err := time.LoadLocation("America/Santiago") // clocks skip 2026-09-06's midnight
	require.NoError(t, err)
	v = statsView{now: time.Date(2026, 10, 16, 15, 0, 0, 0, santiago), by: tokstats.ByDay}
	assert.Equal(t, "Sun Sep 6", v.keyText("2026-09-06"))
}

func TestStatsView_Comparison(t *testing.T) {
	week := func(total int64) *tokstats.Report {
		return &tokstats.Report{Sum: tokstats.Row{Tokens: core.Tokens{InputTokens: total}}, Days: tokstats.Days{Days: 7}}
	}
	v := statsView{shown: *week(3_400_000), prev: week(2_000_000)}
	assert.Equal(t, "1.7× the 7 days before (2.0M)", v.comparison())
	v.prev = week(4_000_000)
	assert.Equal(t, "15% less than the 7 days before (4.0M)", v.comparison())
	v.prev = week(3_300_000)
	assert.Equal(t, "about the same as the 7 days before (3.3M)", v.comparison())
	v.prev, v.span = week(2_000_000), true
	assert.Equal(t, "1.7× the same span before (2.0M)", v.comparison(), "RFC 3339 bounds compare a span, not days")
	v.prev = week(0)
	assert.Empty(t, v.comparison(), "nothing to compare with")
}

func TestBar(t *testing.T) {
	assert.Equal(t, strings.Repeat("█", barWidth), bar(100, 100))
	assert.Equal(t, "██████", bar(50, 100))
	assert.Equal(t, "▏", bar(1, 1_000_000), "any tokens show")
	assert.Empty(t, bar(0, 100))
}

// The summary names workspaces by their last element, with the path when
// two share it, and puts the temporary ones together.
func TestWorkspaceShares(t *testing.T) {
	row := func(ws string, total int64) tokstats.Row {
		return tokstats.Row{Key: ws, Tokens: core.Tokens{InputTokens: total}}
	}
	got := workspaceShares([]tokstats.Row{
		row("/src/web-tty", 50), row("/tmp/bench.1", 30), row("/a/uah", 20), row("/b/uah", 10), row(filepath.Join(os.TempDir(), "x"), 15),
	})
	assert.Equal(t, []share{{"web-tty", 50}, {"temporary", 45}, {"/a/uah", 20}, {"/b/uah", 10}}, got)
	assert.Equal(t, "web-tty 40% · temporary 36% · /a/uah 16% · 1 other 8%", topShares(got, 125, 3))
	assert.Equal(t, "web-tty 99% · 1 other 0.8%", topShares([]share{{"web-tty", 990}, {"x", 8}}, 998, 3), "under 1% goes to the others")
}

// The heatmap has a column for every week, Monday to Sunday, that the
// range touches.
func TestWeeksBetween(t *testing.T) {
	day := func(d int) time.Time { return time.Date(2026, 10, d, 12, 0, 0, 0, time.UTC) }
	assert.Equal(t, 2, weeksBetween(day(11), day(16)), "a Sunday and the Friday after")
	assert.Equal(t, 1, weeksBetween(day(12), day(18)))
	assert.Equal(t, 4, weeksBetween(day(4), day(19)), "a Sunday to a Monday two weeks on")
	assert.True(t, isDate("2026-10-01"))
	assert.False(t, isDate("2026-10-01T08:00:00Z"))
}
