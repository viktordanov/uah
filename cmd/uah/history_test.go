package main_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/testing/fakellm"
)

const converted = "11111111-2222-4333-8444-555555555555"

// writeConverted writes a session as another program converts one from
// another harness: a session file with a message and an answer, no run
// record, and, with workspace set, a sidecar.
func writeConverted(t *testing.T, stateDir, id, workspace string) {
	t.Helper()
	lines := []string{
		fmt.Sprintf(`{"type":"session","data":{"Version":2,"Session":{"ID":%q,"CreatedAt":"2025-03-01T10:00:00Z"}}}`, id),
		`{"type":"item","data":{"Item":{"Sequence":1,"RecordedAt":"2025-03-01T10:00:01Z","Kind":"input","Data":{"ID":"in-1","Kind":"external","Payload":"How do I rename a Go package?"}}}}`,
		`{"type":"item","data":{"Item":{"Sequence":2,"RecordedAt":"2025-03-01T10:00:02Z","Kind":"turn","Data":{"ID":"turn-1","PreviousTurnID":"","Type":"regular"}}}}`,
		`{"type":"item","data":{"Item":{"Sequence":3,"RecordedAt":"2025-03-01T10:00:09Z","Kind":"model_response","Data":{"TurnID":"turn-1","Response":{"ID":"resp-1","Stop":"complete","Output":[{"ProviderID":"msg-1","Type":"message","Data":{"Role":"assistant","Text":"Use gopls rename on the package clause.","Phase":"final_answer"}}],"Usage":{"InputTokens":10,"CachedInputTokens":0,"OutputTokens":5,"ReasoningTokens":0}}}}}}`,
	}
	writeFile(t, filepath.Join(stateDir, "sessions", id+".session.jsonl"), strings.Join(lines, "\n")+"\n")
	if workspace != "" {
		writeFile(t, filepath.Join(stateDir, "sessions", id+".uah.json"),
			fmt.Sprintf(`{"source":"tui","created":"2025-03-01T10:00:00Z","workspace":%q,"first_prompt":"How do I rename a Go package?"}`, workspace))
	}
}

type listed struct {
	ID, Workspace, FirstPrompt, Started, LastActivity, Status, Source string
	Runs                                                            int
	LastSequence                                                    uint64
}

func listAll(t *testing.T, env []string) []listed {
	t.Helper()
	res := uahWith(t, env, "", "sessions", "--json", "--all")
	require.Equal(t, 0, res.code, res.stderr)
	var out []listed
	require.NoError(t, json.Unmarshal([]byte(res.stdout), &out), res.stdout)

	return out
}

// TestConvertedSession: a session file with history and no run record is
// listed, shown from its session file, resumed, listed once from its run
// after that, and removed, as a terminal host checks a session it converted.
func TestConvertedSession(t *testing.T) {
	t.Parallel()
	llm := fakellm.New(t, fakellm.Reply{Text: "carried on"})
	e, env := modelEnv(t, llm)
	writeConverted(t, e.StateDir, converted, e.Workspace)

	got := listAll(t, env)
	require.Len(t, got, 1)
	assert.Equal(t, listed{
		ID: converted, Workspace: e.Workspace, FirstPrompt: "How do I rename a Go package?", Source: "tui",
		Started: "2025-03-01T10:00:00Z", LastActivity: "2025-03-01T10:00:09Z", LastSequence: 3,
	}, got[0], "its times are the sidecar's and the last item's")

	res := uahWith(t, env, "", "sessions", "show", "--json", converted)
	require.Equal(t, 0, res.code, res.stderr)
	var shown struct {
		Session struct{ ID string }
		Runs    []json.RawMessage `json:"runs"`
		History struct {
			Messages []string `json:"messages"`
			Answer   string   `json:"answer"`
		} `json:"history"`
	}
	require.NoError(t, json.Unmarshal([]byte(res.stdout), &shown), res.stdout)
	assert.Equal(t, converted, shown.Session.ID)
	assert.NotNil(t, shown.Runs)
	assert.Empty(t, shown.Runs)
	assert.Equal(t, []string{"How do I rename a Go package?"}, shown.History.Messages)
	assert.Equal(t, "Use gopls rename on the package clause.", shown.History.Answer)

	res = uahWith(t, env, "", "sessions", "show", converted[:8])
	require.Equal(t, 0, res.code, res.stderr)
	assert.Contains(t, res.stdout, "› How do I rename a Go package?\n")
	assert.Contains(t, res.stdout, "✓ Use gopls rename on the package clause.\n")

	res = uahWith(t, env, "", "exec", "-q", "-C", e.Workspace, "--session", converted, "now do it")
	require.Equal(t, 0, res.code, res.stderr)
	assert.Equal(t, "carried on\n", res.stdout)
	reqs := llm.Requests()
	require.Len(t, reqs, 1)
	assert.Equal(t, []string{"How do I rename a Go package?", "now do it"}, reqs[0].UserTexts, "the model gets the converted history")
	got = listAll(t, env)
	require.Len(t, got, 1, "listed once, from its run")
	assert.Equal(t, 1, got[0].Runs)
	res = uahWith(t, env, "", "sessions", "show", converted)
	require.Equal(t, 0, res.code, res.stderr)
	before, after, ok := strings.Cut(res.stdout, "── run ")
	require.True(t, ok, res.stdout)
	assert.Contains(t, before, "── history from before uah ran it", "the converted history stays, before the first run")
	assert.Contains(t, before, "✓ Use gopls rename on the package clause.\n")
	assert.Contains(t, after, "› now do it\n")
	assert.Contains(t, after, "✓ carried on\n")
	res = uahWith(t, env, "", "sessions", "show", "--json", converted)
	require.Equal(t, 0, res.code, res.stderr)
	require.NoError(t, json.Unmarshal([]byte(res.stdout), &shown), res.stdout)
	assert.Len(t, shown.Runs, 1)
	assert.Equal(t, "Use gopls rename on the package clause.", shown.History.Answer)

	res = uahWith(t, env, "", "sessions", "rm", converted)
	require.Equal(t, 0, res.code, res.stderr)
	assert.Empty(t, listAll(t, env))
	assert.NoFileExists(t, filepath.Join(e.StateDir, "sessions", converted+".session.jsonl"))
}

// TestConvertedSession_NoSidecar lists a converted session with no
// sidecar from its session file, and removes it.
func TestConvertedSession_NoSidecar(t *testing.T) {
	t.Parallel()
	e, env := fakeEnv(t)
	writeConverted(t, e.StateDir, converted, "")

	got := listAll(t, env)
	require.Len(t, got, 1)
	assert.Equal(t, listed{
		ID: converted, FirstPrompt: "How do I rename a Go package?",
		Started: "2025-03-01T10:00:00Z", LastActivity: "2025-03-01T10:00:09Z", LastSequence: 3,
	}, got[0], "the header's creation time and the first message")
	res := uahWith(t, env, "", "sessions", "show", converted)
	require.Equal(t, 0, res.code, res.stderr)
	assert.Contains(t, res.stdout, "✓ Use gopls rename on the package clause.\n")

	res = uahWith(t, env, "", "sessions", "rm", converted)
	require.Equal(t, 0, res.code, res.stderr)
	assert.Empty(t, listAll(t, env))
}

// TestConvertedSession_PickerRules: --last resumes a converted session,
// as it has history, while a session that never ran and a subagent stay
// out.
func TestConvertedSession_PickerRules(t *testing.T) {
	t.Parallel()
	e, env := fakeEnv(t, fakellm.Reply{Text: "resumed"})
	writeConverted(t, e.StateDir, converted, e.Workspace)
	writeConverted(t, e.StateDir, "subagent-"+converted, "") // newer by name only: same times
	writeFile(t, filepath.Join(e.StateDir, "sessions", "99999999-2222-4333-8444-555555555555.uah.json"),
		fmt.Sprintf(`{"source":"tui","created":"2026-01-01T00:00:00Z","workspace":%q}`, e.Workspace)) // never ran, newer

	assert.Len(t, listAll(t, env), 3, "uah sessions lists all three")
	res := uahWith(t, env, "", "exec", "-q", "-C", e.Workspace, "--last", "go on")
	require.Equal(t, 0, res.code, res.stderr)
	assert.Equal(t, "resumed\n", res.stdout)
	for _, in := range listAll(t, env) {
		if in.ID == converted {
			assert.Equal(t, 1, in.Runs, "--last took the converted session, not the never-run one")
		}
	}
	_, err := os.Stat(filepath.Join(e.StateDir, "sessions", "99999999-2222-4333-8444-555555555555.session.jsonl"))
	assert.True(t, os.IsNotExist(err), "the never-run session stays as it was")
}

// TestExecLast_SkipsSubagent: `uah exec --last` continues the parent, not
// a newer subagent's or reviewer's session in the same directory.
func TestExecLast_SkipsSubagent(t *testing.T) {
	t.Parallel()
	llm := fakellm.New(t, fakellm.Reply{Text: "one"}, fakellm.Reply{Text: "two"})
	e, env := modelEnv(t, llm)
	res := uahWith(t, env, "", "exec", "-C", e.Workspace, "first question")
	require.Equal(t, 0, res.code, res.stderr)
	parent := listAll(t, env)[0].ID
	sub := "subagent-" + converted
	lines := []string{
		fmt.Sprintf(`{"type":"session","data":{"Version":2,"Session":{"ID":%q,"CreatedAt":"2030-03-01T10:00:00Z"}}}`, sub),
		`{"type":"item","data":{"Item":{"Sequence":1,"RecordedAt":"2030-03-01T10:00:01Z","Kind":"input","Data":{"ID":"in-1","Kind":"external","Payload":"child task"}}}}`,
	}
	writeFile(t, filepath.Join(e.StateDir, "sessions", sub+".session.jsonl"), strings.Join(lines, "\n")+"\n")
	writeFile(t, filepath.Join(e.StateDir, "sessions", sub+".uah.json"),
		fmt.Sprintf(`{"source":"subagent","parent":%q,"created":"2030-03-01T10:00:00Z","workspace":%q}`, parent, e.Workspace))

	res = uahWith(t, env, "", "exec", "--last", "-C", e.Workspace, "follow up")
	require.Equal(t, 0, res.code, res.stderr)
	for _, in := range listAll(t, env) {
		switch in.ID {
		case parent:
			assert.Equal(t, 2, in.Runs, "--last continued the parent")
		case sub:
			assert.Zero(t, in.Runs, "the newer subagent stays as it was")
		}
	}
}
