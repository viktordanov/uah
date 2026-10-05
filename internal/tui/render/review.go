package render

import (
	"cmp"
	"fmt"
	"math"
	"path/filepath"
	"regexp"
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

// reviewLines draws a /review: the REVIEW line (what it looks at, the
// reviewer, the time, the steps, the tokens) and, while it runs, the
// reviewer's latest steps; once done, the verdict line, the explanation,
// and the findings, sorted. The detailed view lists every step kept, also
// after the review, and the findings' whole bodies.
func (st *Styles) reviewLines(it state.Item, w int, now time.Time, details bool) []string {
	r := it.Review
	if r == nil {
		return nil
	}
	label, end := st.dim.Render("  REVIEW "), r.Ended
	if r.Running {
		label, end = st.accent.Render("  REVIEW "), now
	}
	var meta []string
	if m := strings.TrimSpace(r.Model + " " + r.Effort); m != "" {
		meta = append(meta, m)
	}
	meta = append(meta, elapsed(end.Sub(r.Started)))
	if r.StepCount > 0 {
		meta = append(meta, plural(r.StepCount, "step"))
	}
	if n := r.Tokens.InputTokens + r.Tokens.OutputTokens; n > 0 {
		meta = append(meta, tokens(n)+" tokens")
	}
	out := append([]string{""}, st.flow(label+r.Hint, "  ", styleLines(meta, st.dim), reviewIndent, w)...)
	if r.Running || details {
		out = append(out, st.reviewSteps(r, w, now, details)...)
	}
	switch {
	case r.Running:
		return out
	case r.Err != "":
		return append(out, styleLines(wrapPrefixed("failed: "+oneLine(r.Err), w, reviewIndent, reviewIndent), st.bad)...)
	case r.Interrupted:
		return append(out, st.warn.Render(reviewIndent+"interrupted"))
	case details && len(r.Steps) > 0:
		out = append(out, "")
	}
	out = append(out, st.flow(reviewIndent, "", st.verdict(r.Output), reviewIndent, w)...)
	if e := strings.TrimSpace(r.Output.OverallExplanation); e != "" {
		out = append(out, st.markdownLines(e, w, reviewIndent, reviewIndent)...)
	} else if len(r.Output.Findings) == 0 {
		out = append(out, st.dim.Render(reviewIndent+codereview.FallbackMessage))
	}
	for _, f := range sortedFindings(r.Output.Findings) {
		out = append(out, st.findingLines(f, r.Workspace, w, details)...)
	}

	return out
}

// reviewIndent is where a review's lines start under its REVIEW line.
const reviewIndent = "    "

// liveSteps is how many of the reviewer's latest steps the compact view
// shows while it runs.
const liveSteps = 4

// reviewSteps draws the reviewer's steps, one line each: the latest few
// and every live one while it runs, every one kept in the detailed view,
// with a spinning "thinking" last while it runs and no call is live.
func (st *Styles) reviewSteps(r *state.Review, w int, now time.Time, details bool) []string {
	steps := r.Steps
	var out []string
	if !details {
		steps = latestSteps(steps, liveSteps)
	} else if n := r.StepCount - len(steps); n > 0 {
		out = append(out, st.dim.Render(reviewIndent+"⋮ "+plural(n, "earlier step")+" not kept"))
	}
	for _, step := range steps {
		out = append(out, st.reviewStep(step, w, now, details))
	}
	if r.Running && !slices.ContainsFunc(r.Steps, live) {
		out = append(out, ansi.Truncate(reviewIndent+st.tool.Render(spin(now))+st.dim.Render(" thinking"), w, "…"))
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

// reviewStep is one of the reviewer's tool calls: a mark (the spinner
// while it runs, ✓, ✗ and its exit code when it failed, ■ when the review
// stopped it), its label in the tool column, and what it did; the
// detailed view adds how long it took.
func (st *Styles) reviewStep(it state.Item, w int, now time.Time, details bool) string {
	label := cmp.Or(it.Verb, toolLabel(it.Name))
	var head, tail string
	switch {
	case live(it):
		head = st.tool.Render(spin(now)) + " " + st.accent.Render(pad(liveLabel(label)))
	case it.Tool == state.ToolFailed && !noMatches(it):
		head = st.bad.Render("✗") + " " + st.dim.Render(pad(label))
		if t := failTail(it); t != "" {
			tail = st.bad.Render("  " + t)
		}
	case it.Tool == state.ToolStopped:
		head = st.warn.Render("■") + " " + st.dim.Render(pad(label))
	default:
		head = st.dim.Render("✓ " + pad(label))
		if noMatches(it) {
			tail = st.dim.Render("  no matches")
		}
	}
	if details && !live(it) && it.Duration >= time.Second {
		tail += st.dim.Render("  " + secs(it.Duration))
	}
	head = reviewIndent + head
	room := max(w-ansi.StringWidth(head)-ansi.StringWidth(tail), 8)

	return ansi.Truncate(head+ansi.Truncate(st.parts(toolParts(it)), room, "…")+tail, w, "…")
}

// verdict is the finished review's second line, in parts: its findings
// with their count by priority ("3 findings (1 P0 · 2 P2)"), the verdict
// (correct in the good color, incorrect in the bad one), and the
// reviewer's confidence.
func (st *Styles) verdict(o codereview.Output) []string {
	n := plural(len(o.Findings), "finding")
	if len(o.Findings) == 0 {
		n = "no findings"
	}
	var counts [4]int
	for _, f := range o.Findings {
		if _, p := findingPriority(f); p >= 0 {
			counts[p]++
		}
	}
	var by []string
	for p, c := range counts {
		if c > 0 {
			by = append(by, fmt.Sprintf("%d P%d", c, p))
		}
	}
	if len(by) > 0 {
		n += " (" + strings.Join(by, " · ") + ")"
	}
	parts := []string{st.bold.Render(n)}
	switch v := strings.TrimSpace(o.OverallCorrectness); v {
	case "":
	case "patch is correct":
		parts = append(parts, st.ok.Render(v))
	case "patch is incorrect":
		parts = append(parts, st.bad.Render(v))
	default:
		parts = append(parts, st.dim.Render(v))
	}
	if c, ok := confidence(o.OverallConfidenceScore); ok {
		parts = append(parts, st.dim.Render("confidence "+percent(c)))
	}

	return parts
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

// lowConfidence is the confidence under which a finding is drawn dim.
// Codex shows no confidence and filters nothing (rust-v0.160.0), so the
// line is uah's own.
const lowConfidence = 0.5

// confidence is a score from 0 to 1, and whether the reviewer gave one:
// 0 is a score left out, and a score from 1 to 100 a percentage.
func confidence(c float64) (float64, bool) {
	switch {
	case c <= 0 || c > 100:
		return 0, false
	case c > 1:
		return c / 100, true
	}

	return c, true
}

// percent is a confidence as "82%".
func percent(c float64) string { return strconv.Itoa(int(math.Round(c*100))) + "%" }

// sortedFindings are the findings by priority, P0 first and those without
// one last, then by confidence, highest first; ties keep the reviewer's
// order.
func sortedFindings(findings []codereview.Finding) []codereview.Finding {
	out := slices.Clone(findings)
	rank := func(f codereview.Finding) int {
		if _, p := findingPriority(f); p >= 0 {
			return p
		}

		return 4
	}
	slices.SortStableFunc(out, func(a, b codereview.Finding) int {
		ca, _ := confidence(a.ConfidenceScore)
		cb, _ := confidence(b.ConfidenceScore)

		return cmp.Or(cmp.Compare(rank(a), rank(b)), cmp.Compare(cb, ca))
	})

	return out
}

// findingIndent is where a finding's text starts, after its priority in
// the tool column.
var findingIndent = strings.Repeat(" ", 4+labelWidth)

// priorityTag is the "[P1] " the rubric starts a title with.
var priorityTag = regexp.MustCompile(`^\s*\[P([0-3])\]\s*`)

// findingPriority is a finding's title without its "[P1] " tag and its
// priority: the tag's, else the priority field's, else -1.
func findingPriority(f codereview.Finding) (string, int) {
	if m := priorityTag.FindStringSubmatch(f.Title); m != nil {
		return f.Title[len(m[0]):], int(m[1][0] - '0')
	}
	if f.Priority != nil && *f.Priority >= 0 && *f.Priority <= 3 {
		return f.Title, *f.Priority
	}

	return f.Title, -1
}

// findingBodyLines is how many lines of a finding's body the compact view
// shows.
const findingBodyLines = 4

// findingLines draws one finding: "P1  title  82%" (P0 and P1 in the bad
// color, P2 in the warning color, P3 dim), its place relative to the
// workspace, and its body as Markdown, cut to a few lines in the compact
// view. A finding of low confidence is drawn dim throughout.
func (st *Styles) findingLines(f codereview.Finding, workspace string, w int, details bool) []string {
	title, p := findingPriority(f)
	prio, tag := "", st.dim
	if p >= 0 {
		prio = fmt.Sprintf("P%d", p)
	}
	switch p {
	case 0, 1:
		tag = st.bad
	case 2:
		tag = st.warn
	}
	c, scored := confidence(f.ConfidenceScore)
	low := scored && c < lowConfidence
	head, score := st.bold.Render(title), ""
	if scored {
		score = "  " + percent(c)
	}
	if low {
		tag, head, score = st.dim, st.dim.Render(title), score+" · low confidence"
	}
	out := append([]string{""}, wrapPrefixed(head+st.dim.Render(score), w, "    "+tag.Render(pad(prio)), findingIndent)...)
	out = append(out, ansi.Truncate(st.dim.Render(findingIndent+findingPlace(f, workspace)), w, "…"))
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
		lines = append(lines[:findingBodyLines:findingBodyLines], st.dim.Render(findingIndent+fmt.Sprintf("… +%d lines (ctrl+t to view)", n)))
	}

	return append(out, lines...)
}

// findingPlace is "path:12-14" (one number for one line), relative to the
// workspace when the file is in it.
func findingPlace(f codereview.Finding, workspace string) string {
	loc := f.CodeLocation
	path := loc.AbsoluteFilePath
	if workspace != "" {
		if rel, err := filepath.Rel(workspace, path); err == nil && !strings.HasPrefix(rel, "..") {
			path = rel
		}
	}
	if path == "" {
		return "(no location)"
	}
	lines := fmt.Sprintf("%d-%d", loc.LineRange.Start, loc.LineRange.End)
	if loc.LineRange.End <= loc.LineRange.Start {
		lines = strconv.Itoa(loc.LineRange.Start)
	}

	return path + ":" + lines
}
