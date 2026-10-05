package main

import (
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/cmdparse"
	"github.com/viktordanov/uah/internal/codereview"
	"github.com/viktordanov/uah/internal/tui/render"
)

// What `uah review` prints of a review, as the TUI shows it: on stderr,
// each of the reviewer's steps as it calls a tool, shaped as the TUI's
// tool lines ("→ READ    internal/x.go:1-80"), and the steps that failed;
// on stdout, the REVIEW line, the findings counted by priority with the
// verdict and the confidence, the explanation, and the findings sorted by
// priority and confidence. Colors follow the terminal: a writer that is
// not one, or NO_COLOR, gets plain text.

// step prints one of the reviewer's tool events on stderr: a call when it
// starts, and its end when it failed.
func (o *reviewOutput) step(ev core.Event) {
	switch e := ev.(type) {
	case core.ToolCalled:
		label, text := o.shape(e.Name, e.Arguments, e.Label)
		if o.calls == nil {
			o.calls = map[string][2]string{}
		}
		o.calls[e.CallID] = [2]string{label, text}
		o.progress.say(fmt.Sprintf("  → %-8s%s", label, oneLine(text, 160)))
	case core.ToolFinished:
		call, ok := o.calls[e.CallID]
		delete(o.calls, e.CallID)
		if e.OK || !ok || (call[0] == cmdparse.LabelSearch && e.Detail == "exit 1") {
			return // a search that found nothing did not fail
		}
		o.progress.say(fmt.Sprintf("  ✗ %-8s%s  (%s, %.1fs)", call[0], oneLine(call[1], 160), e.Detail, e.Duration.Seconds()))
	}
}

// shape is a tool call's label and text as the TUI's tool lines read: a
// command as READ, LIST, SEARCH, or RAN and its targets, another tool by
// its name.
func (o *reviewOutput) shape(name, arguments, label string) (string, string) {
	if name != "Bash" {
		return strings.ToUpper(name), label
	}
	var args struct {
		Command string `json:"command"`
	}
	if json.Unmarshal([]byte(arguments), &args) != nil || args.Command == "" {
		args.Command = label
	}
	sum := cmdparse.Summarize(args.Command, o.env)

	return cmp.Or(sum.Label, "RAN"), sum.Text()
}

// reviewStyles are the review's colors on stdout: the TUI's dark theme.
type reviewStyles struct{ dim, bold, accent, bad, warn, ok lipgloss.Style }

func newReviewStyles(t render.Theme) reviewStyles {
	fg := func(c interface{ RGBA() (r, g, b, a uint32) }) lipgloss.Style {
		return lipgloss.NewStyle().Foreground(c)
	}

	return reviewStyles{dim: fg(t.Dim), bold: lipgloss.NewStyle().Bold(true), accent: fg(t.Accent).Bold(true), bad: fg(t.Bad), warn: fg(t.Warn), ok: fg(t.Good)}
}

// print writes the finished review to w: the REVIEW line with what was
// reviewed, the findings by priority, the verdict, the confidence, the
// time, the reviewer, and the tokens; the explanation; then each finding,
// sorted, as "P1  title  82%", its place, and its body indented. A finding
// of low confidence is dim and says so.
func (o *reviewOutput) print(w io.Writer) {
	st := newReviewStyles(render.Amber)
	r := o.finished.Output
	head := []string{st.bold.Render(r.Counts())}
	switch v, correct := r.Verdict(); {
	case correct > 0:
		head = append(head, st.ok.Render(v))
	case correct < 0:
		head = append(head, st.bad.Render(v))
	case v != "":
		head = append(head, v)
	}
	var stats []string
	if c, ok := r.Confidence(); ok {
		stats = append(stats, "confidence "+codereview.Percent(c))
	}
	stats = append(stats, duration(o.finished.At.Sub(o.started.At)))
	if m := strings.TrimSpace(o.started.Model + " " + o.started.Effort); m != "" {
		stats = append(stats, m)
	}
	if t := o.finished.Tokens; t.InputTokens+t.OutputTokens > 0 {
		stats = append(stats, compactTokens(t.InputTokens+t.OutputTokens)+" tokens")
	}
	for i := range stats {
		stats[i] = st.dim.Render(stats[i])
	}
	sep := st.dim.Render(" · ")
	fmt.Fprintln(w, st.accent.Render("REVIEW")+"  "+o.started.Hint)
	fmt.Fprintln(w, "        "+strings.Join(append(head, stats...), sep))
	if e := strings.TrimSpace(r.OverallExplanation); e != "" {
		fmt.Fprintln(w)
		fmt.Fprintln(w, e)
	}
	for _, f := range codereview.Sorted(r.Findings) {
		o.printFinding(w, st, f)
	}
}

// printFinding writes one finding: its priority (P0 and P1 in the bad
// color, P2 in the warning color, P3 dim), title, and confidence, its
// place, and its body indented.
func (o *reviewOutput) printFinding(w io.Writer, st reviewStyles, f codereview.Finding) {
	tag, title, prio := st.dim, st.bold, "  "
	if p, ok := f.Level(); ok {
		prio = fmt.Sprintf("P%d", p)
		switch p {
		case 0, 1:
			tag = st.bad
		case 2:
			tag = st.warn
		}
	}
	low := f.Low()
	if low {
		tag, title = st.dim, st.dim
	}
	line := tag.Render(prio) + "  " + title.Render(f.Heading())
	if c, ok := f.Confidence(); ok {
		score := codereview.Percent(c)
		if low {
			score += " · low confidence"
		}
		line += "  " + st.dim.Render(score)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, line)
	fmt.Fprintln(w, st.dim.Render("    "+f.Place(o.env.Workspace)))
	for l := range strings.Lines(strings.TrimSpace(f.Body)) {
		l = strings.TrimRight(l, "\r\n")
		if l == "" {
			fmt.Fprintln(w)

			continue
		}
		if low {
			l = st.dim.Render(l)
		}
		fmt.Fprintln(w, "    "+l)
	}
}

// duration is "42s", "1m 12s", or "1h 02m".
func duration(d time.Duration) string {
	d = max(d, 0).Round(time.Second)
	switch {
	case d >= time.Hour:
		return fmt.Sprintf("%dh %02dm", int(d.Hours()), int(d.Minutes())%60)
	case d >= time.Minute:
		return fmt.Sprintf("%dm %02ds", int(d.Minutes()), int(d.Seconds())%60)
	}

	return fmt.Sprintf("%ds", int(d.Seconds()))
}

// compactTokens is 834, 12.4k, or 1.2M.
func compactTokens(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	}

	return strconv.FormatInt(n, 10)
}
