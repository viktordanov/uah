package store_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uagent/core"
	uaharness "github.com/viktordanov/uagent/harness"
	"github.com/viktordanov/uagent/stream"
	"github.com/viktordanov/uagent/testing/fixtures"

	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/internal/store"
	"github.com/viktordanov/uah/testing/harnesstest"
)

const loaded = "3f2a1b2c-0000-4000-8000-000000000001"

// TestLoadMatchesTheScan: the index finds the same runs of a session as
// the file scan, with their tokens and events, and leaves out a run whose
// summary is gone since the index read it.
func TestLoadMatchesTheScan(t *testing.T) {
	env := harnesstest.NewEnv(t)
	runs(t, env, loaded, "first question", "second question")
	runs(t, env, "", "another session")

	got, err := store.Load(context.Background(), env.StateDir, loaded)
	require.NoError(t, err)
	want, err := session.Load(env.StateDir, loaded)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, want, got)
	assert.Positive(t, got[0].Record.Result.Stats.Tokens.InputTokens, "the tokens, from the summary")
	assert.NotEmpty(t, got[0].Events)

	ix, err := store.Open(context.Background(), env.StateDir)
	require.NoError(t, err)
	defer ix.Close()
	require.NoError(t, os.Remove(filepath.Join(got[0].Record.Dir, uaharness.SummaryFile)))
	rest, err := session.LoadRuns(env.StateDir, loaded, []string{got[0].Record.Dir, got[1].Record.Dir})
	require.NoError(t, err)
	require.Len(t, rest, 1)
	assert.Equal(t, got[1].Record.Dir, rest[0].Record.Dir)
}

// BenchmarkLoadSession loads one session of 10 runs from a state directory
// of 2,000 runs across 200 sessions: by the file scan every caller used
// (session.Load), and by the index (store.Load), whose file is already
// built, as it is after any listing.
func BenchmarkLoadSession(b *testing.B) {
	stateDir := manyRuns(b, 200, 10)
	ix, err := store.Open(context.Background(), stateDir)
	require.NoError(b, err)
	require.NoError(b, ix.Close())
	id := sessionID(100)
	for name, load := range map[string]func() ([]session.LoadedRun, error){
		"scan":  func() ([]session.LoadedRun, error) { return session.Load(stateDir, id) },
		"index": func() ([]session.LoadedRun, error) { return store.Load(context.Background(), stateDir, id) },
	} {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				got, err := load()
				if err != nil || len(got) != 10 {
					b.Fatalf("%d runs, %v", len(got), err)
				}
			}
		})
	}
}

func sessionID(n int) string { return fmt.Sprintf("3f2a1b2c-0000-4000-8000-%012d", n) }

// manyRuns writes sessions×perSession run records, each a copy of one the
// fake runner wrote with its own run ID, session, and start.
func manyRuns(b *testing.B, sessions, perSession int) string {
	b.Helper()
	env := harnesstest.NewEnv(b)
	b.Setenv("FAKERUNNER_FIXTURE", fixtures.Path("simple.jsonl"))
	h := uaharness.New(uaharness.Config{RunnerPath: harnesstest.FakeRunner(b), StateDir: env.StateDir, Getenv: env.Getenv})
	res, err := h.Run(context.Background(), core.Request{
		SessionID: loaded, Messages: []core.UserInput{{Text: "a question"}},
		Provider: "openai-codex", Model: "gpt-6-sol", Effort: "high", Workspace: env.Workspace,
	}, func(core.Event) {})
	require.NoError(b, err)
	template := h.RunDir(res.Request.RunID)
	files := map[string][]byte{}
	for _, name := range []string{uaharness.SummaryFile, uaharness.RequestFile, uaharness.EventsFile} {
		data, err := os.ReadFile(filepath.Join(template, name))
		require.NoError(b, err)
		files[name] = data
	}
	require.NoError(b, os.RemoveAll(template))
	var summary stream.SummaryDTO
	require.NoError(b, json.Unmarshal(files[uaharness.SummaryFile], &summary))
	var request map[string]any
	require.NoError(b, json.Unmarshal(files[uaharness.RequestFile], &request))
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range sessions * perSession {
		runID := fmt.Sprintf("run-%06d", i)
		dir := filepath.Join(env.StateDir, "runs", runID)
		require.NoError(b, os.MkdirAll(dir, 0o700))
		summary.RunID, summary.SessionID, summary.StartedAt = runID, sessionID(i%sessions), start.Add(time.Duration(i)*time.Minute)
		request["session_id"] = summary.SessionID
		for name, v := range map[string]any{uaharness.SummaryFile: summary, uaharness.RequestFile: request} {
			data, err := json.Marshal(v)
			require.NoError(b, err)
			require.NoError(b, os.WriteFile(filepath.Join(dir, name), data, 0o600))
		}
		require.NoError(b, os.WriteFile(filepath.Join(dir, uaharness.EventsFile), files[uaharness.EventsFile], 0o600))
	}

	return env.StateDir
}
