package embedded

import (
	"cmp"
	"fmt"
	"strings"
	"time"

	"github.com/viktordanov/uah-core/harness/coordinator"
	"github.com/viktordanov/uah-core/harness/operation"
	"github.com/viktordanov/uah-core/harness/tool/bash"
)

// wakeHold is how long a turn's results wait for its running calls before
// a call still running wakes the model with its output so far: the longest
// wait of Codex's write_stdin on a running command.
const wakeHold = 5 * time.Minute

// wakePolicy is the coordinator's wake policy: the model is not woken just
// to hear that a call is still running. A turn's results wait until every
// call it issued has finished, or a call has run for hold (wakeHold when
// 0).
func wakePolicy(hold time.Duration) coordinator.WakePolicy {
	return coordinator.WakePolicy{Hold: cmp.Or(hold, wakeHold), Progress: progress}
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
