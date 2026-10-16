package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/urfave/cli/v3"

	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/internal/usage/tokstats"
)

const (
	flagBy    = "by"
	flagUntil = "until"
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
			"unless -C names one. Without --by, --since, or --until it shows the last 7 days by day\n" +
			"and all time under them. Days are local; weeks start on Monday. total is input not\n" +
			"served from the cache plus output (cached is part of input, reasoning part of output).\n" +
			"A run counts on the day it started.",
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
	if cmd.IsSet(flagWorkspace) {
		dir, err := currentDir(cmd)
		if err != nil {
			return err
		}
		o.Workspace = session.NormalDir(dir)
	}
	full := tokstats.Build(entries, o)
	if cmd.Bool(flagJSON) {
		return writeJSON(os.Stdout, statsJSONFor(full, o.By))
	}
	shown, allTime := full, (*tokstats.Report)(nil)
	if !cmd.IsSet(flagBy) && !cmd.IsSet(flagSince) && !cmd.IsSet(flagUntil) {
		week := o
		week.Since = startOfDay(now.Year(), now.Month(), now.Day()-6, now.Location())
		shown, allTime = tokstats.Build(entries, week), &full
	}

	return printStats(os.Stdout, shown, allTime, o.By)
}

// printStats writes the rows and their sum, all time's sum under them when
// the rows are the last 7 days, and the statistics of the range.
func printStats(w io.Writer, r tokstats.Report, allTime *tokstats.Report, by tokstats.By) error {
	var t table
	t.capColumn(0, 40) // a long workspace runs past it in its own row
	t.header(strings.ToUpper(string(by)), "RUNS", "SESSIONS", "INPUT", "CACHED", "OUTPUT", "REASONING", "TOTAL", "CACHE HIT")
	for _, row := range r.Rows {
		var style *lipgloss.Style
		if row.Runs == 0 && row.Total() == 0 {
			style = &cliStyles.dim
		}
		t.row(style, statsCells(row, homeShort)...)
	}
	sum := statsCells(r.Sum, homeShort)
	sum[0] = rangeLabel(r)
	t.row(&cliStyles.bold, sum...)
	if allTime != nil {
		all := statsCells(allTime.Sum, homeShort)
		all[0] = "all time"
		t.row(&cliStyles.bold, all...)
		r = *allTime
	}
	if err := t.write(w); err != nil {
		return err
	}
	out := colorOut(w)
	d := r.Days
	fmt.Fprintln(out)
	fmt.Fprintf(out, "%s %s: %d of %d days active, a streak of %s\n", cliStyles.bold.Render("days"), rangeLabel(r), d.Active, d.Days, plural(d.Streak, "day"))
	if d.Active > 0 {
		fmt.Fprintf(out, "%s mean %s · median %s · p90 %s · max %s on %s\n", cliStyles.bold.Render("per active day"),
			compactTokens(d.Mean), compactTokens(d.Median), compactTokens(d.P90), compactTokens(d.Max), d.Peak)
		fmt.Fprintf(out, "%s %s per run · %s per session\n", cliStyles.bold.Render("average"), compactTokens(r.PerRun), compactTokens(r.PerSession))
	}
	if len(r.Models) > 0 {
		var parts []string
		var rest float64
		others := 0
		for _, m := range r.Models {
			if m.Percent < 1 && len(parts) > 0 { // a model under 1% is noise here; --by model lists it
				rest += m.Percent
				others++

				continue
			}
			parts = append(parts, fmt.Sprintf("%s %.0f%%", m.Model, m.Percent))
		}
		if others > 0 {
			parts = append(parts, fmt.Sprintf("%s %.1f%%", plural(others, "other"), rest))
		}
		fmt.Fprintf(out, "%s %s\n", cliStyles.bold.Render("models"), strings.Join(parts, " · "))
	}

	return nil
}

func statsCells(r tokstats.Row, short func(string) string) []string {
	hit := "-"
	if r.Tokens.InputTokens > 0 {
		hit = fmt.Sprintf("%.0f%%", 100*float64(r.Tokens.CachedInputTokens)/float64(r.Tokens.InputTokens))
	}

	return []string{
		short(r.Key), strconv.Itoa(r.Runs), strconv.Itoa(r.Sessions),
		compactTokens(r.Tokens.InputTokens), compactTokens(r.Tokens.CachedInputTokens), compactTokens(r.Tokens.OutputTokens),
		compactTokens(r.Tokens.ReasoningTokens), compactTokens(r.Total()), hit,
	}
}

func rangeLabel(r tokstats.Report) string {
	first, last := r.First.Format(time.DateOnly), r.Last.Format(time.DateOnly)
	if first == last {
		return first
	}

	return first + " – " + last
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}

	return strconv.Itoa(n) + " " + word + "s"
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
