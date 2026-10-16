package main

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/internal/usage/tokstats"
)

const (
	flagBy      = "by"
	flagUntil   = "until"
	flagWide    = "wide"
	flagHeatmap = "heatmap"
)

// statsCommand is `uah stats`: the tokens of every session, by day, week,
// month, model, workspace, or source (docs/design/token-stats.md).
func statsCommand() *cli.Command {
	bys := make([]string, len(tokstats.Bys))
	for i, b := range tokstats.Bys {
		bys[i] = string(b)
	}

	return &cli.Command{
		Name:  "stats",
		Usage: "add up the tokens of every session by day, week, month, model, workspace, or source",
		Description: "Adds up each finished run's tokens, and each compaction call's, from every workspace\n" +
			"unless -C names one. Without --by, --since, or --until it shows the last 7 days by day,\n" +
			"compared with the 7 before, and all time under them. Days are local; weeks start on\n" +
			"Monday. total is input not served from the cache plus output (cached is part of input,\n" +
			"reasoning part of output). A run counts on the day it started.",
		Flags: []cli.Flag{
			stateDirFlag(),
			&cli.StringFlag{Name: flagBy, Usage: "group the rows by " + strings.Join(bys, ", "), Value: string(tokstats.ByDay), Validator: func(v string) error {
				if !slices.Contains(tokstats.Bys, tokstats.By(v)) {
					return fmt.Errorf("invalid --by %q (want %s)", v, strings.Join(bys, ", "))
				}

				return nil
			}},
			&cli.StringFlag{Name: flagSince, Usage: "from this date (2026-10-01) or RFC 3339 time", Validator: validDate(flagSince)},
			&cli.StringFlag{Name: flagUntil, Usage: "to this date or RFC 3339 time, included", Validator: validDate(flagUntil)},
			&cli.StringFlag{Name: flagWorkspace, Aliases: []string{"C"}, Usage: "only this directory's sessions", DefaultText: "every directory", TakesFile: true},
			&cli.BoolFlag{Name: flagWide, Usage: "show input, cached, output, and reasoning tokens"},
			&cli.BoolFlag{Name: flagHeatmap, Usage: "show a heatmap of the tokens per day in place of the table (26 weeks, or since --since)"},
			&cli.BoolFlag{Name: flagJSON, Usage: "print the report as JSON"},
		},
		OnUsageError: onUsageError,
		Action:       statsAction,
	}
}

func statsAction(ctx context.Context, cmd *cli.Command) error {
	stateDir, err := filepath.Abs(cmd.String("state-dir"))
	if err != nil {
		return fmt.Errorf("failed to resolve state dir: %w", err)
	}
	entries, err := tokstats.Collect(ctx, stateDir)
	if err != nil {
		return err
	}
	now := time.Now()
	o := tokstats.Options{By: tokstats.By(cmd.String(flagBy)), Now: now, Normal: session.NormalDir}
	o.Since, _ = parseDate(cmd.String(flagSince), false, now.Location()) // the validators checked them
	o.Until, _ = parseDate(cmd.String(flagUntil), true, now.Location())
	v := statsView{now: now, by: o.By, wide: cmd.Bool(flagWide)}
	if cmd.IsSet(flagWorkspace) {
		dir, err := currentDir(cmd)
		if err != nil {
			return err
		}
		o.Workspace, v.workspace = session.NormalDir(dir), dir
	}
	full := tokstats.Build(entries, o)
	if cmd.Bool(flagJSON) {
		return writeJSON(os.Stdout, statsJSONFor(full, o.By))
	}
	build := func(since, until time.Time, by tokstats.By) tokstats.Report {
		x := o
		x.Since, x.Until, x.By = since, until, by

		return tokstats.Build(entries, x)
	}
	v.shown, v.stats = full, full
	switch {
	case !cmd.IsSet(flagBy) && !cmd.IsSet(flagSince) && !cmd.IsSet(flagUntil):
		// The last 7 days by day, all time under them.
		since := startOfDay(now.Year(), now.Month(), now.Day()-6, now.Location())
		v.shown, v.allTime, v.label = build(since, time.Time{}, tokstats.ByDay), &full, "last 7 days"
		prev := build(startOfDay(now.Year(), now.Month(), now.Day()-13, now.Location()), since, tokstats.ByDay)
		v.prev = &prev
	case !o.Since.IsZero() && isDate(cmd.String(flagSince)) && (o.Until.IsZero() || isDate(cmd.String(flagUntil))):
		// Whole days: the same number of days before.
		n := v.shown.Days.Days
		f := v.shown.First
		prev := build(startOfDay(f.Year(), f.Month(), f.Day()-n, f.Location()), o.Since, tokstats.ByDay)
		v.prev = &prev
	case !o.Since.IsZero():
		// A time: the span of the same length before.
		end := cmp.Or(o.Until, now)
		prev := build(o.Since.Add(-end.Sub(o.Since)), o.Since, tokstats.ByDay)
		v.prev, v.span = &prev, true
	}
	if v.allTime != nil {
		v.stats = *v.allTime
	}
	if o.Workspace == "" && o.By != tokstats.ByWorkspace {
		v.places = build(o.Since, o.Until, tokstats.ByWorkspace).Rows // the statistics' range: all time in the default view
	}
	if cmd.Bool(flagHeatmap) {
		// Every week the range touches, ending with its last day's, at most
		// a year's; 26 weeks without --since.
		last := v.shown.Last
		weeks := heatmapWeeks
		if !o.Since.IsZero() {
			weeks = weeksBetween(v.shown.First, last)
		}
		v.heatWeeks, v.heatEnd = min(weeks, maxHeatmapWeeks), last
		v.heat = build(o.Since, o.Until, tokstats.ByDay).Rows
	}

	return v.print(os.Stdout)
}

// parseDate reads --since or --until: a date is the start of that local
// day (--until: of the next, so the day is included), else an RFC 3339 time (--until:
// included). "" is no bound.
func parseDate(v string, until bool, loc *time.Location) (time.Time, error) {
	if v == "" {
		return time.Time{}, nil
	}
	if d, err := time.Parse(time.DateOnly, v); err == nil { // the date's numbers: a local midnight may not exist
		y, m, day := d.Date()
		if until {
			day++
		}

		return startOfDay(y, m, day, loc), nil
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Time{}, err
	}
	if until {
		t = t.Add(time.Nanosecond)
	}

	return t, nil
}

// startOfDay is the first instant of a date: midnight, or where a clock
// change skips midnight, the hour after it, since time.Date normalizes a
// skipped midnight into the day before.
func startOfDay(y int, m time.Month, d int, loc *time.Location) time.Time {
	t := time.Date(y, m, d, 0, 0, 0, 0, loc)
	want := time.Date(y, m, d, 12, 0, 0, 0, loc).Format(time.DateOnly) // d may overflow the month
	for t.Format(time.DateOnly) < want {
		t = t.Add(time.Hour)
	}

	return t
}

// isDate reports whether a --since or --until value is a date, not a time.
func isDate(v string) bool {
	_, err := time.Parse(time.DateOnly, v)

	return err == nil
}

// weeksBetween counts the weeks, Monday to Sunday, that the days from first
// to last touch.
func weeksBetween(first, last time.Time) int {
	monday := func(t time.Time) time.Time { return t.AddDate(0, 0, -((int(t.Weekday()) + 6) % 7)) }
	days := int(math.Round(monday(last).Sub(monday(first)).Hours() / 24))

	return days/7 + 1
}

func validDate(flag string) func(string) error {
	return func(v string) error {
		if _, err := parseDate(v, false, time.Local); err != nil {
			return fmt.Errorf("invalid --%s %q (want a date, such as 2026-10-01, or an RFC 3339 time)", flag, v)
		}

		return nil
	}
}

// statsJSON is the report for scripts, with every count exact.
type statsJSON struct {
	First      string      `json:"first_day"`
	Last       string      `json:"last_day"`
	By         string      `json:"by"`
	Rows       []rowJSON   `json:"rows"`
	Total      rowJSON     `json:"total"`
	Days       daysJSON    `json:"days"`
	PerRun     int64       `json:"tokens_per_run"`
	PerSession int64       `json:"tokens_per_session"`
	Models     []shareJSON `json:"models"`
}

type rowJSON struct {
	Key             string `json:"key"`
	Runs            int    `json:"runs"`
	Sessions        int    `json:"sessions"`
	Input           int64  `json:"input"`
	CachedInput     int64  `json:"cached_input"`
	CacheWriteInput int64  `json:"cache_write_input"`
	Output          int64  `json:"output"`
	Reasoning       int64  `json:"reasoning"`
	Total           int64  `json:"total"`
}

type daysJSON struct {
	Days   int    `json:"days"`
	Active int    `json:"active"`
	Mean   int64  `json:"mean"`
	Median int64  `json:"median"`
	P90    int64  `json:"p90"`
	Max    int64  `json:"max"`
	Peak   string `json:"peak_day,omitempty"`
	Streak int    `json:"streak"`
}

type shareJSON struct {
	Model   string  `json:"model"`
	Total   int64   `json:"total"`
	Percent float64 `json:"percent"`
}

func statsJSONFor(r tokstats.Report, by tokstats.By) statsJSON {
	row := func(x tokstats.Row) rowJSON {
		t := x.Tokens

		return rowJSON{
			Key: x.Key, Runs: x.Runs, Sessions: x.Sessions, Input: t.InputTokens, CachedInput: t.CachedInputTokens,
			CacheWriteInput: t.CacheWriteInputTokens, Output: t.OutputTokens, Reasoning: t.ReasoningTokens, Total: x.Total(),
		}
	}
	out := statsJSON{
		First: r.First.Format(time.DateOnly), Last: r.Last.Format(time.DateOnly), By: string(by),
		Rows: []rowJSON{}, Total: row(r.Sum), PerRun: r.PerRun, PerSession: r.PerSession, Models: []shareJSON{},
		Days: daysJSON{Days: r.Days.Days, Active: r.Days.Active, Mean: r.Days.Mean, Median: r.Days.Median, P90: r.Days.P90, Max: r.Days.Max, Peak: r.Days.Peak, Streak: r.Days.Streak},
	}
	for _, x := range r.Rows {
		out.Rows = append(out.Rows, row(x))
	}
	for _, m := range r.Models {
		out.Models = append(out.Models, shareJSON(m))
	}

	return out
}
