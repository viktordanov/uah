package session_test

import (
	"testing"

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
			h.until(isType[session.Idle])

			_, err = h.s.Submit("next")
			require.NoError(t, err)
			next := <-h.eng.started
			var texts []string
			for _, m := range next.req.Messages {
				texts = append(texts, m.Text)
			}
			if tc.unread {
				assert.Equal(t, []string{"<subagent_notification>done</subagent_notification>", "next"}, texts, "held again for the next run")
			} else {
				assert.Equal(t, []string{"next"}, texts)
			}
			next.finish(core.StatusOK)
		})
	}
}

// TestSession_InjectWhileIdleIsHeld: with no run, an injected message waits
// for the next run, and withdraw takes it back.
func TestSession_InjectWhileIdleIsHeld(t *testing.T) {
	h := newHarness(t, fakeCaps{LiveInput: true})
	_, live := h.s.Inject("kept")
	assert.False(t, live)
	withdraw, _ := h.s.Inject("taken back")
	withdraw()

	_, err := h.s.Submit("next")
	require.NoError(t, err)
	r := <-h.eng.started
	var texts []string
	for _, m := range r.req.Messages {
		texts = append(texts, m.Text)
	}
	assert.Equal(t, []string{"kept", "next"}, texts)
	r.finish(core.StatusOK)
}
