package render_test

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/codereview"
	"github.com/viktordanov/uah/internal/gitdiff"
	"github.com/viktordanov/uah/internal/patch"
	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/internal/tui/render"
	"github.com/viktordanov/uah/internal/tui/state"
)

// TestScreens_Diff draws /diff: an edited file, an untracked one, and a
// binary one with its note.
func TestScreens_GitDiff(t *testing.T) {
	d := gitdiff.Diff{Root: "/workspace/proj", Files: []gitdiff.File{
		{FileDiff: patch.FileDiff{Op: "update", Path: "cmd/main.go", Added: 1, Removed: 1, Hunks: []patch.DiffHunk{{Lines: []patch.DiffLine{
			{Kind: " ", Old: 7, New: 7, Text: "func main() {"},
			{Kind: "-", Old: 8, Text: `	fmt.Println("hi")`},
			{Kind: "+", New: 8, Text: `	fmt.Println("hello")`},
			{Kind: " ", Old: 9, New: 9, Text: "}"},
		}}}}},
		{FileDiff: patch.FileDiff{Op: "add", Path: "NOTES.md", Added: 2, Hunks: []patch.DiffHunk{{Lines: []patch.DiffLine{
			{Kind: "+", New: 1, Text: "# Notes"},
			{Kind: "+", New: 2, Text: "Remember the tests."},
		}}}}, Untracked: true},
		{FileDiff: patch.FileDiff{Op: "add", Path: "logo.png"}, Untracked: true, Note: gitdiff.NoteBinary},
	}, MoreUntracked: 3}
	golden(t, "gitdiff", screen(apply(base(), state.DiffShown{Diff: d}), ""))
}

// reviewerBash is a reviewer's command, as the runner reports it.
func reviewerBash(id, command string) core.ToolCalled {
	return core.ToolCalled{At: t0, CallID: id, Name: "Bash", Label: command, Arguments: `{"command":` + strconv.Quote(command) + `}`}
}

// reviewing is a review of the changes against main that has run 42s:
// five steps (a read, a search that found nothing, a failed test run, a
// listing, and a diff still running) and two model responses.
func reviewing() state.State {
	act := func(e core.Event) session.ReviewActivity { return session.ReviewActivity{At: t0, ID: "r1", Event: e} }
	done := func(id string, ok bool, detail string, d time.Duration) session.ReviewActivity {
		return act(core.ToolFinished{At: t0, CallID: id, Name: "Bash", OK: ok, Detail: detail, Duration: d})
	}

	return apply(base(),
		session.ReviewStarted{At: t0, ID: "r1", Hint: "changes against 'main'", Model: "gpt-6-astra", Effort: "high"},
		act(reviewerBash("c1", "sed -n 1,80p /workspace/proj/internal/config/load.go")), done("c1", true, "exit 0", 0),
		act(core.ModelResponded{At: t0, Usage: core.Tokens{InputTokens: 12_000, OutputTokens: 400}}),
		act(reviewerBash("c2", "rg -n ParseConfig internal")), done("c2", false, "exit 1", 0),
		act(reviewerBash("c3", "go test ./internal/config")), done("c3", false, "exit 1", 4*time.Second),
		act(reviewerBash("c4", "ls internal/config")), done("c4", true, "exit 0", 0),
		act(core.ModelResponded{At: t0, Usage: core.Tokens{InputTokens: 26_000, OutputTokens: 700}}),
		act(reviewerBash("c5", "git diff 1a2b3c4")), act(core.ToolStarted{At: t0, CallID: "c5", Name: "Bash"}),
		state.Tick{Now: t0.Add(42 * time.Second)},
	)
}

// findings is a review's answer with findings out of order: a P3, two
// P1s of different confidence, one of low confidence, a P2 with a long
// body, and one without a priority.
const findings = `{"findings":[
{"title":"[P3] Name the timeout","body":"A named constant would say what 30 is.","confidence_score":0.6,"priority":3,
 "code_location":{"absolute_file_path":"/elsewhere/x.go","line_range":{"start":7,"end":7}}},
{"title":"[P1] Close the file on error","body":"The early return leaks the handle.","confidence_score":0.55,"priority":1,
 "code_location":{"absolute_file_path":"/workspace/proj/internal/config/load.go","line_range":{"start":30,"end":31}}},
{"title":"[P1] Return the parse error","body":"` + "`parse`" + ` drops the error, so a bad file loads as empty.","confidence_score":0.82,"priority":1,
 "code_location":{"absolute_file_path":"/workspace/proj/internal/config/load.go","line_range":{"start":41,"end":44}}},
{"title":"[P2] Guard the empty list","body":"An empty list reaches ` + "`xs[0]`" + `.\n\n1. Load an empty file.\n2. Call ` + "`First`" + `.\n3. It panics.\n\nCheck the length first.","confidence_score":0.7,"priority":2,
 "code_location":{"absolute_file_path":"/workspace/proj/internal/config/list.go","line_range":{"start":12,"end":15}}},
{"title":"[P2] Maybe drop the retry","body":"The retry might be needed after all.","confidence_score":0.3,"priority":2,
 "code_location":{"absolute_file_path":"/workspace/proj/internal/config/retry.go","line_range":{"start":3,"end":9}}},
{"title":"Mention the default","body":"The doc comment leaves the default out.","confidence_score":0.9,
 "code_location":{"absolute_file_path":"/workspace/proj/internal/config/doc.go","line_range":{"start":1,"end":1}}}],
"overall_correctness":"patch is incorrect","overall_explanation":"The loader hides a failure and can panic on an empty list.","overall_confidence_score":0.82}`

// reviewed is the review from reviewing, finished after 2m 10s with out.
func reviewed(out codereview.Output) state.State {
	return apply(reviewing(), session.ReviewFinished{At: t0.Add(130 * time.Second), ID: "r1", Output: out, Tokens: core.Tokens{InputTokens: 138_000, OutputTokens: 2_100}})
}

// TestScreens_Review draws a review while it runs: the latest steps, the
// newest with the spinner, the steps and tokens so far in its line; every
// step in the detailed view; and "thinking" while no call runs.
func TestScreens_Review(t *testing.T) {
	running := reviewing()
	golden(t, "review-running", screen(running, ""))
	golden(t, "review-running-details", screen(apply(running, state.ToggleDetails{}), ""))

	started := apply(base(), session.ReviewStarted{At: t0, ID: "r1", Hint: "changes against 'main'"})
	assert.Contains(t, screen(started, ""), "    ⠋ thinking", "before its first tool")
	thinking := apply(running, session.ReviewActivity{At: t0, ID: "r1", Event: core.ToolFinished{At: t0, CallID: "c5", Name: "Bash", OK: true, Detail: "exit 0"}})
	got := screen(thinking, "")
	assert.Contains(t, got, "✓ RAN    git diff 1a2b3c4\n    ⠋ thinking", "the spinner moves to a thinking line between calls")
	assert.Contains(t, got, "5 steps · 39.1k tokens", "the tokens of its model responses so far")

	act := func(e core.Event) session.ReviewActivity { return session.ReviewActivity{At: t0, ID: "r1", Event: e} }
	slow := apply(reviewing(),
		act(reviewerBash("c6", "ls a")), act(core.ToolFinished{At: t0, CallID: "c6", Name: "Bash", OK: true, Detail: "exit 0"}),
		act(reviewerBash("c7", "ls b")), act(core.ToolFinished{At: t0, CallID: "c7", Name: "Bash", OK: true, Detail: "exit 0"}),
		act(reviewerBash("c8", "ls c")), act(core.ToolFinished{At: t0, CallID: "c8", Name: "Bash", OK: true, Detail: "exit 0"}),
		act(reviewerBash("c9", "ls d")), act(core.ToolFinished{At: t0, CallID: "c9", Name: "Bash", OK: true, Detail: "exit 0"}),
	)
	got = screen(slow, "")
	assert.Contains(t, got, "⠋ RUN    git diff 1a2b3c4\n    ✓ LIST   b\n    ✓ LIST   c\n    ✓ LIST   d\n\n", "a call still running stays in view, in the place of the oldest finished one")
	assert.NotContains(t, got, "thinking", "no thinking line while a call runs")
}

// TestScreens_ReviewFindings draws a finished review: the verdict line
// with the counts by priority and the confidence, the findings sorted by
// priority then confidence, a low-confidence one marked, and a long body
// cut in the compact view and whole in the detailed one, where the steps
// show again.
func TestScreens_ReviewFindings(t *testing.T) {
	done := reviewed(codereview.Parse(findings))
	golden(t, "review", screenTall(done))
	golden(t, "review-details", screenTall(apply(done, state.ToggleDetails{})))

	got := screenWidth(done, 100)
	order := []string{"Return the parse error  82%", "Close the file on error  55%", "Guard the empty list  70%", "Maybe drop the retry  30% · low confidence", "Name the timeout  60%", "Mention the default  90%"}
	last := 0
	for _, title := range order {
		i := strings.Index(got, title)
		require.Positive(t, i, title)
		assert.Greater(t, i, last, "%s comes after the one before", title)
		last = i
	}
	assert.Contains(t, got, "6 findings (2 P1 · 2 P2 · 1 P3) · patch is incorrect · confidence 82%")
	assert.Contains(t, got, "gpt-6-astra high · 2m 10s · 5 steps · 140.1k tokens")
	assert.Contains(t, got, "… +3 lines (ctrl+t to view)")
	assert.NotContains(t, got, "git diff 1a2b3c4", "the steps fold away in the compact view")
}

// TestScreens_ReviewVerdict styles the verdict: correct in the good
// color, incorrect in the bad one; a review without findings says so.
func TestScreens_ReviewVerdict(t *testing.T) {
	theme := render.Amber
	color := func(c interface{ RGBA() (r, g, b, a uint32) }) string {
		r, g, b, _ := c.RGBA()

		return fmt.Sprintf("38;2;%d;%d;%d", r>>8, g>>8, b>>8)
	}
	clean := reviewed(codereview.Parse(`{"findings":[],"overall_correctness":"patch is correct","overall_explanation":"The change is sound.","overall_confidence_score":0.9}`))
	golden(t, "review-clean", screen(clean, ""))
	raw := rawScreen(clean, render.NewCache(theme))
	tall := func(s state.State) string {
		out, _ := render.Screen(s, render.NewCache(theme), render.Frame{Width: 100, Height: 90, Composer: "λ ", ComposerHeight: 1})

		return out
	}
	assert.Regexp(t, regexp.QuoteMeta(color(theme.Good))+`[;m][^\x1b]*patch is correct`, raw)

	bad := tall(reviewed(codereview.Parse(findings)))
	assert.Regexp(t, regexp.QuoteMeta(color(theme.Bad))+`[;m][^\x1b]*patch is incorrect`, bad)

	empty := screen(reviewed(codereview.Output{}), "")
	assert.Contains(t, empty, "no findings")
	assert.Contains(t, empty, codereview.FallbackMessage)
}

// TestScreens_ReviewEnded draws a review that failed or was stopped: its
// line with the time and tokens, then how it ended; the detailed view
// keeps its steps, the running one stopped.
func TestScreens_ReviewEnded(t *testing.T) {
	stopped := apply(reviewing(), session.ReviewFinished{At: t0.Add(time.Minute), ID: "r1", Interrupted: true, Tokens: core.Tokens{InputTokens: 20_000, OutputTokens: 300}})
	golden(t, "review-interrupted", screen(stopped, ""))
	assert.Contains(t, screen(apply(stopped, state.ToggleDetails{}), ""), "■ RAN    git diff 1a2b3c4")

	failed := apply(reviewing(), session.ReviewFinished{At: t0.Add(time.Minute), ID: "r1", Err: "the reviewer did not answer"})
	golden(t, "review-failed", screen(failed, ""))
}

// TestScreens_ReviewNarrow draws every review state at 60 and 40 columns:
// no line passes the width, and the verdict line wraps between its parts.
func TestScreens_ReviewNarrow(t *testing.T) {
	states := map[string]state.State{
		"running":  reviewing(),
		"findings": reviewed(codereview.Parse(findings)),
		"failed":   apply(reviewing(), session.ReviewFinished{At: t0.Add(time.Minute), ID: "r1", Err: strings.Repeat("the provider closed the connection ", 4)}),
	}
	for name, s := range states {
		for _, details := range []bool{false, true} {
			if details {
				s = apply(s, state.ToggleDetails{})
			}
			for _, w := range []int{60, 40} {
				out, _ := render.Screen(s, render.NewCache(render.Amber), render.Frame{Width: w, Height: 90, Composer: "λ ", ComposerHeight: 1})
				for _, line := range strings.Split(out, "\n") {
					assert.LessOrEqual(t, ansi.StringWidth(line), w, "%s details=%v at %d: %q", name, details, w, ansi.Strip(line))
				}
			}
		}
	}
	golden(t, "review-60", screenWidth(reviewed(codereview.Parse(findings)), 60))
	golden(t, "review-40", screenWidth(reviewed(codereview.Parse(findings)), 40))
	golden(t, "review-running-40", screenWidth(reviewing(), 40))
}

// screenWidth is a tall screen at width w, as text.
func screenWidth(s state.State, w int) string {
	out, _ := render.Screen(s, render.NewCache(render.Amber), render.Frame{Width: w, Height: 90, Composer: "λ ", ComposerHeight: 1})
	lines := strings.Split(ansi.Strip(out), "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " ")
	}

	return strings.Join(lines, "\n") + "\n"
}

// TestSelectedTextReview copies a finished review as clean text: the
// verdict, and each finding's priority, title, confidence, and place.
func TestSelectedTextReview(t *testing.T) {
	s := reviewed(codereview.Parse(findings))
	key := s.Items[len(s.Items)-1].Key
	text, _ := render.SelectedText(selecting(s, pos(key, 0, 0), pos(key, 99, 99)), render.NewCache(render.Amber), render.Frame{Width: 100, Height: 24})
	assert.True(t, strings.HasPrefix(text, "REVIEW changes against 'main'  gpt-6-astra high · 2m 10s · 5 steps · 140.1k tokens\n"+
		"  6 findings (2 P1 · 2 P2 · 1 P3) · patch is incorrect · confidence 82%\n"), text)
	assert.Contains(t, text, "P1     Return the parse error  82%\n       internal/config/load.go:41-44\n       parse drops the error, so a bad file loads as empty.")
}
