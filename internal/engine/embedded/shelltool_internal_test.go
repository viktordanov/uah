package embedded

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/viktordanov/uah-core/harness/llm"
	"github.com/viktordanov/uah-core/harness/operation"
)

type shellPlanContext struct {
	specs []operation.Spec
}

func (c *shellPlanContext) Submit(spec operation.Spec) operation.ID {
	c.specs = append(c.specs, spec)

	return "wait-1"
}

// TestShellWaitTimeout: the wait's plan and the wake policy read the same
// timeout, max_wait without timeout_ms.
func TestShellWaitTimeout(t *testing.T) {
	for _, tt := range []struct {
		name, extra string
		maxWait     time.Duration
		want        time.Duration
	}{
		{"default", ``, time.Hour, time.Hour},
		{"sooner", `,"timeout_ms":120000`, time.Hour, 2 * time.Minute},
		{"minimum", `,"timeout_ms":-1`, time.Hour, time.Second},
		{"above max_wait", `,"timeout_ms":7200000`, time.Hour, time.Hour},
		{"huge number", `,"timeout_ms":1e100`, time.Hour, time.Hour},
		{"configured max_wait", ``, 10 * time.Minute, 10 * time.Minute},
		{"max_wait below the minimum", `,"timeout_ms":1`, 500 * time.Millisecond, 500 * time.Millisecond},
	} {
		t.Run(tt.name, func(t *testing.T) {
			call := llm.ToolCall{Name: toolWaitShell, Arguments: `{"shell_ids":["shell-1"]` + tt.extra + `}`}
			ctx := &shellPlanContext{}
			status := (shellTranslator{name: toolWaitShell, offered: true, maxWait: tt.maxWait}).Translate(ctx, call)
			require.Empty(t, status.Error)
			require.Len(t, ctx.specs, 1)
			op := operation.Operation{Type: ctx.specs[0].Type, Version: ctx.specs[0].Version, State: ctx.specs[0].State, MaxOutputLength: ctx.specs[0].MaxOutputLength}
			state, err := operation.DecodeRemoteJobState(op)
			require.NoError(t, err)
			var plan shellPlan
			require.NoError(t, json.Unmarshal(state.Plan.Data, &plan))
			assert.Equal(t, tt.want.Milliseconds(), plan.TimeoutMS)
			timeout, ok := shellWaitTimeout(call, tt.maxWait)
			assert.True(t, ok)
			assert.Equal(t, tt.want, timeout)
		})
	}
}

// TestShellWaitTimeoutIgnoresOtherOrInvalidCalls: a call the translator
// would refuse is not a wait.
func TestShellWaitTimeoutIgnoresOtherOrInvalidCalls(t *testing.T) {
	for _, call := range []llm.ToolCall{
		{Name: toolKillShell, Arguments: `{"shell_ids":["shell-1"],"timeout_ms":3600000}`},
		{Name: toolWaitShell, Arguments: `{"timeout_ms":3600000}`},
		{Name: toolWaitShell, Arguments: `{"shell_ids":["shell-1"],"timeout_ms":"long"}`},
		{Name: toolWaitShell, Arguments: `not json`},
	} {
		_, ok := shellWaitTimeout(call, time.Hour)
		assert.False(t, ok, call.Arguments)
	}
}
