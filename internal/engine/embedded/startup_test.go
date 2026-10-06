package embedded_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/testing/fakellm"
)

// TestEmbedded_SteerRightAfterSubmit: a message steered in while the run
// starts goes with its first model request, not as a second request that
// cancels the first (uah-core v0.9.1 reads queued events first).
func TestEmbedded_SteerRightAfterSubmit(t *testing.T) {
	e := newEnv(t, fakellm.Reply{Text: "a"}, fakellm.Reply{Text: "b"}, fakellm.Reply{Text: "c"})
	s, ev := e.open(t, e.embedded(), "")

	_, err := s.Submit("start")
	require.NoError(t, err)
	_, err = s.SteerNow("also this")
	require.NoError(t, err)
	ev.finished()

	reqs := e.llm.Requests()
	require.Len(t, reqs, 1, "one request")
	assert.Equal(t, []string{"start", "also this"}, reqs[0].UserTexts)
}

// TestEmbedded_InterruptRightAfterSubmit: an interrupt while the run
// starts sends no model request.
func TestEmbedded_InterruptRightAfterSubmit(t *testing.T) {
	e := newEnv(t, fakellm.Reply{Text: "a"})
	s, ev := e.open(t, e.embedded(), "")

	_, err := s.Submit("go")
	require.NoError(t, err)
	require.NoError(t, s.Interrupt())
	assert.Equal(t, core.StatusInterrupted, ev.finished().Status)
	assert.Empty(t, e.llm.Requests())
}
