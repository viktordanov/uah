package codereview

import (
	"cmp"
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// What the TUI and `uah review` show of a review beyond Codex's text: each
// finding's priority and confidence, the findings sorted by them, and the
// counts by priority. Codex parses the confidence scores and shows none
// (rust-v0.160.0); what the main agent gets (ExitMessage) is unchanged.

// LowConfidence is the confidence under which a finding is shown faint.
// Codex has no such line; it is uah's own.
const LowConfidence = 0.5

// Verdicts are the rubric's two answers for overall_correctness.
const (
	VerdictCorrect   = "patch is correct"
	VerdictIncorrect = "patch is incorrect"
)

// priorityTag is the "[P1] " the rubric starts a title with.
var priorityTag = regexp.MustCompile(`^\s*\[P([0-3])\]\s*`)

// Level is a finding's priority, 0 (P0) to 3, from the title's "[P1]" tag,
// else the priority field, and whether it has one.
func (f Finding) Level() (int, bool) {
	if m := priorityTag.FindStringSubmatch(f.Title); m != nil {
		return int(m[1][0] - '0'), true
	}
	if f.Priority != nil && *f.Priority >= 0 && *f.Priority <= 3 {
		return *f.Priority, true
	}

	return 0, false
}

// Heading is a finding's title without its "[P1] " tag.
func (f Finding) Heading() string {
	if m := priorityTag.FindStringSubmatch(f.Title); m != nil {
		return f.Title[len(m[0]):]
	}

	return f.Title
}

// Confidence is the finding's confidence from 0 to 1, and whether the
// reviewer gave one.
func (f Finding) Confidence() (float64, bool) { return confidence(f.ConfidenceScore) }

// Confidence is the reviewer's overall confidence from 0 to 1, and whether
// it gave one.
func (o Output) Confidence() (float64, bool) { return confidence(o.OverallConfidenceScore) }

// Low reports whether the reviewer gave the finding a confidence under
// LowConfidence.
func (f Finding) Low() bool {
	c, ok := f.Confidence()

	return ok && c < LowConfidence
}

// confidence reads a score: 0 (or less, or not a number) is a score left
// out, and one from 1 to 100 a percentage.
func confidence(c float64) (float64, bool) {
	switch {
	case math.IsNaN(c) || c <= 0 || c > 100:
		return 0, false
	case c > 1:
		return c / 100, true
	}

	return c, true
}

// Percent is a confidence as "82%".
func Percent(c float64) string { return strconv.Itoa(int(math.Round(c*100))) + "%" }

// Sorted are the findings by priority, P0 first and those without one
// last, then by confidence, highest first; ties keep the reviewer's order.
func Sorted(findings []Finding) []Finding {
	out := slices.Clone(findings)
	rank := func(f Finding) int {
		if p, ok := f.Level(); ok {
			return p
		}

		return 4
	}
	slices.SortStableFunc(out, func(a, b Finding) int {
		ca, _ := a.Confidence()
		cb, _ := b.Confidence()

		return cmp.Or(cmp.Compare(rank(a), rank(b)), cmp.Compare(cb, ca))
	})

	return out
}

// Counts is "3 findings (1 P0 · 1 P1 · 1 P3)", "1 finding", or "no
// findings".
func (o Output) Counts() string {
	var n string
	switch len(o.Findings) {
	case 0:
		return "no findings"
	case 1:
		n = "1 finding"
	default:
		n = fmt.Sprintf("%d findings", len(o.Findings))
	}
	var counts [4]int
	for _, f := range o.Findings {
		if p, ok := f.Level(); ok {
			counts[p]++
		}
	}
	var by []string
	for p, c := range counts {
		if c > 0 {
			by = append(by, fmt.Sprintf("%d P%d", c, p))
		}
	}
	if len(by) == 0 {
		return n
	}

	return n + " (" + strings.Join(by, " · ") + ")"
}

// Verdict is the verdict with its mark: "✓ patch is correct", "✗ patch is
// incorrect", or another answer as it is; correct is +1, -1, or 0 for
// neither.
func (o Output) Verdict() (text string, correct int) {
	switch v := strings.TrimSpace(o.OverallCorrectness); v {
	case VerdictCorrect:
		return "✓ " + v, 1
	case VerdictIncorrect:
		return "✗ " + v, -1
	default:
		return v, 0
	}
}

// Place is "path:12-14" (one number for one line), relative to workspace
// when the file is in it, or "(no location)".
func (f Finding) Place(workspace string) string {
	loc := f.CodeLocation
	path := loc.AbsoluteFilePath
	if path == "" {
		return "(no location)"
	}
	if workspace != "" {
		if rel, err := filepath.Rel(workspace, path); err == nil && !strings.HasPrefix(rel, "..") {
			path = rel
		}
	}
	lines := fmt.Sprintf("%d-%d", loc.LineRange.Start, loc.LineRange.End)
	if loc.LineRange.End <= loc.LineRange.Start {
		lines = strconv.Itoa(loc.LineRange.Start)
	}

	return path + ":" + lines
}
