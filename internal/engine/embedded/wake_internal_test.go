package embedded

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/viktordanov/uah-core/harness/llm"

	"github.com/viktordanov/uah/internal/engine"
)

func TestWakePolicyHolds(t *testing.T) {
	p := wakePolicy(0, time.Hour, 10*time.Minute, nil)
	assert.Equal(t, wakeHold, p.Hold)
	assert.NotNil(t, p.CallHold)
	assert.NotNil(t, p.Progress)
}

// waitingAgents is Subagents whose wait_agent waits as long as the call's
// timeout_ms says.
type waitingAgents struct{ engine.Subagents }

func (waitingAgents) WaitTimeout(tool string, args json.RawMessage) (time.Duration, bool) {
	var a struct {
		TimeoutMS *float64 `json:"timeout_ms"`
	}
	if tool != "wait_agent" || json.Unmarshal(args, &a) != nil || a.TimeoutMS == nil {
		return 0, false
	}

	return time.Duration(*a.TimeoutMS) * time.Millisecond, true
}

// TestWaitHold: a wait longer than the wake hold is held for its timeout
// and 30 seconds; a short wait, another tool, or bad arguments keep the
// hold.
func TestWaitHold(t *testing.T) {
	hold := waitHold(5*time.Minute, time.Hour, 10*time.Minute, waitingAgents{})
	for _, tc := range []struct {
		name, tool, args string
		want             time.Duration
	}{
		{"shell, default", toolWaitShell, `{"shell_ids":["shell-1"]}`, time.Hour + 30*time.Second},
		{"shell, twenty minutes", toolWaitShell, `{"shell_ids":["shell-1"],"timeout_ms":1200000}`, 20*time.Minute + 30*time.Second},
		{"shell, huge number", toolWaitShell, `{"shell_ids":["shell-1"],"timeout_ms":1e100}`, time.Hour + 30*time.Second},
		{"shell, short", toolWaitShell, `{"shell_ids":["shell-1"],"timeout_ms":60000}`, 0},
		{"shell, at the hold", toolWaitShell, `{"shell_ids":["shell-1"],"timeout_ms":270000}`, 0},
		{"shell, bad arguments", toolWaitShell, `{"shell_ids":"shell-1"}`, 0},
		{"agent, long", "wait_agent", `{"targets":["a"],"timeout_ms":1800000}`, 30*time.Minute + 30*time.Second},
		{"agent, short", "wait_agent", `{"targets":["a"],"timeout_ms":60000}`, 0},
		{"agent, bad arguments", "wait_agent", `{"targets":["a"]`, 0},
		{"another tool", "Bash", `{"command":"sleep 3600"}`, 0},
	} {
		assert.Equal(t, tc.want, hold(llm.ToolCall{Name: tc.tool, Arguments: tc.args}), tc.name)
	}
	assert.Zero(t, waitHold(5*time.Minute, time.Hour, 10*time.Minute, nil)(llm.ToolCall{Name: "wait_agent", Arguments: `{"targets":["a"],"timeout_ms":1800000}`}), "no subagents")
}
