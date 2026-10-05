package main

import (
	"bytes"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/cmdparse"
	"github.com/viktordanov/uah/internal/codereview"
	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/session"
)

// TestReviewPrint writes a finished review: the findings sorted by
// priority and confidence, a low-confidence one marked, the places
// relative to the workspace; in color on a terminal, plain otherwise.
func TestReviewPrint(t *testing.T) {
	t0 := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	o := reviewOutput{
		env:     cmdparse.Env{Workspace: "/w"},
		started: session.ReviewStarted{At: t0, Hint: "current changes", Model: "gpt-6-astra", Effort: "high"},
		finished: session.ReviewFinished{At: t0.Add(130 * time.Second), Tokens: core.Tokens{InputTokens: 138_000, OutputTokens: 2_100}, Output: codereview.Parse(`{"findings":[
{"title":"[P3] Name it","body":"A name.","confidence_score":0.6,"code_location":{"absolute_file_path":"/w/a.go","line_range":{"start":1,"end":1}}},
{"title":"[P1] Maybe","body":"Perhaps.\n\nOr not.","confidence_score":0.3,"code_location":{"absolute_file_path":"/w/b.go","line_range":{"start":2,"end":3}}},
{"title":"[P1] Check the error","body":"It is dropped.","confidence_score":0.82,"code_location":{"absolute_file_path":"/w/c.go","line_range":{"start":4,"end":9}}}],
"overall_correctness":"patch is incorrect","overall_explanation":"Two bugs.","overall_confidence_score":0.7}`)},
	}
	var plain bytes.Buffer
	o.print(colorprofile.NewWriter(&plain, nil))
	assert.Equal(t, "REVIEW  current changes\n"+
		"        3 findings (2 P1 · 1 P3) · ✗ patch is incorrect · confidence 70% · 2m 10s · gpt-6-astra high · 140.1k tokens\n\n"+
		"Two bugs.\n\n"+
		"P1  Check the error  82%\n    c.go:4-9\n    It is dropped.\n\n"+
		"P1  Maybe  30% · low confidence\n    b.go:2-3\n    Perhaps.\n\n    Or not.\n\n"+
		"P3  Name it  60%\n    a.go:1\n    A name.\n", plain.String())

	var color bytes.Buffer
	w := colorprofile.NewWriter(&color, nil)
	w.Profile = colorprofile.TrueColor
	o.print(w)
	assert.NotEqual(t, color.String(), plain.String(), "colors on a terminal")
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
