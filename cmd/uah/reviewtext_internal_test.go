package main

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/cmdparse"
	"github.com/viktordanov/uah/internal/codereview"
	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/session"
)

// reviewGolden compares got with testdata/<name>.golden, which -update
// (the flag main_test registers in the same binary) rewrites.
func reviewGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if f := flag.Lookup("update"); f != nil && f.Value.String() == "true" {
		require.NoError(t, os.WriteFile(path, []byte(got), 0o600))
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "run go test ./cmd/uah -update")
	assert.Equal(t, string(want), got)
}

// finishedReview is a review with findings out of order: a P3, a P1 of
// low confidence, a P1, a P0, and one without a priority.
func finishedReview() reviewOutput {
	t0 := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

	return reviewOutput{
		env:     cmdparse.Env{Workspace: "/w"},
		started: session.ReviewStarted{At: t0, Hint: "changes against 'main'", Model: "gpt-6-astra", Effort: "high"},
		finished: session.ReviewFinished{At: t0.Add(130 * time.Second), Tokens: core.Tokens{InputTokens: 138_000, OutputTokens: 2_100}, Output: codereview.Parse(`{"findings":[
{"title":"[P3] Name it","body":"A name.","confidence_score":0.6,"priority":3,"code_location":{"absolute_file_path":"/w/a.go","line_range":{"start":1,"end":1}}},
{"title":"[P1] Maybe","body":"Perhaps.\n\nOr not.","confidence_score":0.3,"priority":1,"code_location":{"absolute_file_path":"/w/b.go","line_range":{"start":2,"end":3}}},
{"title":"[P1] Check the error","body":"It is dropped:\n\n- in load\n- in save","confidence_score":0.82,"priority":1,"code_location":{"absolute_file_path":"/w/c.go","line_range":{"start":4,"end":9}}},
{"title":"[P0] Relative commondir escapes the check","body":"A crafted file escapes.","confidence_score":0.91,"priority":0,"code_location":{"absolute_file_path":"/w/sandbox/worktree.go","line_range":{"start":88,"end":104}}},
{"title":"Mention the default","body":"Say it.","confidence_score":0.9,"code_location":{"absolute_file_path":"/elsewhere/d.go","line_range":{"start":7,"end":7}}}],
"overall_correctness":"patch is incorrect","overall_explanation":"Two bugs, one blocking.","overall_confidence_score":0.82}`)},
	}
}

// TestReviewPrint writes a finished review: the counts by priority, the
// verdict, the confidence, the time, the reviewer, and the tokens; the
// explanation; the findings sorted by priority and confidence, a
// low-confidence one marked, the places relative to the workspace. Plain
// off a terminal, in the TUI's colors on one, the same text either way.
func TestReviewPrint(t *testing.T) {
	o := finishedReview()
	var plain bytes.Buffer
	o.print(colorprofile.NewWriter(&plain, nil))
	reviewGolden(t, "review-plain", plain.String())
	assert.NotContains(t, plain.String(), "\x1b", "no escape off a terminal")

	var color bytes.Buffer
	w := colorprofile.NewWriter(&color, nil)
	w.Profile = colorprofile.TrueColor
	o.print(w)
	reviewGolden(t, "review-color", color.String())
	assert.Contains(t, color.String(), "\x1b[")
	assert.Equal(t, plain.String(), ansi.Strip(color.String()), "the same text")
}

// TestReviewSteps prints the reviewer's calls shaped as the TUI's tool
// lines, and each one that failed once its end and its output are in,
// in either order, with why; a search that exits 1 with no output found
// nothing; a failure whose output never came is printed when the review
// ends.
func TestReviewSteps(t *testing.T) {
	var buf bytes.Buffer
	o := reviewOutput{progress: newPrinter(&buf, false), env: cmdparse.Env{Workspace: "/w"}}
	call := func(id, command string) {
		o.step(core.ToolCalled{CallID: id, Name: "Bash", Arguments: `{"command":` + strconv.Quote(command) + `}`})
	}
	end := func(id, detail string, d time.Duration) {
		o.step(core.ToolFinished{CallID: id, OK: detail == "exit 0", Detail: detail, Duration: d})
	}
	output := func(id, text string) { o.step(engine.ToolOutput{CallID: id, Output: text}) }
	call("1", "sed -n 1,40p /w/a.go")
	end("1", "exit 0", 0)
	call("2", "rg -n Foo /w/internal") // found nothing: end, then empty output
	end("2", "exit 1", 0)
	output("2", "")
	call("3", "rg -n Bar /w") // found nothing: empty output, then end
	output("3", "")
	end("3", "exit 1", 0)
	call("4", "go test ./...") // its output comes after its end
	end("4", "exit 1", 4*time.Second)
	output("4", "--- FAIL: TestX\nFAIL\tpkg\n\x1b[0m\n")
	call("5", "find /w/missing -name '*.go'")
	output("5", "find: /w/missing: No such file or directory\n")
	end("5", "exit 1", 0)
	call("6", "cat /w/b.go") // no output ever came
	end("6", "exit 2", 0)
	o.flush()
	var lines []string
	for l := range strings.Lines(buf.String()) {
		lines = append(lines, strings.TrimRight(l[strings.Index(l, "]")+2:], "\n"))
	}
	assert.Equal(t, []string{
		"  → READ    a.go:1-40",
		"  → SEARCH  Foo in internal",
		"  → SEARCH  Bar in .",
		"  → RAN     go test ./...",
		"  ✗ RAN     go test ./...  (exit 1, 4.0s): FAIL pkg",
		"  → SEARCH  *.go in missing",
		"  ✗ SEARCH  *.go in missing  (exit 1, 0.0s): find: /w/missing: No such file or directory",
		"  → READ    b.go",
		"  ✗ READ    b.go  (exit 2, 0.0s)",
	}, lines)
	assert.Empty(t, o.calls, "every ended call is settled")
}

// TestReviewPrintEmpty says the reviewer gave nothing, as Codex's text
// does, when it answered with neither an explanation nor findings.
func TestReviewPrintEmpty(t *testing.T) {
	var buf bytes.Buffer
	o := reviewOutput{started: session.ReviewStarted{Hint: "current changes"}, finished: session.ReviewFinished{Output: codereview.Parse("{}")}}
	o.print(colorprofile.NewWriter(&buf, nil))
	assert.Equal(t, "REVIEW  current changes\n        no findings · 0s\n\n"+codereview.FallbackMessage+"\n", buf.String())
}

// TestReviewWriter: NO_COLOR set to anything turns off every style, bold
// too, even on a terminal that has colors.
func TestReviewWriter(t *testing.T) {
	env := []string{"TERM=xterm-256color", "COLORTERM=truecolor", "CLICOLOR_FORCE=1"}
	assert.NotEqual(t, colorprofile.NoTTY, reviewWriter(&bytes.Buffer{}, env).Profile, "a forced color terminal")
	for _, v := range []string{"1", "yes"} {
		var buf bytes.Buffer
		w := reviewWriter(&buf, append(env, "NO_COLOR="+v))
		assert.Equal(t, colorprofile.NoTTY, w.Profile, v)
		_, _ = w.Write([]byte("\x1b[1mbold\x1b[m"))
		assert.Equal(t, "bold", buf.String(), v)
	}
}
