package state_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/internal/tui/state"
)

// TestReduce_PreflightWarning: a run's preflight warning becomes a notice,
// except the state directory inside the workspace, which uah allows.
func TestReduce_PreflightWarning(t *testing.T) {
	s, _ := apply(opened(), core.RunStarted{At: t0, RunID: "run-1"})
	n := len(s.Items)

	s, _ = apply(s, core.PreflightWarning{At: t0, Code: core.FindingStateInWorkspace, Message: "workspace contains the state directory"})
	assert.Len(t, s.Items, n, "no notice on every run")

	s, _ = apply(s, core.PreflightWarning{At: t0, Code: core.FindingDotenvRisky, Message: "the workspace .env sets an endpoint"})
	last := s.Items[len(s.Items)-1]
	assert.Equal(t, state.KindNotice, last.Kind)
	assert.Equal(t, session.LevelWarning, last.Level)
	assert.Equal(t, "the workspace .env sets an endpoint", last.Text)
}
