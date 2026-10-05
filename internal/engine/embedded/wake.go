package embedded

import (
	"cmp"
	"time"

	"github.com/viktordanov/uah-core/harness/coordinator"
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
	return coordinator.WakePolicy{Hold: cmp.Or(hold, wakeHold), Progress: bash.Progress}
}
