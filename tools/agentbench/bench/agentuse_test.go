package bench_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/tools/agentbench/bench"
)

// TestCountAgentUse counts a main agent that spawns a child, waits for it
// once in vain, messages it while it works, gets its notification, waits
// again, and closes it once it is done.
func TestCountAgentUse(t *testing.T) {
	dir := t.TempDir()
	parent, child := newSession(t, "main"), newSession(t, "subagent-1")
	waitOp := func(call, result string) map[string]any {
		return map[string]any{"ID": "op-" + call, "Type": "remote_job", "Status": "completed", "State": map[string]any{"TerminalResult": result}}
	}
	finish := func(call, result string) {
		parent.item("tool_call_status", map[string]any{"TurnID": "t", "CallID": call, "Status": map[string]any{"WaitingFor": []string{"op-" + call}}}, waitOp(call, result))
	}

	parent.item("input", map[string]any{"ID": "u1", "Kind": "external", "Payload": "fix four bugs"})
	parent.request([2]string{"spawn_agent", `{"message":"fix csvq"}`})
	child.item("input", map[string]any{"ID": "c1", "Kind": "external", "Payload": "fix csvq"})
	w := parent.request([2]string{"wait_agent", `{"targets":["subagent-1"],"timeout_ms":10000}`})
	finish(w[0], `{"status":{},"timed_out":true}`)
	parent.request([2]string{"send_input", `{"target":"subagent-1","message":"status?"}`}, [2]string{"Bash", `{"command":"go test ./..."}`})
	child.request()
	parent.item("input", map[string]any{"ID": "n1", "Kind": "developer", "Payload": `<subagent_notification>{"agent_path":"subagent-1","status":{"completed":"done"}}</subagent_notification>`})
	w = parent.request([2]string{"wait_agent", `{"targets":["subagent-1"]}`})
	finish(w[0], `{"status":{"subagent-1":{"completed":"done"}},"timed_out":false}`)
	parent.request([2]string{"close_agent", `{"target":"subagent-1"}`})
	parent.write(dir, "main")
	child.write(dir, "subagent-1")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "sessions", "subagent-1.uah.json"), []byte(`{"parent":"main"}`), 0o644))

	a, err := bench.CountAgentUse(dir, "main")
	require.NoError(t, err)
	assert.Equal(t, bench.AgentUse{
		Spawns: 1, Messages: 1, ToRunning: 1, Closes: 1, Waits: 2, WaitsTimedOut: 1, Notes: 1,
		BusyRequests: 2, AgentOnlyRequests: 1,
	}, a, "the wait and the message came while the child worked; only the wait's request did nothing else")
	assert.Equal(t, 1, a.Interventions())

	none, err := bench.CountAgentUse(dir, "subagent-1")
	require.NoError(t, err)
	assert.False(t, none.Used(), "a session that spawned nothing has no counts")
}

// TestCountAgentUse_WorkWithoutAnAnswer counts an interrupt that reaches a
// child during a tool call, after its last response, and a close of a child
// that never answered, as reaching working children.
func TestCountAgentUse_WorkWithoutAnAnswer(t *testing.T) {
	dir := t.TempDir()
	parent, busy, silent := newSession(t, "main"), newSession(t, "subagent-1"), newSession(t, "subagent-2")
	parent.request([2]string{"spawn_agent", `{"message":"one"}`}, [2]string{"spawn_agent", `{"message":"two"}`})
	busy.item("input", map[string]any{"ID": "c1", "Kind": "external", "Payload": "one"})
	silent.item("input", map[string]any{"ID": "c2", "Kind": "external", "Payload": "two"})
	busy.request([2]string{"Bash", `{"command":"go test ./..."}`})
	parent.request([2]string{"send_input", `{"target":"subagent-1","message":"stop","interrupt":true}`})
	busy.item("input", map[string]any{"ID": "c3", "Kind": "external", "Payload": "stop"})
	busy.request()
	parent.request([2]string{"close_agent", `{"target":"subagent-2"}`})
	parent.write(dir, "main")
	for _, c := range []struct {
		w  *sessionWriter
		id string
	}{{busy, "subagent-1"}, {silent, "subagent-2"}} {
		c.w.write(dir, c.id)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "sessions", c.id+".uah.json"), []byte(`{"parent":"main"}`), 0o644))
	}

	a, err := bench.CountAgentUse(dir, "main")
	require.NoError(t, err)
	assert.Equal(t, 1, a.Interrupts)
	assert.Equal(t, 1, a.ToRunning, "the interrupt came during the tool call")
	assert.Equal(t, 1, a.ClosedRunning, "the closed child never answered")
	assert.Equal(t, 2, a.Interventions())
}
