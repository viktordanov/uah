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

func TestShellWaitOptIn(t *testing.T) {
	for _, tt := range []struct {
		name, extra string
		want        time.Duration
		quiet       bool
	}{
		{"default", ``, 270 * time.Second, false},
		{"old cap", `,"timeout_ms":3600000`, 270 * time.Second, false},
		{"explicit false", `,"timeout_ms":3600000,"allow_long_wait":false`, 270 * time.Second, false},
		{"one hour", `,"timeout_ms":3600000,"allow_long_wait":true`, time.Hour, true},
		{"long cap", `,"timeout_ms":7200000,"allow_long_wait":true`, time.Hour, true},
		{"twenty minutes", `,"timeout_ms":1200000,"allow_long_wait":true`, 20 * time.Minute, true},
		{"opt in alone", `,"allow_long_wait":true`, 270 * time.Second, false},
		{"short opt in", `,"timeout_ms":2000,"allow_long_wait":true`, 2 * time.Second, false},
		{"minimum", `,"timeout_ms":-1,"allow_long_wait":true`, time.Second, false},
		{"huge number", `,"timeout_ms":1e100,"allow_long_wait":true`, time.Hour, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			call := llm.ToolCall{Name: toolWaitShell, Arguments: `{"shell_ids":["shell-1"]` + tt.extra + `}`}
			ctx := &shellPlanContext{}
			status := (shellTranslator{name: toolWaitShell, offered: true}).Translate(ctx, call)
			require.Empty(t, status.Error)
			require.Len(t, ctx.specs, 1)
			// The remote job's durable plan must use the same duration as
			// the wake policy; these checks do not wait on a real timer.
			op := operation.Operation{Type: ctx.specs[0].Type, Version: ctx.specs[0].Version, State: ctx.specs[0].State, MaxOutputLength: ctx.specs[0].MaxOutputLength}
			state, err := operation.DecodeRemoteJobState(op)
			require.NoError(t, err)
			var plan shellPlan
			require.NoError(t, json.Unmarshal(state.Plan.Data, &plan))
			assert.Equal(t, tt.want.Milliseconds(), plan.TimeoutMS)
			if tt.quiet {
				assert.Equal(t, tt.want+30*time.Second, quietShellWait(call))
			} else {
				assert.Zero(t, quietShellWait(call))
			}
		})
	}
}

func TestQuietShellWaitIgnoresOtherOrInvalidCalls(t *testing.T) {
	for _, call := range []llm.ToolCall{
		{Name: toolKillShell, Arguments: `{"shell_ids":["shell-1"],"timeout_ms":3600000,"allow_long_wait":true}`},
		{Name: toolWaitShell, Arguments: `{"timeout_ms":3600000,"allow_long_wait":true}`},
		{Name: toolWaitShell, Arguments: `{"shell_ids":["shell-1"],"allow_long_wait":"true"}`},
	} {
		assert.Zero(t, quietShellWait(call))
	}
}
