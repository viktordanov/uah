// Package tokstats adds up the tokens of every session in a state
// directory by day, week, month, model, workspace, or source, with
// descriptive statistics of the days (`uah stats`,
// docs/design/token-stats.md).
package tokstats

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/viktordanov/uagent/core"
)

// The sources an Entry comes from: a session's (the TUI, uah exec, a
// subagent, a /review's reviewer), or a compaction's summary call.
const (
	SourceTUI        = "tui"
	SourceExec       = "exec"
	SourceSubagent   = "subagent"
	SourceReview     = "review"
	SourceCompaction = "compaction"
	SourceUnknown    = "unknown"
)

// Entry is one run's tokens, or one compaction call's (Run false), at the
// time it started.
type Entry struct {
	At                                time.Time
	Session, Model, Workspace, Source string
	Tokens                            core.Tokens
	Run                               bool
}

// By is how rows are grouped.
type By string

// The groupings.
const (
	ByDay       By = "day"
	ByWeek      By = "week"
	ByMonth     By = "month"
	ByModel     By = "model"
	ByWorkspace By = "workspace"
	BySource    By = "source"
)

// Bys are the groupings, as --by takes them.
var Bys = []By{ByDay, ByWeek, ByMonth, ByModel, ByWorkspace, BySource}

func (b By) timed() bool { return b == ByDay || b == ByWeek || b == ByMonth }

// Options choose the entries and the rows.
type Options struct {
	By By
	// Since and Until bound the entries, Until excluded; zero is
	// unbounded.
	Since, Until time.Time
	// Now is today, in the location whose days count.
	Now time.Time
	// Workspace keeps one workspace's entries, a NormalDir ("": all);
	// Normal makes an entry's comparable, for the filter and for rows by
	// workspace.
	Workspace string
	Normal    func(string) string
}

// Row is a group's sum: the runs, the sessions with any entry, and the
// tokens.
type Row struct {
	Key            string
	Runs, Sessions int
	Tokens         core.Tokens
}

// Total is Codex's total, which the exit summary prints: the input not
// served from the cache, and the output. Cached input is part of input,
// and reasoning part of output.
func Total(t core.Tokens) int64 { return t.InputTokens - t.CachedInputTokens + t.OutputTokens }

// Total is the row's Total.
func (r Row) Total() int64 { return Total(r.Tokens) }

// Days are descriptive statistics of the range's days: of the days with
// any entry, their totals' mean, median, 90th percentile, and maximum (on
// Peak); Streak, the active days in a row up to today (or yesterday, while
// today has none).
type Days struct {
	Days, Active      int
	Mean, Median, P90 int64
	Max               int64
	Peak              string
	Streak            int
}

// Share is a model's part of the total.
type Share struct {
	Model   string
	Total   int64
	Percent float64
}

// Report is the range's rows, its sum, and its statistics.
type Report struct {
	// First and Last are the range's first and last days, at noon.
	First, Last time.Time
	Rows        []Row
	Sum         Row
	Days        Days
	// PerRun and PerSession are the sum's Total per run and per session.
	PerRun, PerSession int64
	Models             []Share
}

// Build adds the entries up. The range runs from Since, or the first
// entry's day, to Until, or today; with rows by time, every day, week, or
// month in it has a row, an empty one too.
func Build(entries []Entry, o Options) Report {
	loc := o.Now.Location()
	kept := filter(entries, o)
	last := day(o.Now)
	if !o.Until.IsZero() {
		last = day(o.Until.Add(-time.Nanosecond).In(loc))
	}
	first := last // no entries and no Since: the range's last day alone
	switch {
	case !o.Since.IsZero():
		first = day(o.Since.In(loc))
	case len(kept) > 0:
		first = day(slices.MinFunc(kept, func(a, b Entry) int { return a.At.Compare(b.At) }).At.In(loc))
	}
	if last.Before(first) {
		last = first
	}
	r := Report{First: first, Last: last, Sum: sum("total", kept)}
	r.Rows = rows(kept, o.By, first, last)
	r.Days = days(kept, first, last, day(o.Now))
	if r.Sum.Runs > 0 {
		r.PerRun = r.Sum.Total() / int64(r.Sum.Runs)
	}
	if r.Sum.Sessions > 0 {
		r.PerSession = r.Sum.Total() / int64(r.Sum.Sessions)
	}
	r.Models = shares(kept, r.Sum.Total())

	return r
}

// filter keeps the range's entries, and the workspace's with one, with
// each workspace made comparable, so rows by workspace group as the
// workspace filter matches.
func filter(entries []Entry, o Options) []Entry {
	var out []Entry
	normal := map[string]string{}
	for _, e := range entries {
		if !o.Since.IsZero() && e.At.Before(o.Since) || !o.Until.IsZero() && !e.At.Before(o.Until) {
			continue
		}
		if o.Normal != nil && e.Workspace != "" {
			n, ok := normal[e.Workspace]
			if !ok {
				n = o.Normal(e.Workspace)
				normal[e.Workspace] = n
			}
			e.Workspace = n
		}
		if o.Workspace != "" && e.Workspace != o.Workspace {
			continue
		}
		out = append(out, e)
	}

	return out
}

// sum adds entries into a row named key.
func sum(key string, entries []Entry) Row {
	r := Row{Key: key}
	sessions := map[string]bool{}
	for _, e := range entries {
		r.Tokens = r.Tokens.Add(e.Tokens)
		if e.Run {
			r.Runs++
		}
		sessions[e.Session] = true
	}
	r.Sessions = len(sessions)

	return r
}

func rows(entries []Entry, by By, first, last time.Time) []Row {
	groups := map[string][]Entry{}
	for _, e := range entries {
		k := key(e, by, first.Location())
		groups[k] = append(groups[k], e)
	}
	if by.timed() {
		var out []Row
		for _, k := range timeKeys(by, first, last) {
			out = append(out, sum(k, groups[k]))
		}

		return out
	}
	out := make([]Row, 0, len(groups))
	for _, k := range slices.Sorted(maps.Keys(groups)) {
		out = append(out, sum(k, groups[k]))
	}
	slices.SortStableFunc(out, func(a, b Row) int { return cmp.Compare(b.Total(), a.Total()) })

	return out
}

func key(e Entry, by By, loc *time.Location) string {
	at := e.At.In(loc)
	switch by {
	case ByDay:
		return at.Format(time.DateOnly)
	case ByWeek:
		return weekKey(at)
	case ByMonth:
		return at.Format("2006-01")
	case ByModel:
		return cmp.Or(e.Model, "(unknown)")
	case ByWorkspace:
		return cmp.Or(e.Workspace, "(unknown)")
	}

	return cmp.Or(e.Source, SourceUnknown)
}

func weekKey(t time.Time) string {
	y, w := t.ISOWeek()

	return fmt.Sprintf("%d-W%02d", y, w)
}

// timeKeys are the keys of every day, week, or month from first to last.
func timeKeys(by By, first, last time.Time) []string {
	var keys []string
	for d := first; !d.After(last); d = d.AddDate(0, 0, 1) {
		k := key(Entry{At: d}, by, d.Location())
		if len(keys) == 0 || keys[len(keys)-1] != k {
			keys = append(keys, k)
		}
	}

	return keys
}

func days(entries []Entry, first, last, today time.Time) Days {
	totals := map[string]int64{}
	active := map[string]bool{}
	for _, e := range entries {
		k := e.At.In(first.Location()).Format(time.DateOnly)
		totals[k] += Total(e.Tokens)
		active[k] = true
	}
	var d Days
	var values []int64
	for t := first; !t.After(last); t = t.AddDate(0, 0, 1) {
		d.Days++
		k := t.Format(time.DateOnly)
		if !active[k] {
			continue
		}
		values = append(values, totals[k])
		if totals[k] > d.Max || d.Peak == "" {
			d.Max, d.Peak = totals[k], k
		}
	}
	d.Active = len(values)
	if d.Active > 0 {
		var all int64
		for _, v := range values {
			all += v
		}
		d.Mean = all / int64(d.Active)
		slices.Sort(values)
		d.Median = median(values)
		d.P90 = values[(len(values)*9+9)/10-1] // nearest rank
	}
	t := today
	if !active[t.Format(time.DateOnly)] {
		t = t.AddDate(0, 0, -1)
	}
	for ; active[t.Format(time.DateOnly)]; t = t.AddDate(0, 0, -1) {
		d.Streak++
	}

	return d
}

func median(sorted []int64) int64 {
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}

	return (sorted[n/2-1] + sorted[n/2]) / 2
}

func shares(entries []Entry, total int64) []Share {
	byModel := map[string]int64{}
	for _, e := range entries {
		byModel[cmp.Or(e.Model, "(unknown)")] += Total(e.Tokens)
	}
	out := make([]Share, 0, len(byModel))
	for _, m := range slices.Sorted(maps.Keys(byModel)) {
		s := Share{Model: m, Total: byModel[m]}
		if total > 0 {
			s.Percent = 100 * float64(s.Total) / float64(total)
		}
		out = append(out, s)
	}
	slices.SortStableFunc(out, func(a, b Share) int { return cmp.Compare(b.Total, a.Total) })

	return out
}

// day is t's date, at local noon: days are stepped from it with AddDate,
// and where a clock change skips midnight, midnight would normalize into
// the day before, which noon never does.
func day(t time.Time) time.Time {
	y, m, d := t.Date()

	return time.Date(y, m, d, 12, 0, 0, 0, t.Location())
}
