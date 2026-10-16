package store_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uagent/core"
	uaharness "github.com/viktordanov/uagent/harness"
	"github.com/viktordanov/uagent/testing/fixtures"

	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/internal/store"
	"github.com/viktordanov/uah/testing/harnesstest"
)

// runs writes real run records with the fake runner: one run per prompt,
// each in its own session unless sessionID is set.
func runs(t *testing.T, env *harnesstest.Env, sessionID string, prompts ...string) {
	t.Helper()
	t.Setenv("FAKERUNNER_FIXTURE", fixtures.Path("simple.jsonl"))
	h := uaharness.New(uaharness.Config{RunnerPath: harnesstest.FakeRunner(t), StateDir: env.StateDir, Getenv: env.Getenv})
	for _, p := range prompts {
		_, err := h.Run(context.Background(), core.Request{
			SessionID: sessionID, Messages: []core.UserInput{{Text: p}},
			Provider: "openai-codex", Model: "gpt-6-sol", Effort: "high", Workspace: env.Workspace,
		}, func(core.Event) {})
		require.NoError(t, err)
	}
}

func TestIndexMatchesTheFiles(t *testing.T) {
	env := harnesstest.NewEnv(t)
	runs(t, env, "", "summarize the parser")
	runs(t, env, "3f2a1b2c-0000-4000-8000-000000000001", "first question", "second question")

	ix, err := store.Open(context.Background(), env.StateDir)
	require.NoError(t, err)
	defer ix.Close()
	got, err := ix.Sessions(context.Background())
	require.NoError(t, err)
	want, err := session.Sessions(env.StateDir)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, utc(want), utc(got))
	assert.Equal(t, "first question", got[0].FirstPrompt)
	assert.Equal(t, 2, got[0].Runs)
	assert.Positive(t, got[0].Tokens.InputTokens, "the runs' tokens, from their summaries")
}

func TestReconcile(t *testing.T) {
	env := harnesstest.NewEnv(t)
	runs(t, env, "", "one")
	ix, err := store.Open(context.Background(), env.StateDir)
	require.NoError(t, err)
	defer ix.Close()

	runs(t, env, "", "two") // written after the index was opened, as by another uah or uagent
	require.NoError(t, ix.Reconcile(context.Background()))
	got, err := ix.Sessions(context.Background())
	require.NoError(t, err)
	assert.Len(t, got, 2)

	entries, err := os.ReadDir(filepath.Join(env.StateDir, "runs"))
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(filepath.Join(env.StateDir, "runs", entries[0].Name())))
	require.NoError(t, ix.Reconcile(context.Background()))
	got, err = ix.Sessions(context.Background())
	require.NoError(t, err)
	assert.Len(t, got, 1, "a deleted run leaves the index")

	require.NoError(t, ix.Close())
	require.NoError(t, os.Remove(filepath.Join(env.StateDir, "uah.db")))
	rebuilt, err := store.Open(context.Background(), env.StateDir)
	require.NoError(t, err)
	defer rebuilt.Close()
	again, err := rebuilt.Sessions(context.Background())
	require.NoError(t, err)
	assert.Equal(t, utc(got), utc(again), "a deleted index rebuilds from the files")
}

func TestSearchAndActivity(t *testing.T) {
	env := harnesstest.NewEnv(t)
	runs(t, env, "", "fix the flaky parser test", "write the changelog")
	ix, err := store.Open(context.Background(), env.StateDir)
	require.NoError(t, err)
	defer ix.Close()

	found, err := ix.Search(context.Background(), "flaky parser")
	require.NoError(t, err)
	require.Len(t, found, 1)
	assert.Equal(t, "fix the flaky parser test", found[0].FirstPrompt)
	found, err = ix.Search(context.Background(), `"quotes" and OR`)
	require.NoError(t, err, "user text is never FTS syntax")
	assert.Empty(t, found)

	activity, err := ix.Activity(context.Background(), time.Now(), 7)
	require.NoError(t, err)
	assert.Equal(t, 2, activity[time.Now().Format(time.DateOnly)])
}

func TestTokens(t *testing.T) {
	env := harnesstest.NewEnv(t)
	runs(t, env, "", "one", "two")
	got, err := store.TokensIn(context.Background(), env.StateDir)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.False(t, got[1].Started.Before(got[0].Started), "oldest first")
	assert.Equal(t, "gpt-6-sol", got[0].Model)
	assert.Equal(t, env.Workspace, got[0].Workspace)
	assert.Positive(t, got[0].Tokens.InputTokens, "the run's tokens, from its summary")
}

// utc drops time zones and monotonic readings, which differ between JSON and
// SQLite but name the same instants.
func utc(infos []session.Info) []session.Info {
	out := make([]session.Info, len(infos))
	for i, in := range infos {
		in.Started, in.LastActivity = in.Started.UTC().Round(0), in.LastActivity.UTC().Round(0)
		out[i] = in
	}

	return out
}

// TestForget drops a session's rows, runs and full text, for
// `uah sessions rm`, and keeps the others.
func TestForget(t *testing.T) {
	env := harnesstest.NewEnv(t)
	const gone, kept = "3f2a1b2c-0000-4000-8000-000000000002", "3f2a1b2c-0000-4000-8000-000000000003"
	runs(t, env, gone, "flaky parser")
	runs(t, env, kept, "flaky lexer")
	ix, err := store.Open(context.Background(), env.StateDir)
	require.NoError(t, err)
	defer ix.Close()

	require.NoError(t, ix.Forget(context.Background(), []string{gone}))

	infos, err := ix.Sessions(context.Background())
	require.NoError(t, err)
	require.Len(t, infos, 1)
	assert.Equal(t, kept, infos[0].ID)
	found, err := ix.Search(context.Background(), "flaky")
	require.NoError(t, err)
	require.Len(t, found, 1, "no full text left of the forgotten session")
	assert.Equal(t, kept, found[0].ID)
}
