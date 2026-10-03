package review_test

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/review"
)

var update = flag.Bool("update", false, "rewrite golden files")

func TestPrompt_Golden(t *testing.T) {
	req := sample()
	req.Transcript.Entries = append(req.Transcript.Entries, review.Entry{Kind: review.EntryUser, Text: "Also run the tests before pushing."})
	req.Action.Denied = "fatal: unable to access 'https://github.com/o/r/': Could not resolve host: github.com"
	req.Action.Rule = `prefix_rule(pattern=["git", "push"], decision="prompt")`
	delta := req
	delta.Transcript.Entries = append(delta.Transcript.Entries, review.Entry{Kind: review.EntryCall, Tool: "Bash", Text: `{"command":"go test ./..."}`})
	got := "=== instructions ===\n" + review.Instructions("", false) + "=== user ===\n" + review.Render(req, review.DefaultLimits) +
		"=== delta ===\n" + review.RenderDelta(delta, 4, review.DefaultLimits)

	path := filepath.Join("testdata", "prompt.golden")
	if *update {
		require.NoError(t, os.WriteFile(path, []byte(got), 0o600))
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, string(want), got)
}

func TestPrompt_OneNumberedTranscript(t *testing.T) {
	out := review.Render(sample(), review.DefaultLimits)

	transcript := section(t, out, "TRANSCRIPT")
	assert.Equal(t, "[1] user: Fix the typo and push it to my feature branch.\n"+
		`[2] tool Bash call: {"command":"git commit -am typo"}`+"\n"+
		"[3] tool Bash result: exit 0\n", transcript)
	assert.Contains(t, out, "user's own messages and answers: trusted content")
	assert.Contains(t, section(t, out, "APPROVAL REQUEST"), `"sandbox_permissions": "require_escalated"`)
}

func TestPrompt_DeltaHasOnlyTheNewEntries(t *testing.T) {
	req := sample()
	req.Transcript.Entries = append(req.Transcript.Entries, review.Entry{Kind: review.EntryUser, Text: "push it now"})

	out := review.RenderDelta(req, 3, review.DefaultLimits)

	assert.Contains(t, out, "added since your last approval assessment. Continue the same review conversation.")
	assert.Equal(t, "[4] user: push it now\n", section(t, out, "TRANSCRIPT DELTA"))
	assert.Contains(t, section(t, out, "TRANSCRIPT DELTA"), "[4] user: push it now")
	assert.Equal(t, "<no retained transcript delta entries>\n", section(t, review.RenderDelta(req, 4, review.DefaultLimits), "TRANSCRIPT DELTA"))
}

func TestPrompt_DroppedEntriesAreCounted(t *testing.T) {
	first := review.Entry{Kind: review.EntryUser, Text: "the task"}
	req := review.Request{Transcript: review.Transcript{
		Start: 10, First: &first, FirstAt: 0,
		Entries: []review.Entry{{Kind: review.EntryResult, Tool: "Bash", Text: "exit 0"}},
	}}

	full := section(t, review.Render(req, review.DefaultLimits), "TRANSCRIPT")
	assert.Equal(t, "<omitted transcript_entries=\"9\" reason=\"budget\" />\n[1] user: the task\n[11] tool Bash result: exit 0\n", full)
	delta := section(t, review.RenderDelta(req, 7, review.DefaultLimits), "TRANSCRIPT DELTA")
	assert.Equal(t, "<omitted transcript_entries=\"3\" reason=\"budget\" />\n[11] tool Bash result: exit 0\n", delta)
}

func TestPrompt_ToolsChangeTheInvestigation(t *testing.T) {
	without, with := review.Instructions("", false), review.Instructions("", true)

	assert.Contains(t, without, "You cannot run commands or call tools.")
	assert.NotContains(t, without, "exec_command")
	assert.NotContains(t, without, "{{")
	assert.Contains(t, with, "attempt a read-only inspection of the target path first")
	assert.Contains(t, with, "you can only run read-only commands, with `exec_command`")
	assert.NotContains(t, with, "You cannot run commands")
	assert.NotContains(t, with, "{{")
}

func TestPrompt_CustomPolicyReplacesTheDefault(t *testing.T) {
	got := review.Instructions("Never allow pushes to main.", false)

	assert.Contains(t, got, "# Security Policy\nNever allow pushes to main.\n")
	assert.NotContains(t, got, "{{ tenant_policy_config }}")
	assert.NotContains(t, got, "Data Exfiltration")
	assert.Contains(t, review.Instructions("", false), "### Data Exfiltration")
}

func TestPrompt_Budget(t *testing.T) {
	limits := review.Limits{UserMessageBytes: 40, UserBytes: 120, CallBytes: 30, CallsBytes: 1000, Calls: 2, ActionBytes: 40}
	user := func(s string) review.Entry { return review.Entry{Kind: review.EntryUser, Text: s} }
	call := func(name, args string) review.Entry {
		return review.Entry{Kind: review.EntryCall, Tool: name, Text: args}
	}
	req := review.Request{
		Transcript: review.Transcript{Entries: []review.Entry{
			user("the task"), user("second"), user("third"), user("fourth"), user("fifth " + strings.Repeat("x", 100)),
			call("a", "{}"), call("b", "{}"), call("c", strings.Repeat("y", 100)),
		}},
		Action: review.Action{Command: strings.Repeat("z", 100)},
	}
	out := section(t, review.Render(req, limits), "TRANSCRIPT")

	assert.Contains(t, out, "[1] user: the task", "the first message is kept")
	assert.Contains(t, out, "[5] user: fifth ")
	assert.Contains(t, out, `<truncated omitted_approx_tokens="17" />`)
	assert.NotContains(t, out, "second", "the middle messages are dropped")
	assert.Contains(t, out, `<omitted transcript_entries="4" reason="budget" />`)
	assert.NotContains(t, out, "tool a call")
	assert.Contains(t, out, "[7] tool b call")
	assert.Contains(t, out, "[8] tool c call: yyyyyyyyyyyyyyy<truncated")
	assert.Contains(t, section(t, review.Render(req, limits), "APPROVAL REQUEST"), `zzzzzzzzzzzzzzzzzzzz<truncated omitted_approx_tokens=\"15\" />zzzz`)
}

// section is the text between ">>> NAME START" and ">>> NAME END".
func section(t *testing.T, out, name string) string {
	t.Helper()
	_, rest, ok := strings.Cut(out, ">>> "+name+" START\n")
	require.True(t, ok, name)
	body, _, ok := strings.Cut(rest, ">>> "+name+" END\n")
	require.True(t, ok, name)

	return body
}
