package session

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uagent/core"
	"github.com/viktordanov/uagent/harness"
)

// sessionFile writes a session file with an item per kind given, numbered
// from 1: "msg" an external input, "fork" a fork item.
func sessionFile(t *testing.T, stateDir, id string, kinds ...string) {
	t.Helper()
	lines := []string{fmt.Sprintf(`{"type":"session","data":{"Version":2,"Session":{"ID":%q,"CreatedAt":"2025-03-01T10:00:00Z"}}}`, id)}
	for i, k := range kinds {
		data := fmt.Sprintf(`"Kind":"input","Data":{"ID":"in-%d","Kind":"external","Payload":"message %d"}`, i+1, i+1)
		if k == "fork" {
			data = `"Kind":"fork","Data":{"ParentID":"p","PreviousTurnID":""}`
		}
		lines = append(lines, fmt.Sprintf(`{"type":"item","data":{"Item":{"Sequence":%d,"RecordedAt":"2025-03-01T10:00:%02dZ",%s}}}`, i+1, i+1, data))
	}
	dir := filepath.Join(stateDir, "sessions")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, id+sessionFileSuffix), []byte(strings.Join(lines, "\n")+"\n"), 0o600))
}

// runFrom is a loaded run whose events file starts at the item first.
func runFrom(t *testing.T, first uint64) LoadedRun {
	t.Helper()
	dir := t.TempDir()
	line := fmt.Sprintf(`{"Sequence":%d,"RecordedAt":"2026-10-17T10:00:00Z","Kind":"input","Data":{"ID":"x","Kind":"external","Payload":"later"}}`+"\n", first)
	require.NoError(t, os.WriteFile(filepath.Join(dir, harness.EventsFile), []byte(line), 0o600))

	return LoadedRun{Record: harness.RunRecord{Dir: dir, Result: core.Result{Request: core.Request{RunID: "r1"}}}}
}

func messages(r LoadedRun) []string {
	var out []string
	for _, e := range r.Events {
		if m, ok := e.(core.UserMessage); ok {
			out = append(out, m.Text)
		}
	}

	return out
}

func TestWithPrior(t *testing.T) {
	br := bufio.NewReader(nil)
	state := t.TempDir()
	sessionFile(t, state, "s1", "msg", "msg", "msg")

	runs := withPrior(br, state, "s1", nil)
	require.Len(t, runs, 1, "no run: the whole file is history")
	assert.True(t, runs[0].IsPrior())
	assert.Equal(t, []string{"message 1", "message 2", "message 3"}, messages(runs[0]))

	runs = withPrior(br, state, "s1", []LoadedRun{runFrom(t, 3)})
	require.Len(t, runs, 2)
	assert.Equal(t, []string{"message 1", "message 2"}, messages(runs[0]), "the items before the first run's")
	assert.False(t, runs[1].IsPrior())

	runs = withPrior(br, state, "s1", []LoadedRun{runFrom(t, 1)})
	assert.Len(t, runs, 1, "a session uah ran from its first item has no history before it")

	empty := runFrom(t, 0)
	require.NoError(t, os.WriteFile(filepath.Join(empty.Record.Dir, harness.EventsFile), nil, 0o600))
	runs = withPrior(br, state, "s1", []LoadedRun{empty, runFrom(t, 3)})
	require.Len(t, runs, 3)
	assert.Equal(t, []string{"message 1", "message 2"}, messages(runs[0]), "a run that failed before its first item does not hide the history")

	sessionFile(t, state, "fork", "msg", "fork", "msg", "fork", "msg")
	runs = withPrior(br, state, "fork", nil)
	require.Len(t, runs, 1)
	assert.Equal(t, []string{"message 5"}, messages(runs[0]), "a fork's copy of its parents' history, to its last fork item, is theirs")

	sessionFile(t, state, SubagentIDPrefix+"x", "msg", "msg")
	runs = withPrior(br, state, SubagentIDPrefix+"x", []LoadedRun{runFrom(t, 2)})
	assert.Len(t, runs, 1, "a forked subagent's copy of its parent's history, with no fork item, is the parent's")
	sessionFile(t, state, "legacy-child", "msg", "msg")
	require.NoError(t, os.WriteFile(filepath.Join(state, "sessions", "legacy-child.uah.json"), []byte(`{"source":"subagent","parent":"p"}`), 0o600))
	runs = withPrior(br, state, "legacy-child", []LoadedRun{runFrom(t, 2)})
	assert.Len(t, runs, 1, "an older subagent, known by its sidecar's parent")

	runs = withPrior(br, state, "none", nil)
	assert.Empty(t, runs, "no session file, no history")
	require.NoError(t, os.WriteFile(filepath.Join(state, "sessions", "bad"+sessionFileSuffix), []byte("not a session file\n"), 0o600))
	one := []LoadedRun{runFrom(t, 2)}
	assert.Equal(t, one, withPrior(br, state, "bad", one), "a session file that does not read leaves the runs")
}

// A recorded session's tool call keeps its result: the operation
// snapshots the session file keeps beside the item reach the decoders.
func TestWithPrior_ToolCalls(t *testing.T) {
	state := t.TempDir()
	data, err := os.ReadFile(filepath.Join("..", "sessionfile", "testdata", "recorded.session.jsonl"))
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(state, "sessions"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(state, "sessions", "rec"+sessionFileSuffix), data, 0o600))

	runs := withPrior(bufio.NewReader(nil), state, "rec", nil)
	require.Len(t, runs, 1)
	finished := 0
	for _, e := range runs[0].Events {
		if _, ok := e.(core.ToolFinished); ok {
			finished++
		}
	}
	assert.Positive(t, finished, "a call's result is in the history")
}
