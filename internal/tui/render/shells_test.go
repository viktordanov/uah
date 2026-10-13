package render_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/engine"
)

// TestShellLines: a Bash call that starts a background shell reads BG with
// the shell's ID, wait_shell and kill_shell read WAIT and KILL with the
// shells they name, and the footer counts the shells that run while they
// run.
func TestShellLines(t *testing.T) {
	s := apply(finishedRun(t),
		core.ToolCalled{At: t0, CallID: "c1", Name: "Bash", Arguments: `{"command":"npm run dev","background":true}`},
		engine.ShellUpdated{At: t0, ID: "shell-1", Command: "npm run dev", State: engine.ShellRunning, CallID: "c1", Started: t0},
		core.ToolCalled{At: t0, CallID: "c2", Name: "wait_shell", Arguments: `{"shell_ids":["shell-1"],"timeout_ms":1000}`},
		core.ToolCalled{At: t0, CallID: "c3", Name: "kill_shell", Arguments: `{"shell_id":"shell-1"}`},
	)
	out := screen(s, "")
	assert.Contains(t, out, "BG")
	assert.Contains(t, out, "shell-1 npm run dev")
	assert.Contains(t, out, "WAIT")
	assert.Contains(t, out, "KILL")
	assert.NotContains(t, out, `"shell_ids"`, "not the call's JSON")
	assert.Contains(t, footer(s), "1 shell running · /ps")

	s = apply(s, engine.ShellUpdated{At: t0, ID: "shell-1", Command: "npm run dev", State: engine.ShellKilled, CallID: "c1", Started: t0, Ended: t0})
	assert.False(t, strings.Contains(footer(s), "shell running"), "gone once none runs")
}

// TestBridgeCallLine: a call a command made with uah tool reads as an MCP
// call, marked as a script's, with its time, or its error.
func TestBridgeCallLine(t *testing.T) {
	s := apply(finishedRun(t),
		engine.ToolBridgeCalled{At: t0, Name: "mcp__docs__search", Arguments: `{"q":"retry"}`, OK: true, Duration: 1200 * time.Millisecond},
		engine.ToolBridgeCalled{At: t0, Name: "mcp__docs__get", Arguments: `{"id":"7"}`, Error: "blocked by a PreToolUse hook: no"},
	)
	out := screen(s, "")
	assert.Contains(t, out, "docs · search")
	assert.Contains(t, out, "· uah tool")
	assert.Contains(t, out, "blocked by a PreToolUse hook")
}
