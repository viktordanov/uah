package main

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/engine"
)

// TestPrinter_Agents prints a subagent's state, and its tool calls with
// --verbose.
func TestPrinter_Agents(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	p := newPrinter(&out, true)
	p.print(engine.AgentUpdated{ID: "a1", Nickname: "Ada", State: engine.AgentRunning})
	p.print(engine.AgentActivity{ID: "a1", Event: core.ToolFinished{Name: "Bash", Label: "ls", OK: true, Detail: "exit 0", Duration: time.Second}})

	assert.Contains(t, out.String(), "agent Ada: running")
	assert.Contains(t, out.String(), "agent Ada: Bash  ls (exit 0, 1.0s)")
	p.print(engine.AgentUpdated{ID: "a1", Nickname: "Ada", State: engine.AgentErrored, Message: "The 'gpt-luna-6' model is not supported"})
	assert.Contains(t, out.String(), "agent Ada: failed: The 'gpt-luna-6' model is not supported")

	out.Reset()
	quiet := newPrinter(&out, false)
	quiet.print(engine.AgentActivity{ID: "a1", Event: core.ToolFinished{Name: "Bash"}})
	assert.Empty(t, out.String())
}

// TestPrinter_Shells prints a background shell's start and end.
func TestPrinter_Shells(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	p := newPrinter(&out, false)
	p.print(engine.ShellUpdated{ID: "shell-1", Command: "make  build", State: engine.ShellRunning, Log: "/tmp/shell-1.log"})
	p.print(engine.ShellUpdated{ID: "shell-1", State: engine.ShellExited, ExitCode: 2})
	p.print(engine.ShellUpdated{ID: "shell-2", State: engine.ShellKilled})

	assert.Contains(t, out.String(), "shell shell-1: started: make build (log /tmp/shell-1.log)")
	assert.Contains(t, out.String(), "shell shell-1: exited 2")
	assert.Contains(t, out.String(), "shell shell-2: stopped")
}

// TestPrinter_EffortUpdatesOff warns on stderr when the backend rejects
// effort updates.
func TestPrinter_EffortUpdatesOff(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	newPrinter(&out, false).print(engine.EffortUpdatesOff{Err: "rejected"})

	assert.Contains(t, out.String(), "warning: effort updates were rejected by the backend; switching effort per request (cache misses on switches)")
}
