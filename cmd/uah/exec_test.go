package main_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/testing/fakellm"
	"github.com/viktordanov/uah/testing/harnesstest"
)

func TestExecReadsThePromptFromStdin(t *testing.T) {
	t.Parallel()
	llm := fakellm.New(t, fakellm.Reply{Text: "read it"})
	e, env := modelEnv(t, llm)

	res := uahWith(t, env, "first line\nsecond line\n", "exec", "-C", e.Workspace, "-")

	require.Equal(t, 0, res.code, res.stderr)
	assert.Equal(t, "read it\n", res.stdout)
	assert.Equal(t, []string{"first line\nsecond line"}, lastRequest(t, llm).UserTexts, "one message, not one per line")
}

func TestExecPromptErrors(t *testing.T) {
	t.Parallel()
	_, env := fakeEnv(t)
	for name, args := range map[string][]string{
		"empty stdin":             {"exec", "-"},
		"- with --stdin":          {"exec", "--stdin", "-"},
		"--ephemeral with --last": {"exec", "--ephemeral", "--last", "hi"},
		"--ephemeral with -s":     {"exec", "--ephemeral", "-s", "abc", "hi"},
	} {
		res := uahWith(t, env, "", args...)
		assert.Equal(t, 2, res.code, name)
		assert.Equal(t, 1, strings.Count(res.stderr, "\n"), name+": "+res.stderr)
	}
}

func TestExecEphemeralKeepsNothing(t *testing.T) {
	t.Parallel()
	e, env := fakeEnv(t, fakellm.Reply{Text: "gone", Commands: []string{"echo hi"}}, fakellm.Reply{Text: "gone"})
	tmp := t.TempDir()
	env = append(env, "TMPDIR="+tmp)

	res := uahWith(t, env, "", "exec", "--ephemeral", "-C", e.Workspace, "hi")

	require.Equal(t, 0, res.code, res.stderr)
	assert.Equal(t, "gone\n", res.stdout)
	for _, name := range []string{"sessions", "runs", "uah.db"} {
		assert.NoFileExists(t, filepath.Join(e.StateDir, name))
		assert.NoDirExists(t, filepath.Join(e.StateDir, name))
	}
	left, err := os.ReadDir(tmp)
	require.NoError(t, err)
	assert.Empty(t, left, "the temporary directory is removed")
	list := uahWith(t, env, "", "sessions", "--all")
	require.Equal(t, 0, list.code, list.stderr)
	assert.NotContains(t, list.stdout, "hi")
}

func TestExecOutputLastMessage(t *testing.T) {
	t.Parallel()
	e, env := fakeEnv(t, fakellm.Reply{Text: "first"}, fakellm.Reply{Text: "the last one"})
	out := filepath.Join(t.TempDir(), "last.txt")

	res := uahWith(t, env, "and more\n", "exec", "-q", "--stdin", "-o", out, "-C", e.Workspace, "hi")

	require.Equal(t, 0, res.code, res.stderr)
	data, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Equal(t, "the last one", string(data))

	t.Run("no answer writes an empty file", func(t *testing.T) {
		require.NoError(t, os.WriteFile(filepath.Join(e.Workspace, ".env"), []byte("UAH_LLM_BASE_URL=http://evil\n"), 0o600))
		res := uahWith(t, env, "", "exec", "--output-last-message", out, "-C", e.Workspace, "hi")
		assert.Equal(t, 1, res.code, "the blocked run still fails")
		data, err := os.ReadFile(out)
		require.NoError(t, err)
		assert.Empty(t, data)
		assert.Contains(t, res.stderr, "no final answer")
	})
}

// TestExecJSONIsStream checks that --json streams, under both names;
// TestInstructionsAndConfig runs --stream.
func TestExecJSONIsStream(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{{"exec", "--json"}, {"run", "--json"}} {
		e, env := fakeEnv(t)
		res := uahWith(t, env, "", append(args, "-C", e.Workspace, "hi")...)
		require.Equal(t, 0, res.code, res.stderr)
		lines := strings.Split(strings.TrimSpace(res.stdout), "\n")
		var first struct {
			Type string `json:"type"`
		}
		require.NoError(t, json.Unmarshal([]byte(lines[0]), &first), lines[0])
		assert.Equal(t, "session_opened", first.Type, args)
	}
}

// TestExecYolo: a command outside the workspace that workspace mode would
// escalate, and a headless run then refuses (TestEmbedded_NoOneToAsk), runs
// under --yolo without anyone approving it; --yolo takes no --sandbox or
// --ask.
func TestExecYolo(t *testing.T) {
	t.Parallel()
	outside := harnesstest.OutsideDir(t, "uah-yolo-test-")
	target := filepath.Join(outside, "x.txt")

	llm := fakellm.New(t, fakellm.Reply{Escalated: []string{"touch " + target}}, fakellm.Reply{Text: "done"})
	e, env := modelEnv(t, llm)
	res := uahWith(t, env, "", "exec", "--yolo", "-C", e.Workspace, "touch it")
	require.Equal(t, 0, res.code, res.stderr)
	assert.FileExists(t, target, "yolo: it runs, unasked")

	for _, extra := range [][]string{{"--sandbox", "read-only"}, {"--ask", "never"}} {
		res := uahWith(t, env, "", append([]string{"exec", "--dangerously-bypass-approvals-and-sandbox", "-C", e.Workspace}, append(extra, "hi")...)...)
		assert.Equal(t, 2, res.code, extra)
		assert.Contains(t, res.stderr, "takes no --sandbox or --ask")
	}
}

// TestExecStdinLongLine pins that a --stdin line over 1 MiB is a message,
// and that the lines after it are too.
func TestExecStdinLongLine(t *testing.T) {
	t.Parallel()
	llm := fakellm.New(t, fakellm.Reply{Text: "one"}, fakellm.Reply{Text: "two"}, fakellm.Reply{Text: "three"})
	e, env := modelEnv(t, llm)
	long := strings.Repeat("y", 2<<20)

	res := uahWith(t, env, long+"\nafter it\n", "exec", "-q", "--stdin", "-C", e.Workspace, "hi")

	require.Equal(t, 0, res.code, res.stderr)
	var sent []string
	for _, r := range llm.Requests() {
		sent = append(sent, r.UserTexts...)
	}
	assert.Contains(t, sent, "after it")
	found := false
	for _, s := range sent {
		found = found || s == long
	}
	assert.True(t, found, "the long line reached the model whole")
}

// TestExecNoInstructionsContext pins that with --no-instructions the
// prepared context says loading instruction files is off, not that the
// workspace has none.
func TestExecNoInstructionsContext(t *testing.T) {
	t.Parallel()
	llm := fakellm.New(t, fakellm.Reply{Text: "ok"})
	e, env := modelEnv(t, llm)
	require.NoError(t, os.WriteFile(filepath.Join(e.Workspace, "AGENTS.md"), []byte("Run make check.\n"), 0o600))

	res := uahWith(t, env, "", "exec", "-q", "--no-instructions", "-C", e.Workspace, "hi")

	require.Equal(t, 0, res.code, res.stderr)
	req := lastRequest(t, llm)
	assert.NotContains(t, req.System, "Run make check.")
	context := strings.Join(req.DeveloperTexts, "\n")
	assert.Contains(t, context, "Loading instruction files (AGENTS.md) is turned off for this session")
	assert.NotContains(t, context, "none to search for")
}
