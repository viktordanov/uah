package embedded

import (
	"cmp"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/viktordanov/uah-core/harness/coordinator"
	"github.com/viktordanov/uah-core/harness/llm"
	"github.com/viktordanov/uah-core/harness/operation"
	"github.com/viktordanov/uah-core/harness/tool/bash"

	"github.com/viktordanov/uah/internal/engine"
)

// wakeHold is how long a turn's results wait for its running calls before
// a call still running wakes the model with its output so far: the longest
// wait of Codex's write_stdin on a running command.
const wakeHold = 5 * time.Minute

// waitSlack is how much longer than its timeout a wait is held, for its
// result to come back.
const waitSlack = 30 * time.Second

// wakePolicy is the coordinator's wake policy: the model is not woken just
// to hear that a call is still running. A turn's results wait until every
// call it issued has finished, or a call has run for hold (wakeHold when
// 0). A wait_shell or wait_agent call is held for its own timeout, and an
// advisor call for the advisor's (waitHold).
func wakePolicy(hold, maxWait, advisor time.Duration, agents engine.Subagents) coordinator.WakePolicy {
	hold = cmp.Or(hold, wakeHold)

	return coordinator.WakePolicy{Hold: hold, CallHold: waitHold(hold, maxWait, advisor, agents), Progress: progress}
}

// waitHold holds a call that may last longer than hold until its timeout
// passes, plus waitSlack, so the model is not woken only to wait again: a
// wait, or an advisor call; any other call keeps hold (0).
func waitHold(hold, maxWait, advisor time.Duration, agents engine.Subagents) func(llm.ToolCall) time.Duration {
	return func(call llm.ToolCall) time.Duration {
		timeout, ok := shellWaitTimeout(call, maxWait)
		if !ok {
			timeout, ok = advisorHold(call, advisor)
		}
		if !ok && agents != nil {
			timeout, ok = agents.WaitTimeout(call.Name, json.RawMessage(call.Arguments))
		}
		if !ok || timeout+waitSlack <= hold {
			return 0
		}

		return timeout + waitSlack
	}
}

// progress is a running call's output so far (bash.Progress), then how to
// stop it: its process group, which a kill of the session's own commands
// may signal from any sandbox (commands.ownKill). In the sandbox a
// command cannot see another command's processes on Linux, nor signal
// them on macOS, so the group is the one handle the model has.
func progress(ops []operation.Operation) string {
	out := bash.Progress(ops)
	for _, op := range ops {
		state, err := operation.DecodeShellState(op)
		if op.Type != operation.TypeShell || err != nil || state.ProcessGroupID <= 1 {
			continue
		}
		hint := fmt.Sprintf("uah: it runs as process group %d; to stop it, run kill -- -%d", state.ProcessGroupID, state.ProcessGroupID)
		out = strings.TrimSpace(out + "\n" + hint)
	}

	return out
}
