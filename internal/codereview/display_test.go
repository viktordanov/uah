package codereview_test

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/viktordanov/uah/internal/codereview"
)

func prio(p int) *int { return &p }

// TestFinding_Level reads the priority from the title's tag, else the
// field; a value out of range is none.
func TestFinding_Level(t *testing.T) {
	cases := []struct {
		f       codereview.Finding
		want    int
		ok      bool
		heading string
	}{
		{codereview.Finding{Title: "[P1] Check it", Priority: prio(3)}, 1, true, "Check it"},
		{codereview.Finding{Title: "Check it", Priority: prio(2)}, 2, true, "Check it"},
		{codereview.Finding{Title: "Check it", Priority: prio(7)}, 0, false, "Check it"},
		{codereview.Finding{Title: "  [P0]Now"}, 0, true, "Now"},
		{codereview.Finding{Title: "[P4] Odd"}, 0, false, "[P4] Odd"},
	}
	for _, c := range cases {
		p, ok := c.f.Level()
		assert.Equal(t, c.ok, ok, c.f.Title)
		assert.Equal(t, c.want, p, c.f.Title)
		assert.Equal(t, c.heading, c.f.Heading())
	}
}

// TestFinding_Confidence: 0 and NaN are left out, a score above 1 is a
// percentage, one above 100 is nonsense; under 0.5 is low.
func TestFinding_Confidence(t *testing.T) {
	cases := []struct {
		score float64
		want  float64
		ok    bool
		low   bool
	}{
		{0.82, 0.82, true, false}, {0.3, 0.3, true, true}, {0, 0, false, false}, {math.NaN(), 0, false, false},
		{82, 0.82, true, false}, {150, 0, false, false}, {-1, 0, false, false}, {1, 1, true, false},
	}
	for _, c := range cases {
		f := codereview.Finding{ConfidenceScore: c.score}
		got, ok := f.Confidence()
		assert.Equal(t, c.ok, ok, c.score)
		assert.InDelta(t, c.want, got, 1e-9, c.score)
		assert.Equal(t, c.low, f.Low(), c.score)
	}
	assert.Equal(t, "82%", codereview.Percent(0.816))
}

// TestSorted orders by priority, none last, then confidence, ties in the
// reviewer's order, and leaves the input as it was.
func TestSorted(t *testing.T) {
	in := []codereview.Finding{
		{Title: "a", ConfidenceScore: 0.9},
		{Title: "[P3] b", ConfidenceScore: 0.9},
		{Title: "[P1] c", ConfidenceScore: 0.5},
		{Title: "[P1] d", ConfidenceScore: 0.8},
		{Title: "e", Priority: prio(1), ConfidenceScore: 0.8},
		{Title: "[P0] f"},
	}
	var titles []string
	for _, f := range codereview.Sorted(in) {
		titles = append(titles, f.Title)
	}
	assert.Equal(t, []string{"[P0] f", "[P1] d", "e", "[P1] c", "[P3] b", "a"}, titles)
	assert.Equal(t, "a", in[0].Title, "the input keeps its order")
}

// TestOutput_CountsAndVerdict: the count by priority and the marked
// verdict.
func TestOutput_CountsAndVerdict(t *testing.T) {
	o := codereview.Output{Findings: []codereview.Finding{{Title: "[P0] a"}, {Title: "[P2] b"}, {Title: "[P2] c"}, {Title: "d"}}, OverallCorrectness: " patch is incorrect"}
	assert.Equal(t, "4 findings (1 P0 · 2 P2)", o.Counts())
	v, correct := o.Verdict()
	assert.Equal(t, "✗ patch is incorrect", v)
	assert.Equal(t, -1, correct)

	o = codereview.Output{Findings: []codereview.Finding{{Title: "x"}}, OverallCorrectness: "patch is correct"}
	assert.Equal(t, "1 finding", o.Counts())
	v, correct = o.Verdict()
	assert.Equal(t, "✓ patch is correct", v)
	assert.Equal(t, 1, correct)

	o = codereview.Output{OverallCorrectness: "unclear"}
	assert.Equal(t, "no findings", o.Counts())
	v, correct = o.Verdict()
	assert.Equal(t, "unclear", v)
	assert.Equal(t, 0, correct)
}

// TestFinding_Place is relative to the workspace when the file is in it.
func TestFinding_Place(t *testing.T) {
	at := func(path string, start, end int) codereview.Finding {
		return codereview.Finding{CodeLocation: codereview.Location{AbsoluteFilePath: path, LineRange: codereview.LineRange{Start: start, End: end}}}
	}
	assert.Equal(t, "a/b.go:3-4", at("/w/a/b.go", 3, 4).Place("/w"))
	assert.Equal(t, "/x/b.go:7", at("/x/b.go", 7, 7).Place("/w"))
	assert.Equal(t, "/wx/b.go:7", at("/wx/b.go", 7, 0).Place("/w"))
	assert.Equal(t, "(no location)", at("", 1, 2).Place("/w"))
}
