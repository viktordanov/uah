package render_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/session"
)

// TestAnswerHidesCitationMarkers: the citation markers a model writes
// after a web search are not drawn, while the answer streams or done.
func TestAnswerHidesCitationMarkers(t *testing.T) {
	s := apply(base(),
		core.RunStarted{At: t0, RunID: "r1"},
		core.AssistantMessage{At: t0, Text: "The hook now points to the guard. citeturn2view0", Final: true},
		core.RunFinished{At: t0, Result: core.Result{Request: core.Request{RunID: "r1"}, Status: core.StatusOK}},
		session.Idle{At: t0},
	)
	out := screen(s, "")
	assert.Contains(t, out, "The hook now points to the guard.")
	assert.NotContains(t, out, "cite")
	assert.NotContains(t, out, "turn2view0")
	assert.NotContains(t, out, "")
}
