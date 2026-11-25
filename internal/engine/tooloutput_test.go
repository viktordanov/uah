package engine_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/viktordanov/uah/internal/engine"
)

// item is a tool_call_status line as the runner writes it, with one
// operation.
func item(op string) []byte {
	return []byte(`{"Sequence":108,"RecordedAt":"2026-09-30T10:10:59Z","Kind":"tool_call_status","Data":{"TurnID":"t","CallID":"call_1","Status":{"Error":"","WaitingFor":["o"]},"Operations":[` + op + `]}}`)
}

func TestToolOutputFromItem(t *testing.T) {
	failed := item(`{"ID":"o","Type":"shell","Status":"completed","State":{"Result":{"Out":"partial","Err":"cat: x: No such file or directory\n","ExitCode":1}}}`)
	out, ok := engine.ToolOutputFromItem(failed)
	assert.True(t, ok)
	assert.Equal(t, "call_1", out.CallID)
	assert.Equal(t, "cat: x: No such file or directory\n", out.Output)

	quiet := item(`{"ID":"o","Type":"shell","Status":"completed","State":{"Result":{"Out":"FAIL ./x\n","Err":"","ExitCode":1}}}`)
	out, _ = engine.ToolOutputFromItem(quiet)
	assert.Equal(t, "FAIL ./x\n", out.Output, "stdout when stderr is empty")

	long := item(`{"ID":"o","Type":"shell","Status":"completed","State":{"Result":{"Err":"` + strings.Repeat("x", 5000) + `end","ExitCode":2}}}`)
	out, _ = engine.ToolOutputFromItem(long)
	assert.Len(t, out.Output, 4096)
	assert.True(t, strings.HasSuffix(out.Output, "end"), "the end of the output")

	_, ok = engine.ToolOutputFromItem(item(`{"ID":"o","Type":"shell","Status":"completed","State":{"Result":{"Out":"ok","ExitCode":0}}}`))
	assert.False(t, ok, "a command that succeeded leaves nothing")
	_, ok = engine.ToolOutputFromItem(item(`{"ID":"o","Type":"shell","Status":"running","State":{}}`))
	assert.False(t, ok, "nor one still running")

	mcp := item(`{"ID":"o","Type":"remote_job","Status":"completed","State":{"Plan":{"Type":"uah.mcp_call"},"TerminalResult":"{\"data\":1}"}}`)
	out, ok = engine.ToolOutputFromItem(mcp)
	assert.True(t, ok)
	assert.Equal(t, `{"data":1}`, out.Result)
	assert.Equal(t, 10, out.Size)

	mcpFailed := item(`{"ID":"o","Type":"remote_job","Status":"failed","State":{"Plan":{"Type":"uah.mcp_call"},"TerminalError":"401 Unauthorized"}}`)
	out, _ = engine.ToolOutputFromItem(mcpFailed)
	assert.Equal(t, "401 Unauthorized", out.Error)

	_, ok = engine.ToolOutputFromItem(item(`{"ID":"o","Type":"remote_job","Status":"completed","State":{"Plan":{"Type":"uah.apply_patch"}}}`))
	assert.False(t, ok, "a patch is PatchApplied's")

	advice := strings.Repeat("a", 5000) + "end"
	out, ok = engine.ToolOutputFromItem(item(`{"ID":"o","Type":"remote_job","Status":"completed","State":{"Plan":{"Type":"uah.advisor"},"TerminalResult":"` + advice + `"}}`))
	assert.True(t, ok)
	assert.Equal(t, advice, out.Result, "the advice whole")

	out, ok = engine.ToolOutputFromItem(item(`{"ID":"o","Type":"remote_job","Status":"failed","State":{"Plan":{"Type":"uah.advisor"},` +
		`"TerminalResult":"The advisor is unavailable (timeout after 10m); go on without it.","TerminalError":"timeout after 10m"}}`))
	assert.True(t, ok)
	assert.Equal(t, "timeout after 10m", out.Error, "a call that got no advice: why")
	assert.Equal(t, "The advisor is unavailable (timeout after 10m); go on without it.", out.Result, "what the agent read, for the detailed view")
}

// TestToolOutputFromItem_Skips: the quick test before the parse passes
// every item with an output, whatever the key order, and none of a
// command that succeeded or still runs.
func TestToolOutputFromItem_Skips(t *testing.T) {
	for _, op := range []string{
		`{"Type":"shell","State":{"Result":{"Out":"","Err":"x","ExitCode":-1},"TerminalError":""}}`,
		`{"Type":"shell","State":{"PendingExitCode":0,"Result":{"ExitCode":10}}}`,
		`{"Type":"shell","State":{"Result":null,"TerminalError":"killed"}}`,
	} {
		_, ok := engine.ToolOutputFromItem(item(op))
		assert.True(t, ok, op)
	}
	for _, op := range []string{
		`{"Type":"shell","State":{"PendingExitCode":1,"Result":{"Out":"\"ExitCode\":1","ExitCode":0},"TerminalError":""}}`,
		`{"Type":"shell","State":{"PendingExitCode":null,"Result":null,"TerminalError":""}}`,
	} {
		_, ok := engine.ToolOutputFromItem(item(op))
		assert.False(t, ok, op)
	}
}
