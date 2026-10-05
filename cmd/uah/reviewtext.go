package main

import (
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/cmdparse"
	"github.com/viktordanov/uah/internal/codereview"
	"github.com/viktordanov/uah/internal/engine"
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
// starts, and its end when it failed, with why. A failed command's end and
// its output (engine.ToolOutput, for every command that exits nonzero)
// come in either order, so a failure is printed once both are in, or when
// the review ends; a search that exits 1 with no output found nothing, as
// the TUI reads it, and is not a failure.
func (o *reviewOutput) step(ev core.Event) {
	if o.calls == nil {
		o.calls = map[string]*reviewCall{}
	}
	switch e := ev.(type) {
	case core.ToolCalled:
		label, text := o.shape(e.Name, e.Arguments, e.Label)
		o.seq++
		o.calls[e.CallID] = &reviewCall{seq: o.seq, label: label, text: text}
		o.progress.say(fmt.Sprintf("  → %-8s%s", label, oneLine(text, 160)))
	case engine.ToolOutput:
		if c, ok := o.calls[e.CallID]; ok {
			c.why, c.output = cmp.Or(lastLine(e.Error), lastLine(e.Output)), true
			if c.done != nil {
				o.settle(e.CallID, c)
			}
		}
	case core.ToolFinished:
		c, ok := o.calls[e.CallID]
		switch {
		case !ok:
		case e.OK:
			delete(o.calls, e.CallID)
		default:
			c.done = &e
			if c.output {
				o.settle(e.CallID, c)
			}
		}
	}
}

// flush settles the failed calls whose output never came, in the order
// they were made, when the review ends.
func (o *reviewOutput) flush() {
	ids := slices.SortedFunc(maps.Keys(o.calls), func(a, b string) int { return cmp.Compare(o.calls[a].seq, o.calls[b].seq) })
	for _, id := range ids {
		if c := o.calls[id]; c.done != nil {
			o.settle(id, c)
		}
	}
}

// settle prints a call that failed, unless it is a search that found
// nothing, and forgets it.
func (o *reviewOutput) settle(id string, c *reviewCall) {
	delete(o.calls, id)
	if c.label == cmdparse.LabelSearch && c.done.Detail == "exit 1" && c.why == "" {
		return
	}
	why := ""
	if c.why != "" {
		why = ": " + oneLine(c.why, 160)
	}
	o.progress.say(fmt.Sprintf("  ✗ %-8s%s  (%s, %.1fs)%s", c.label, oneLine(c.text, 160), c.done.Detail, c.done.Duration.Seconds(), why))
}

// reviewCall is one of the reviewer's calls: the order it was made in, its
// label and text, why it failed and whether its output came, and its end
// once it failed.
type reviewCall struct {
	seq              int
	label, text, why string
	output           bool
	done             *core.ToolFinished
}

// lastLine is the last line of text with a letter or a digit.
func lastLine(text string) string {
	lines := strings.Split(ansi.Strip(text), "\n")
	for _, l := range slices.Backward(lines) {
		if l = strings.TrimSpace(l); strings.ContainsFunc(l, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) {
			return l
		}
	}

	return ""
}

// command is a Bash call's command, from its arguments, else its label.
func command(arguments, label string) string {
	var args struct {
		Command string `json:"command"`
	}
	if json.Unmarshal([]byte(arguments), &args) != nil || args.Command == "" {
		return label
	}

	return args.Command
}

// reviewWriter writes in the terminal's colors: none on a writer that is
// not a terminal, and none, not even bold, when NO_COLOR is set to
// anything.
func reviewWriter(w io.Writer, environ []string) *colorprofile.Writer {
	out := colorprofile.NewWriter(w, environ)
	for _, kv := range environ {
		if v, ok := strings.CutPrefix(kv, "NO_COLOR="); ok && v != "" {
			out.Profile = colorprofile.NoTTY
		}
	}

	return out
}

// shape is a tool call's label and text as the TUI's tool lines read: a
// command as READ, LIST, SEARCH, or RAN and its targets, another tool by
// its name.
func (o *reviewOutput) shape(name, arguments, label string) (string, string) {
	if name != "Bash" {
		return strings.ToUpper(name), label
	}
	sum := cmdparse.Summarize(command(arguments, label), o.env)

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
	} else if len(r.Findings) == 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, st.dim.Render(codereview.FallbackMessage))
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
