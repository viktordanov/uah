package session_test

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/session"
)

// TestSession_InjectIntoTheLiveRun: during a run an injected message goes
// into the run as a developer message; one the run recorded stays there,
// and one a run that ended never read is held for the next run.
func TestSession_InjectIntoTheLiveRun(t *testing.T) {
	for _, tc := range []struct {
		name   string
		unread bool
	}{{"recorded", false}, {"never read", true}} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, fakeCaps{LiveInput: true})
			_, err := h.s.Submit("go")
			require.NoError(t, err)
			r := <-h.eng.started
			r.unread = tc.unread
			h.until(isType[core.UserMessage])

			_, live := h.s.Inject("<subagent_notification>done</subagent_notification>")
			assert.True(t, live)
			require.Len(t, r.sent, 1)
			assert.Equal(t, core.RoleDeveloper, r.sent[0].Role)
			r.finish(core.StatusOK)
			if tc.unread {
				// The run never read it: the next run starts for it.
				next := <-h.eng.started
				assert.Equal(t, []string{"<subagent_notification>done</subagent_notification>"}, texts(next.req.Messages), "held again, and a run starts for it")
				assert.Equal(t, core.Role(""), next.req.Messages[0].Role, "as the user's: a developer message alone asks for no response")
				next.finish(core.StatusOK)
			}
			h.until(isType[session.Idle])

			_, err = h.s.Submit("next")
			require.NoError(t, err)
			next := <-h.eng.started
			assert.Equal(t, []string{"next"}, texts(next.req.Messages))
			next.finish(core.StatusOK)
		})
	}
}

// TestSession_InjectWhileIdleStartsARun: with no run, an injected message
// starts one, so the agent takes up a subagent's or a shell's end.
func TestSession_InjectWhileIdleStartsARun(t *testing.T) {
	h := newHarness(t, fakeCaps{LiveInput: true})
	_, live := h.s.Inject("a shell ended")
	assert.True(t, live)
	r := <-h.eng.started
	assert.Equal(t, []string{"a shell ended"}, texts(r.req.Messages))
	r.finish(core.StatusOK)
	h.until(isType[session.Idle])
}

// TestSession_InjectAfterAStopIsHeld: after the user stopped a run, an
// injected message waits for the user's next message, and withdraw takes
// it back.
func TestSession_InjectAfterAStopIsHeld(t *testing.T) {
	h := newHarness(t, fakeCaps{LiveInput: true})
	_, err := h.s.Submit("work")
	require.NoError(t, err)
	h.nextRun()
	require.NoError(t, h.s.Interrupt())
	h.until(isType[session.Idle])

	_, live := h.s.Inject("kept")
	assert.False(t, live)
	withdraw, _ := h.s.Inject("taken back")
	withdraw()
	select {
	case r := <-h.eng.started:
		t.Fatalf("no run should start after the user's stop, got %v", texts(r.req.Messages))
	case <-time.After(100 * time.Millisecond):
	}

	_, err = h.s.Submit("next")
	require.NoError(t, err)
	r := <-h.eng.started
	assert.Equal(t, []string{"kept", "next"}, texts(r.req.Messages))
	r.finish(core.StatusOK)
}

// TestSession_InjectSurvivesAFailedStart: a run that fails to start gives
// back the held message it took, and the next run gets it.
func TestSession_InjectSurvivesAFailedStart(t *testing.T) {
	h := newHarness(t, fakeCaps{LiveInput: true})
	h.eng.startErr = errors.New("preflight blocked the run")
	h.s.Inject("note") // its run fails to start, and none is tried again
	notice := h.until(isType[session.Notice]).(session.Notice)
	assert.True(t, notice.StartFailed, "it fails no input: the notice is how the TUI hears of it")
	h.until(isType[session.Idle])

	h.eng.startErr = nil
	_, err := h.s.Submit("second")
	require.NoError(t, err)
	r := <-h.eng.started
	assert.Equal(t, []string{"note", "second"}, texts(r.req.Messages))
	r.finish(core.StatusOK)
}
