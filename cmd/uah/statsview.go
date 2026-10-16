package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/viktordanov/uah/internal/tui/state"
	"github.com/viktordanov/uah/internal/usage/tokstats"
)

// The heatmap's weeks: by default, and at most (a year, 2 columns each).
const (
	heatmapWeeks    = 26
	maxHeatmapWeeks = 52
)

// barWidth is the width of the longest row's bar, in cells.
const barWidth = 12

// statsView is what `uah stats` prints: a headline, the rows (or a
// heatmap), and a summary.
type statsView struct {
	now  time.Time
	by   tokstats.By
	wide bool
	// workspace is -C's directory ("": every workspace).
	workspace string
	// label names the shown range when it has a name ("last 7 days").
	label string
	// shown has the rows; prev is the range of the same length before it,
	// to compare with; allTime is all time, under the last 7 days; stats
	// is the range the summary describes (allTime, else shown).
	shown, stats  tokstats.Report
	prev, allTime *tokstats.Report
	// places are the stats range's workspaces, for the summary.
	places []tokstats.Row
	// heat are the days of the heatmap, when --heatmap asked for one.
	heat      []tokstats.Row
	heatWeeks int
	heatEnd   time.Time
	// span is a comparison with the span of time before, not whole days.
	span bool
}

func (v statsView) print(w io.Writer) error {
	out := colorOut(w)
	v.headline(out)
	fmt.Fprintln(out)
	if v.heatWeeks > 0 {
		v.heatmap(out)
	} else if err := v.table(w); err != nil {
		return err
	}
	fmt.Fprintln(out)
	v.summary(out)

	return nil
}

// headline is the range, its sum in one line, and how it compares with
// the range before it and its busiest day.
func (v statsView) headline(out io.Writer) {
	st := cliStyles
	title := "uah stats · " + v.rangeText(v.shown)
	if v.label != "" {
		title = "uah stats · " + v.label + ", " + v.rangeText(v.shown)
	}
	if v.workspace != "" {
		title += " · " + homeShort(v.workspace)
	}
	fmt.Fprintln(out, st.bold.Render(title))
	s := v.shown.Sum
	if s.Runs == 0 && s.Total() == 0 {
		fmt.Fprintln(out, "\n  "+st.dim.Render("no tokens in this range"))

		return
	}
	fmt.Fprintf(out, "\n  %s tokens · %s · %s · %s from the cache\n", st.bold.Render(compactTokens(s.Total())),
		count(s.Runs, "run"), count(s.Sessions, "session"), hitRate(s))
	var notes []string
	if c := v.comparison(); c != "" {
		notes = append(notes, c)
	}
	if d := v.shown.Days; d.Peak != "" && d.Active > 1 {
		notes = append(notes, "busiest "+v.dayText(d.Peak)+", "+compactTokens(d.Max))
	}
	if len(notes) > 0 {
		fmt.Fprintln(out, "  "+st.dim.Render(strings.Join(notes, " · ")))
	}
}

// comparison says how the shown range compares with the one before it.
func (v statsView) comparison() string {
	if v.prev == nil || v.prev.Sum.Total() == 0 {
		return ""
	}
	cur, prev := float64(v.shown.Sum.Total()), float64(v.prev.Sum.Total())
	period := "the " + plural(v.shown.Days.Days, "day") + " before"
	switch {
	case v.span:
		period = "the same span before"
	case v.shown.Days.Days == 1:
		period = "the day before"
	}
	was := " (" + compactTokens(v.prev.Sum.Total()) + ")"
	switch ratio := cur / prev; {
	case ratio >= 1.05:
		return fmt.Sprintf("%.1f× %s", ratio, period) + was
	case ratio <= 0.95:
		return fmt.Sprintf("%.0f%% less than %s", 100*(1-ratio), period) + was
	}

	return "about the same as " + period + was
}

// table is the rows: the group, runs, sessions, total, cache share, and a
// bar for the total; --wide adds the token buckets.
func (v statsView) table(w io.Writer) error {
	var t table
	t.capColumn(0, 40) // a long workspace runs past it in its own row
	head := []string{strings.ToUpper(string(v.by)), "RUNS", "SESSIONS"}
	if v.wide {
		head = append(head, "INPUT", "CACHED", "OUTPUT", "REASONING")
	}
	head = append(head, "TOTAL", "CACHE", "", "")
	t.header(head...)
	numbers := []int{1, 2}
	for i := range len(head) - 6 {
		numbers = append(numbers, 3+i)
	}
	t.alignRight(append(numbers, len(head)-4, len(head)-3)...)
	var peak int64
	for _, r := range v.shown.Rows {
		peak = max(peak, r.Total())
	}
	for _, r := range v.shown.Rows {
		cells := []string{v.keyText(r.Key), commas(int64(r.Runs)), commas(int64(r.Sessions))}
		if v.wide {
			t := r.Tokens
			cells = append(cells, compactTokens(t.InputTokens), compactTokens(t.CachedInputTokens), compactTokens(t.OutputTokens), compactTokens(t.ReasoningTokens))
		}
		cells = append(cells, compactTokens(r.Total()), hitRate(r), styled(cliStyles.ok, bar(r.Total(), peak)), styled(cliStyles.dim, v.marker(r.Key)))
		var style *lipgloss.Style
		if r.Runs == 0 && r.Total() == 0 {
			style = &cliStyles.dim
		}
		t.row(style, cells...)
	}

	return t.write(w)
}

// heatmap is the tokens per day of the last weeks, a column a week, with
// the months above and a legend under.
func (v statsView) heatmap(out io.Writer) {
	values := map[string]int64{}
	for _, r := range v.heat {
		values[r.Key] = r.Total()
	}
	paint := func(level int, cell string) string {
		if level == 0 {
			return " " + cliStyles.dim.Render(cell)
		}

		return " " + cliStyles.ok.Render(cell)
	}
	rows, start := state.HeatGrid(values, v.heatEnd, v.heatWeeks, paint)
	months := []rune(strings.Repeat(" ", 4+2*v.heatWeeks+4))
	free := 0
	for week := range v.heatWeeks {
		d := start.AddDate(0, 0, 7*week)
		if week > 0 && d.Month() == d.AddDate(0, 0, -7).Month() {
			continue
		}
		at := 4 + 2*week
		if at < free {
			continue
		}
		copy(months[at:], []rune(d.Format("Jan")))
		free = at + 4
	}
	fmt.Fprintln(out, cliStyles.dim.Render(strings.TrimRight(string(months), " ")))
	for i, label := range []string{"Mon", "", "Wed", "", "Fri", "", "Sun"} {
		fmt.Fprintf(out, "%s%s\n", cliStyles.dim.Render(fmt.Sprintf("%-3s", label)), rows[i])
	}
	fmt.Fprintln(out, "   "+cliStyles.dim.Render(" less")+paint(0, "·")+paint(1, "░")+paint(2, "▒")+paint(3, "▓")+paint(4, "█")+" "+cliStyles.dim.Render("more"))
}

// summary describes the stats range: all time under the last 7 days, a
// day's tokens, the streak, the averages, the models, and the workspaces.
func (v statsView) summary(out io.Writer) {
	r := v.stats
	line := func(label, text string) {
		if text != "" {
			fmt.Fprintf(out, "  %s  %s\n", cliStyles.bold.Render(fmt.Sprintf("%-10s", label)), text)
		}
	}
	if a := v.allTime; a != nil {
		line("all time", fmt.Sprintf("%s since %s · %s · %s", compactTokens(a.Sum.Total()), v.dateText(a.First), count(a.Sum.Runs, "run"), count(a.Sum.Sessions, "session")))
	}
	d := r.Days
	if d.Active > 0 {
		line("per day", fmt.Sprintf("median %s · mean %s · p90 %s, of %s", compactTokens(d.Median), compactTokens(d.Mean), compactTokens(d.P90), plural(d.Active, "active day")))
		line("streak", fmt.Sprintf("%s · %d of %d days active", plural(d.Streak, "day"), d.Active, d.Days))
		line("average", fmt.Sprintf("%s a run · %s a session", compactTokens(r.PerRun), compactTokens(r.PerSession)))
	}
	var models []share
	for _, m := range r.Models {
		models = append(models, share{m.Model, m.Total})
	}
	line("models", topShares(models, r.Sum.Total(), 3))
	line("workspaces", topShares(workspaceShares(v.places), r.Sum.Total(), 3))
	if v.wide {
		fmt.Fprintln(out, "\n"+cliStyles.dim.Render("  total is input not served from the cache plus output; cached is part of input, reasoning part of output"))
	}
}

type share struct {
	name  string
	total int64
}

// topShares names the n largest shares with their percent of total, the
// rest as others.
func topShares(shares []share, total int64, n int) string {
	if total <= 0 || len(shares) == 0 {
		return ""
	}
	var parts []string
	var rest int64
	others := 0
	for i, s := range shares {
		if i >= n || 100*s.total < total { // under 1%: noise here; --by lists it
			rest += s.total
			others++

			continue
		}
		parts = append(parts, fmt.Sprintf("%s %s", s.name, percent(s.total, total)))
	}
	if others > 0 {
		parts = append(parts, fmt.Sprintf("%s %s", plural(others, "other"), percent(rest, total)))
	}

	return strings.Join(parts, " · ")
}

func percent(part, total int64) string {
	p := 100 * float64(part) / float64(total)
	if p < 1 {
		return fmt.Sprintf("%.1f%%", p)
	}

	return fmt.Sprintf("%.0f%%", p)
}

// workspaceShares names the workspaces by their last element (the path
// when two share it) and puts the temporary ones together, largest first.
func workspaceShares(rows []tokstats.Row) []share {
	bases := map[string]int{}
	for _, r := range rows {
		if !temporary(r.Key) {
			bases[filepath.Base(r.Key)]++
		}
	}
	var out []share
	var temp int64
	for _, r := range rows {
		switch {
		case temporary(r.Key):
			temp += r.Total()
		case bases[filepath.Base(r.Key)] > 1:
			out = append(out, share{homeShort(r.Key), r.Total()})
		default:
			out = append(out, share{filepath.Base(r.Key), r.Total()})
		}
	}
	if temp > 0 {
		at := len(out)
		for i, s := range out {
			if s.total < temp {
				at = i

				break
			}
		}
		out = append(out[:at], append([]share{{"temporary", temp}}, out[at:]...)...)
	}

	return out
}

// temporary reports whether a workspace is a temporary directory, such as
// a benchmark's.
func temporary(dir string) bool {
	if dir == "" || dir == "(unknown)" {
		return false
	}
	for _, tmp := range []string{os.TempDir(), "/tmp", "/private/tmp", "/var/folders", "/private/var/folders"} {
		tmp = strings.TrimSuffix(tmp, "/")
		if dir == tmp || strings.HasPrefix(dir, tmp+"/") {
			return true
		}
	}

	return false
}

// keyText is a row's key for people: a date with its weekday, a week's
// first and last days, a month's name, a workspace from ~.
func (v statsView) keyText(key string) string {
	switch v.by {
	case tokstats.ByDay:
		return v.dayText(key)
	case tokstats.ByWeek:
		monday := weekStart(key, v.now.Location())
		if monday.IsZero() {
			return key
		}

		return v.spanText(monday, monday.AddDate(0, 0, 6))
	case tokstats.ByMonth:
		if t, err := time.Parse("2006-01", key); err == nil {
			return t.Format("January 2006")
		}
	case tokstats.ByWorkspace:
		return homeShort(key)
	}

	return key
}

// marker points at the row of now: today, this week, this month.
func (v statsView) marker(key string) string {
	switch {
	case v.by == tokstats.ByDay && key == v.now.Format(time.DateOnly):
		return "today"
	case v.by == tokstats.ByWeek && key == weekOf(v.now):
		return "this week"
	case v.by == tokstats.ByMonth && key == v.now.Format("2006-01"):
		return "this month"
	}

	return ""
}

func weekOf(t time.Time) string {
	y, w := t.ISOWeek()

	return fmt.Sprintf("%d-W%02d", y, w)
}

// weekStart is the Monday of an ISO week key (2026-W42), at noon.
func weekStart(key string, loc *time.Location) time.Time {
	var y, w int
	if _, err := fmt.Sscanf(key, "%d-W%d", &y, &w); err != nil {
		return time.Time{}
	}
	jan4 := time.Date(y, time.January, 4, 12, 0, 0, 0, loc) // in week 1
	monday := jan4.AddDate(0, 0, -((int(jan4.Weekday()) + 6) % 7))

	return monday.AddDate(0, 0, 7*(w-1))
}

// dayText is a date as "Sun Oct 12", with the year when it is not this
// year's.
func (v statsView) dayText(key string) string {
	t, err := time.Parse(time.DateOnly, key) // a calendar date: a local midnight may not exist
	if err != nil {
		return key
	}
	if t.Year() != v.now.Year() {
		return t.Format("Mon Jan 2 2006")
	}

	return t.Format("Mon Jan 2")
}

// dateText is a date as "Sep 23", with the year when it is not this year's.
func (v statsView) dateText(t time.Time) string {
	if t.Year() != v.now.Year() {
		return t.Format("Jan 2 2006")
	}

	return t.Format("Jan 2")
}

// spanText is a range of days: "Oct 10 – 16", "Sep 23 – Oct 16".
func (v statsView) spanText(first, last time.Time) string {
	switch {
	case first.Format(time.DateOnly) == last.Format(time.DateOnly):
		return v.dateText(first)
	case first.Year() != last.Year():
		return first.Format("Jan 2 2006") + " – " + last.Format("Jan 2 2006")
	case first.Month() == last.Month():
		return v.dateText(first) + " – " + strconv.Itoa(last.Day())
	}

	return v.dateText(first) + " – " + v.dateText(last)
}

func (v statsView) rangeText(r tokstats.Report) string { return v.spanText(r.First, r.Last) }

// bar is a total's bar against the largest, in eighths of a cell.
func bar(total, peak int64) string {
	if total <= 0 || peak <= 0 {
		return ""
	}
	eighths := max(int(total*barWidth*8/peak), 1)
	partial := []string{"", "▏", "▎", "▍", "▌", "▋", "▊", "▉"}

	return strings.Repeat("█", eighths/8) + partial[eighths%8]
}

func hitRate(r tokstats.Row) string {
	if r.Tokens.InputTokens <= 0 {
		return "-"
	}

	return fmt.Sprintf("%.0f%%", 100*float64(r.Tokens.CachedInputTokens)/float64(r.Tokens.InputTokens))
}

// count is "1,610 runs", "1 run".
func count(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}

	return commas(int64(n)) + " " + word + "s"
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}

	return strconv.Itoa(n) + " " + word + "s"
}

// styled is text in st, and leaves "" without styles, so an empty cell ends a
// line with nothing.
func styled(st lipgloss.Style, text string) string {
	if text == "" {
		return ""
	}

	return st.Render(text)
}
