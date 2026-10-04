package embedded_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/engine/embedded"
	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/testing/fakellm"
)

// TestEmbedded_FinishedRunIsFreed: once a run has ended, nothing the
// engine or the open session keeps holds it. The auto-review transcript,
// which lives as long as the session, held the last run's reviewer, and
// through the reviewer's model client the run's decoded session file, its
// history, and its request encodings: in an interactive session, about
// three copies of the history stayed in memory between turns.
func TestEmbedded_FinishedRunIsFreed(t *testing.T) {
	e := newEnv(t, fakellm.Reply{Text: "one"}, fakellm.Reply{Text: "two"}, fakellm.Reply{Text: "three"})
	reachable := embedded.WatchRuns(t, e.Workspace)
	// Interactive: the session answers approvals, so each run puts the
	// auto-reviewer in front of it.
	s, err := session.Open(context.Background(), e.embedded(), session.Options{Settings: e.settings(), Interactive: true})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	ev := &events{t: t, s: s}

	for _, text := range []string{"first", "second", "third"} {
		_, err := s.Submit(text)
		require.NoError(t, err)
		ev.idle()
	}

	require.Len(t, e.llm.Requests(), 3)
	assert.Eventually(t, func() bool { return reachable() == 0 }, waitTimeout, 10*time.Millisecond,
		"the finished runs are freed while the session stays open")
}
