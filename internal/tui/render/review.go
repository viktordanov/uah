package render

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/viktordanov/uah/internal/codereview"
	"github.com/viktordanov/uah/internal/gitdiff"
	"github.com/viktordanov/uah/internal/patch"
	"github.com/viktordanov/uah/internal/tui/state"
)

// /diff draws as the edit tool's diffs do, under a DIFF line with the
// totals; /review as a REVIEW line with the reviewer's latest steps while
// it works, then the verdict, its explanation, and each finding, sorted by
// priority and confidence: its priority, title, confidence, place, and
// body.

// gitDiffLines draws /diff's changes: every file with its counts and its
// lines, or its note, and the untracked files past the limit.
func (st *Styles) gitDiffLines(d *gitdiff.Diff, w int) []string {
	files := fmt.Sprintf("%d files ", len(d.Files))
	if len(d.Files) == 1 {
		files = "1 file "
	}
	out := []string{"", st.accent.Render("  DIFF   ") + st.dim.Render(files) + st.counts(d.Added(), d.Removed())}
	for _, f := range d.Files {
		head := st.dim.Render("  └ ") + diffPath(f.FileDiff) + " "
		switch {
		case f.Note != "":
			out = append(out, ansi.Truncate(head+st.dim.Render(f.Note+", not shown"), w, "…"))

			continue
		case f.Untracked:
			head += st.dim.Render("untracked ")
		case f.Op == "add":
			head += st.dim.Render("added ")
		case f.Op == "delete":
			head += st.dim.Render("deleted ")
		}
		out = append(out, ansi.Truncate(head+st.counts(f.Added, f.Removed), w, "…"))
		out = append(out, st.diffBlock([]patch.FileDiff{f.FileDiff}, w, 0)...)
	}
	if d.MoreUntracked > 0 {
		out = append(out, st.dim.Render(fmt.Sprintf("  └ %d more untracked files, not shown", d.MoreUntracked)))
	}

	return out
}

// reviewLines draws a /review. While it runs: the REVIEW line (what it
// looks at, the reviewer, its steps, tokens, and time) and a tree of the
// reviewer's latest steps, the live one with the spinner. Once done: the
// REVIEW line, the verdict line under it (the findings counted by
// priority, the verdict, the confidence, the time, the reviewer, the
// tokens), the explanation, and the findings sorted by priority and
// confidence. The detailed view adds every step and the whole bodies.
func (st *Styles) reviewLines(it state.Item, w int, now time.Time, details bool) []string {
	r := it.Review
	if r == nil {
		return nil
	}
	var meta []string
	if m := strings.TrimSpace(r.Model + " " + r.Effort); m != "" {
		meta = append(meta, m)
	}
	used := ""
	if n := r.Tokens.InputTokens + r.Tokens.OutputTokens; n > 0 {
		used = tokens(n) + " tokens"
	}
	if r.Running {
		if r.StepCount > 0 {
			meta = append(meta, plural(r.StepCount, "step"))
		}
		meta = append(meta, used, elapsed(now.Sub(r.Started)))
		out := append([]string{""}, st.flow(st.accent.Render("  REVIEW  ")+r.Hint, st.dim.Render("  · "), styleLines(compact(meta), st.dim), reviewHang, w)...)

		return append(out, st.reviewSteps(r, w, now, details)...)
	}
	out := []string{"", ansi.Truncate(st.dim.Render("  REVIEW  ")+r.Hint, w, "…")}
	stats := styleLines(compact(append([]string{elapsed(r.Ended.Sub(r.Started))}, append(meta, used)...)), st.dim)
	switch {
	case r.Err != "":
		out = append(out, st.verdictLines([]string{st.bad.Render("✗ failed: " + oneLine(r.Err))}, stats, w)...)
	case r.Interrupted:
		out = append(out, st.verdictLines([]string{st.warn.Render("■ interrupted")}, stats, w)...)
	default:
		o := r.Output
		head := []string{st.bold.Render(o.Counts())}
		switch v, correct := o.Verdict(); {
		case correct > 0:
			head = append(head, st.ok.Render(v))
		case correct < 0:
			head = append(head, st.bad.Render(v))
		case v != "":
			head = append(head, st.dim.Render(v))
		}
		if c, ok := o.Confidence(); ok {
			stats = append([]string{st.dim.Render("confidence " + codereview.Percent(c))}, stats...)
		}
		out = append(out, st.verdictLines(head, stats, w)...)
	}
	if r.Err == "" && !r.Interrupted {
		if e := strings.TrimSpace(r.Output.OverallExplanation); e != "" {
			out = append(out, "")
			out = append(out, st.markdownLines(e, w, reviewIndent, reviewIndent)...)
		} else if len(r.Output.Findings) == 0 {
			out = append(out, "", st.dim.Render(reviewIndent+codereview.FallbackMessage))
		}
	}
	if details && r.StepCount > 0 {
		out = append(out, "", st.dim.Render(reviewIndent+"steps ("+strconv.Itoa(r.StepCount)+")"))
		out = append(out, st.reviewSteps(r, w, now, true)...)
	}
	for _, f := range codereview.Sorted(r.Output.Findings) {
		out = append(out, st.findingLines(f, r.Workspace, w, details)...)
	}

	return out
}

// compact drops the empty parts.
func compact(parts []string) []string {
	return slices.DeleteFunc(slices.Clone(parts), func(p string) bool { return p == "" })
}

// verdictLines are the lines under a finished review's REVIEW line, from
// the hint's column: what it found and the verdict, then the confidence,
// the time, the reviewer, and the tokens; on one line when it fits, else
// broken after the verdict.
func (st *Styles) verdictLines(head, stats []string, w int) []string {
	sep, hang := st.dim.Render(" · "), reviewHang
	if one := hang + strings.Join(append(slices.Clone(head), stats...), sep); ansi.StringWidth(one) <= w {
		return []string{one}
	}
	if w < narrowReview {
		hang = reviewIndent
	}

	return append(st.flow(hang, "", head, hang, w), st.flow(hang, "", stats, hang, w)...)
}

// narrowReview is the width under which the verdict line starts at the
// tree's indent rather than under the hint.
const narrowReview = 60

// reviewHang is where a review's lines under its REVIEW line start: under
// the hint. reviewIndent is where its tree, explanation, and findings
// start.
const (
	reviewHang   = "          "
	reviewIndent = "    "
)

// liveSteps is how many of the reviewer's steps the compact view shows
// while it runs.
const liveSteps = 4

// reviewSteps draws the reviewer's steps as a tree: the latest few and
// every live one while it runs, every one kept in the detailed view, and
// a spinning "thinking" last while it runs and no call is live.
func (st *Styles) reviewSteps(r *state.Review, w int, now time.Time, details bool) []string {
	steps := r.Steps
	var lines []string
	if !details {
		steps = latestSteps(steps, liveSteps)
	} else if n := r.StepCount - len(steps); n > 0 {
		lines = append(lines, st.dim.Render("⋮ "+plural(n, "earlier step")+" not kept"))
	}
	room := w - len(reviewIndent) - 2 // the branch
	for _, step := range steps {
		lines = append(lines, st.reviewStep(step, now, details, room))
	}
	if r.Running && !slices.ContainsFunc(r.Steps, live) {
		lines = append(lines, st.tool.Render(spin(now))+st.dim.Render(" thinking"))
	}
	out := make([]string, len(lines))
	for i, l := range lines {
		branch := "├ "
		if i == len(lines)-1 {
			branch = "└ "
		}
		out[i] = ansi.Truncate(st.dim.Render(reviewIndent+branch)+l, w, "…")
	}

	return out
}

// latestSteps are the latest n steps, in order, with every live one kept:
// a call still running before them takes the place of a finished one.
func latestSteps(steps []state.Item, n int) []state.Item {
	done := n
	for _, s := range steps {
		if live(s) {
			done--
		}
	}
	var out []state.Item
	for _, s := range slices.Backward(steps) {
		switch {
		case live(s):
			out = append(out, s)
		case done > 0:
			out, done = append(out, s), done-1
		}
	}
	slices.Reverse(out)

	return out
}

// live reports whether a tool call has not finished.
func live(it state.Item) bool { return it.Tool == state.ToolCalled || it.Tool == state.ToolRunning }

// stepLabel is the width of a step's label: the tool column's and a space.
const stepLabel = labelWidth + 1

// reviewStep is one of the reviewer's tool calls: its label in the tool
// column (the spinner before it while it runs, the error color when it
// failed) and what it did, then how it ended where that says something:
// its exit code, "no matches", "stopped", and in the detailed view how
// long it took. The text is cut to fit width w, so how it ended shows.
func (st *Styles) reviewStep(it state.Item, now time.Time, details bool, w int) string {
	label := cmp.Or(it.Verb, toolLabel(it.Name))
	padded := label + strings.Repeat(" ", max(stepLabel-ansi.StringWidth(label), 1))
	var head string
	var tail []string
	switch {
	case live(it):
		short := label + strings.Repeat(" ", max(stepLabel-2-ansi.StringWidth(label), 1))
		head = st.tool.Render(spin(now)) + " " + st.accent.Render(short)
	case it.Tool == state.ToolFailed && !noMatches(it):
		head = st.bad.Render(padded)
		if t := failTail(it); t != "" {
			tail = append(tail, st.bad.Render(t))
		}
	case it.Tool == state.ToolStopped:
		head = st.dim.Render(padded)
		tail = append(tail, st.warn.Render("stopped"))
	default:
		head = st.dim.Render(padded)
		if noMatches(it) {
			tail = append(tail, st.dim.Render("no matches"))
		}
	}
	if details && !live(it) && it.Duration >= 100*time.Millisecond {
		tail = append(tail, st.dim.Render(secs(it.Duration)))
	}
	end := ""
	if len(tail) > 0 {
		end = "  " + strings.Join(tail, st.dim.Render(" · "))
	}
	room := max(w-ansi.StringWidth(head)-ansi.StringWidth(end), 8)

	return head + ansi.Truncate(st.parts(toolParts(it)), room, "…") + end
}

// flow lays styled parts out after first: gap before the first part, a
// dim " · " between the others, and a new line, after indent, where the
// next part would pass width w. A part wider than a line is cut.
func (st *Styles) flow(first, gap string, parts []string, indent string, w int) []string {
	sep := st.dim.Render(" · ")
	var out []string
	line, empty := first, true
	for _, p := range parts {
		join := sep
		if empty {
			join = gap
		}
		if ansi.StringWidth(line+join+p) <= w {
			line, empty = line+join+p, false

			continue
		}
		if !empty || ansi.StringWidth(line) > ansi.StringWidth(indent) {
			out = append(out, ansi.Truncate(line, w, "…"))
		}
		line, empty = indent+p, false
	}

	return append(out, ansi.Truncate(line, w, "…"))
}

// findingIndent is where a finding's title, place, and body start, after
// its priority; findingWidth is the widest a finding's title row grows, so
// its confidence stays near the title on a wide screen.
const (
	findingIndent = "        "
	findingWidth  = 100
)

// findingBodyLines is how many lines of a finding's body the compact view
// shows.
const findingBodyLines = 3

// findingLines draws one finding: "P0  title" with its confidence ("91%")
// at the row's right end (P0 and P1 in the bad color, P2 in the warning
// color, P3 dim), its place relative to the workspace, and its body as
// Markdown, cut to a few lines in the compact view. A finding of low
// confidence is drawn dim throughout.
func (st *Styles) findingLines(f codereview.Finding, workspace string, w int, details bool) []string {
	tag, prio := st.dim, ""
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
	title, score := st.bold, ""
	if low {
		tag, title = st.dim, st.dim
	}
	if c, ok := f.Confidence(); ok {
		score = codereview.Percent(c)
	}
	right := min(w, findingWidth)
	room := right - len(findingIndent) - ansi.StringWidth(score) - 2
	if score == "" {
		room = right - len(findingIndent)
	}
	room = max(room, 10)
	rows := strings.Split(ansi.Wrap(untab(oneLine(f.Heading())), room, ""), "\n")
	out := []string{""}
	for i, row := range rows {
		lead := findingIndent
		if i == 0 {
			lead = "    " + tag.Render(prio+strings.Repeat(" ", len(findingIndent)-4-len(prio)))
		}
		line := lead + title.Render(row)
		if i == 0 && score != "" {
			gap := max(right-ansi.StringWidth(line)-ansi.StringWidth(score), 2)
			line += strings.Repeat(" ", gap) + st.dim.Render(score)
		}
		out = append(out, ansi.Truncate(line, w, "…"))
	}
	out = append(out, ansi.Truncate(st.dim.Render(findingIndent+f.Place(workspace)), w, "…"))
	body := strings.TrimSpace(f.Body)
	if body == "" {
		return out
	}
	lines := st.markdownLines(body, w, findingIndent, findingIndent)
	if low {
		for i, l := range lines {
			lines[i] = st.dim.Render(ansi.Strip(l))
		}
	}
	if n := len(lines) - findingBodyLines; !details && n > 1 {
		lines = append(lines[:findingBodyLines:findingBodyLines], st.dim.Render(findingIndent+fmt.Sprintf("… %d more lines (ctrl+t to view)", n)))
	}

	return append(out, lines...)
}
