package main_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/testing/fakellm"
)

// reviewAnswer is a reviewer's answer in Codex's format.
const reviewAnswer = `{"findings":[{"title":"[P1] Check the error","body":"It is dropped.","confidence_score":0.8,"priority":1,
"code_location":{"absolute_file_path":"/w/a.go","line_range":{"start":3,"end":4}}}],
"overall_correctness":"patch is incorrect","overall_explanation":"One bug.","overall_confidence_score":0.7}`

// branchRepo makes dir a git repository with a commit on main and one on
// feature, checked out, and returns main's commit.
func branchRepo(t *testing.T, dir string) string {
	t.Helper()
	git := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@localhost"}, args...)...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))

		return strings.TrimSpace(string(out))
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0o644))
	git("init", "-q", "-b", "main")
	git("add", "-A")
	git("commit", "-q", "-m", "first")
	base := git("rev-parse", "HEAD")
	git("checkout", "-q", "-b", "feature")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n\nfunc A() {}\n"), 0o644))
	git("commit", "-q", "-am", "add A")

	return base
}

// TestReviewCommand runs `uah review --base main` headless: the reviewer
// gets Codex's branch prompt with the merge base, the review goes to
// stdout with its counts, verdict, confidence, and sorted findings (plain
// text off a terminal), and its shaped steps, verdict, model, effort, and
// tokens to stderr; -o writes Codex's text, and --json prints the
// findings with what the review ran on and used.
func TestReviewCommand(t *testing.T) {
	t.Parallel()
	llm := fakellm.New(t)
	llm.Route("Review the code changes against the base branch 'main'",
		fakellm.Reply{Commands: []string{"echo looked"}}, fakellm.Reply{Text: reviewAnswer},
		fakellm.Reply{Commands: []string{"echo again"}}, fakellm.Reply{Text: reviewAnswer},
	)
	e, env := modelEnv(t, llm)
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	base := branchRepo(t, e.Workspace)
	out := filepath.Join(t.TempDir(), "review.md")

	res := uahWith(t, env, "", "review", "-C", e.Workspace, "-e", "xhigh", "--base", "main", "-o", out)

	require.Equal(t, 0, res.code, res.stderr)
	assert.Regexp(t, `^REVIEW  changes against 'main'\n        1 finding \(1 P1\) · ✗ patch is incorrect · confidence 70% · [0-9]+s · \S+ xhigh · [0-9.]+k? tokens\n\n`+
		`One bug\.\n\nP1  Check the error  80%\n    /w/a\.go:3-4\n    It is dropped\.\n$`, res.stdout, "plain text, not a terminal")
	data, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Equal(t, "One bug.\n\nReview comment:\n\n- [P1] Check the error — /w/a.go:3-4\n  It is dropped.", string(data), "-o writes Codex's text")
	assert.Contains(t, res.stderr, "uah review · changes against 'main' · ")
	assert.Contains(t, res.stderr, "→ RAN     echo looked", "the reviewer's steps, shaped")
	assert.Regexp(t, `review: 1 finding \(1 P1\) · ✗ patch is incorrect · confidence 70% · [0-9.]+s · \S+, effort xhigh · [0-9,]+ in \([0-9,]+ cached\) · [0-9,]+ out tokens`, res.stderr)
	req := lastRequest(t, llm)
	require.NotEmpty(t, req.UserTexts)
	assert.Contains(t, req.UserTexts[0], "The merge base commit for this comparison is "+base)
	assert.Equal(t, "xhigh", req.Effort)

	res = uahWith(t, env, "", "review", "-C", e.Workspace, "--json", "-q", "--base", "main")

	require.Equal(t, 0, res.code, res.stderr)
	assert.Empty(t, res.stderr)
	var doc struct {
		Target, Status, Model, Effort string
		Usage                         struct{ Input, Output int64 }
		Findings                      []struct{ Title string }
		Verdict                       string `json:"overall_correctness"`
	}
	require.NoError(t, json.Unmarshal([]byte(res.stdout), &doc), res.stdout)
	assert.Equal(t, "changes against 'main'", doc.Target)
	assert.Equal(t, "ok", doc.Status)
	assert.NotEmpty(t, doc.Model)
	assert.Equal(t, "high", doc.Effort)
	assert.Positive(t, doc.Usage.Input)
	assert.Positive(t, doc.Usage.Output)
	require.Len(t, doc.Findings, 1)
	assert.Equal(t, "[P1] Check the error", doc.Findings[0].Title)
	assert.Equal(t, "patch is incorrect", doc.Verdict)
}

// TestReviewCommandUsage refuses what Codex refuses: no target, two
// targets, --title without --commit, and empty instructions; and a session
// to resume.
func TestReviewCommandUsage(t *testing.T) {
	t.Parallel()
	_, env := fakeEnv(t)
	for name, args := range map[string][]string{
		"no target":        {"review"},
		"two targets":      {"review", "--uncommitted", "--base", "main"},
		"instructions too": {"review", "--commit", "abc", "look at errors"},
		"title alone":      {"review", "--title", "Fix it", "--uncommitted"},
		"empty stdin":      {"review", "-"},
		"a session":        {"review", "-s", "abc", "--uncommitted"},
	} {
		res := uahWith(t, env, "", args...)
		assert.Equal(t, 2, res.code, name)
		assert.Equal(t, 1, strings.Count(res.stderr, "\n"), name+": "+res.stderr)
	}
}
