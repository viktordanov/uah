package bubble_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/tui/bubble"
	"github.com/viktordanov/uah/internal/tui/term"
	"github.com/viktordanov/uah/internal/usage"
	"github.com/viktordanov/uah/internal/usage/cachestats"
	"github.com/viktordanov/uah/testing/harnesstest"
)

// usageReader reads a Codex test login's usage from a loopback backend that
// serves the Pro login's fixture, and counts the reads.
func usageReader(t *testing.T) (usage.Reader, *atomic.Int32) {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", "usage", "testdata", "pro_weekly_only.json"))
	require.NoError(t, err)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	env := harnesstest.NewEnv(t)

	return usage.NewCodexReader(usage.ReaderOptions{Getenv: env.Getenv, BaseURL: srv.URL}), &calls
}

// TestTUI_Usage reads the usage after the run, for the footer, and again on
// /status, for its rows.
func TestTUI_Usage(t *testing.T) {
	d0 := deps(t, "simple.jsonl")
	reader, calls := usageReader(t)
	d0.Usage = reader
	d := start(t, d0)
	d.until("the session is open", func() bool { return d.m.(bubble.Model).Exit().SessionID != "" })
	assert.Zero(t, calls.Load(), "no read before a run")

	d.typeText("hi")
	d.key(term.KeyEnter, 0)
	d.waitFor("weekly 78% left ·")
	assert.Equal(t, int32(1), calls.Load(), "one read after the run")

	d.typeText("/status")
	d.key(term.KeyEnter, 0)
	d.waitFor("weekly [███████████████░░░░░] 78% left (resets ")
	assert.Equal(t, int32(2), calls.Load(), "/status reads again")
}

// TestTUI_NoUsage shows nothing without a reader.
func TestTUI_NoUsage(t *testing.T) {
	d := start(t, deps(t, "simple.jsonl"))
	d.until("the session is open", func() bool { return d.m.(bubble.Model).Exit().SessionID != "" })
	d.typeText("/status")
	d.key(term.KeyEnter, 0)
	d.waitFor("usage is not available for openai-codex")
	assert.NotContains(t, d.view(), "% left")
}

// TestTUI_Cache shows the session's prompt cache on /usage, read for the
// open session.
func TestTUI_Cache(t *testing.T) {
	d0 := deps(t, "simple.jsonl")
	var asked atomic.Value
	d0.Cache = func(id string) ([]cachestats.Attributed, error) {
		asked.Store(id)

		return []cachestats.Attributed{{
			Request: cachestats.Request{Input: 12_000, Cached: 4_096, Output: 100},
			Misses:  []cachestats.Miss{{Cause: cachestats.CauseCold, Tokens: 7_904}},
		}}, nil
	}
	d := start(t, d0)
	d.until("the session is open", func() bool { return d.m.(bubble.Model).Exit().SessionID != "" })

	d.typeText("/usage")
	d.key(term.KeyEnter, 0)
	d.waitFor("prompt cache 34% · missed 8k: cold start 8k")
	assert.Equal(t, d.m.(bubble.Model).Exit().SessionID, asked.Load())
}
