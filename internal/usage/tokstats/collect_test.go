package tokstats_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uagent/core"
	uaharness "github.com/viktordanov/uagent/harness"
	"github.com/viktordanov/uagent/testing/fixtures"

	"github.com/viktordanov/uah/internal/compaction"
	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/internal/usage/tokstats"
	"github.com/viktordanov/uah/testing/harnesstest"
)

// Collect reads the runs from the index, with each session's source from
// its sidecar, and the compaction calls from the compaction logs.
func TestCollect(t *testing.T) {
	env := harnesstest.NewEnv(t)
	env.StateDir = filepath.Join(t.TempDir(), "state[1]") // no glob reads it
	t.Setenv("FAKERUNNER_FIXTURE", fixtures.Path("simple.jsonl"))
	h := uaharness.New(uaharness.Config{RunnerPath: harnesstest.FakeRunner(t), StateDir: env.StateDir, Getenv: env.Getenv})
	const id = "3f2a1b2c-0000-4000-8000-000000000001"
	_, err := h.Run(context.Background(), core.Request{
		SessionID: id, Messages: []core.UserInput{{Text: "hi"}},
		Provider: "openai-codex", Model: "gpt-6-sol", Effort: "high", Workspace: env.Workspace,
	}, func(core.Event) {})
	require.NoError(t, err)
	sessionsDir := filepath.Join(env.StateDir, "sessions")
	require.NoError(t, os.MkdirAll(sessionsDir, 0o700))
	sc, err := json.Marshal(session.Sidecar{Source: session.SourceRun, Workspace: env.Workspace})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(sessionsDir, id+".uah.json"), sc, 0o600))
	log := compaction.OpenLog(sessionsDir, id)
	at := time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)
	require.NoError(t, log.Append(compaction.Record{Covered: 4, Hash: "h1", At: at, Model: "gpt-6.1-sol", Stats: &compaction.Stats{Call: compaction.Usage{Input: 900, Cached: 100, Output: 40}}}))
	require.NoError(t, log.Append(compaction.Record{Covered: 6, Hash: "h2", At: at, Stats: &compaction.Stats{Strategy: "elide"}}))

	// A forked subagent's log starts with a copy of its parent's records.
	// An older one's ID is a plain UUID, here one that sorts before its
	// parent's: its sidecar's parent says what it is.
	const child = "00000000-0000-4000-8000-000000000009"
	require.NoError(t, os.WriteFile(filepath.Join(sessionsDir, child+".uah.json"), []byte(`{"source":"subagent","parent":"`+id+`"}`), 0o600))
	fork := compaction.OpenLog(sessionsDir, child)
	require.NoError(t, fork.Append(compaction.Record{Covered: 4, Hash: "h1", At: at, Model: "gpt-6.1-sol", Stats: &compaction.Stats{Call: compaction.Usage{Input: 900, Cached: 100, Output: 40}}}))
	require.NoError(t, fork.Append(compaction.Record{Covered: 8, Hash: "h3", At: at.Add(time.Hour), Model: "gpt-6.1-sol", Stats: &compaction.Stats{Call: compaction.Usage{Input: 50, Output: 5}}}))

	// A fork of the fork, sorting before both, copies the child's call: it
	// stays the child's.
	const grandchild = "00000000-0000-4000-8000-000000000000"
	require.NoError(t, os.WriteFile(filepath.Join(sessionsDir, grandchild+".uah.json"), []byte(`{"source":"subagent","parent":"`+child+`"}`), 0o600))
	require.NoError(t, compaction.OpenLog(sessionsDir, grandchild).Append(compaction.Record{Covered: 8, Hash: "h3", At: at.Add(time.Hour), Model: "gpt-6.1-sol", Stats: &compaction.Stats{Call: compaction.Usage{Input: 50, Output: 5}}}))

	entries, err := tokstats.Collect(context.Background(), env.StateDir)
	require.NoError(t, err)
	require.Len(t, entries, 3, "the run, the parent's compaction call once, and the fork's own")
	assert.True(t, entries[0].Run)
	assert.Equal(t, tokstats.SourceExec, entries[0].Source)
	assert.Equal(t, "gpt-6-sol", entries[0].Model)
	assert.Positive(t, entries[0].Tokens.InputTokens)
	assert.Equal(t, tokstats.Entry{
		At: at, Session: id, Model: "gpt-6.1-sol", Workspace: env.Workspace, Source: tokstats.SourceCompaction,
		Tokens: core.Tokens{InputTokens: 900, CachedInputTokens: 100, OutputTokens: 40},
	}, entries[1])
	assert.Equal(t, child, entries[2].Session)
	assert.Equal(t, int64(50), entries[2].Tokens.InputTokens)
}

// A session whose sidecar has no workspace, from before uah kept it, takes
// its runs': its compaction calls count in that workspace too.
func TestCollect_WorkspaceFromRuns(t *testing.T) {
	env := harnesstest.NewEnv(t)
	t.Setenv("FAKERUNNER_FIXTURE", fixtures.Path("simple.jsonl"))
	h := uaharness.New(uaharness.Config{RunnerPath: harnesstest.FakeRunner(t), StateDir: env.StateDir, Getenv: env.Getenv})
	const id = "3f2a1b2c-0000-4000-8000-000000000002"
	_, err := h.Run(context.Background(), core.Request{
		SessionID: id, Messages: []core.UserInput{{Text: "hi"}},
		Provider: "openai-codex", Model: "gpt-6-sol", Effort: "high", Workspace: env.Workspace,
	}, func(core.Event) {})
	require.NoError(t, err)
	sessionsDir := filepath.Join(env.StateDir, "sessions")
	require.NoError(t, os.MkdirAll(sessionsDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(sessionsDir, id+".uah.json"), []byte(`{"source":"tui"}`), 0o600))
	require.NoError(t, compaction.OpenLog(sessionsDir, id).Append(compaction.Record{Covered: 4, Hash: "h", At: time.Now(), Stats: &compaction.Stats{Call: compaction.Usage{Input: 10, Output: 1}}}))

	entries, err := tokstats.Collect(context.Background(), env.StateDir)
	require.NoError(t, err)
	require.Len(t, entries, 2)
	assert.Equal(t, env.Workspace, entries[1].Workspace)
}
