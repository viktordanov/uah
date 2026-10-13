package embedded

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/approval"
	"github.com/viktordanov/uah/internal/mcp"
	"github.com/viktordanov/uah/internal/toolbridge"
	"github.com/viktordanov/uah/testing/harnesstest"
)

// TestBridge_RefusesProcessesOutsideThisUah: a process that is not this
// uah's descendant is refused also with the right token, as another uah's
// command, which could read that uah's tokens, would be.
func TestBridge_RefusesProcessesOutsideThisUah(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("no python3")
	}
	short, err := os.MkdirTemp("/tmp", "br")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(short) })
	br, err := listenBridge(filepath.Join(short, "tmp"), filepath.Join(short, "ops"))
	require.NoError(t, err)
	br.wg.Go(br.serve)
	t.Cleanup(br.close)
	dir := filepath.Join(short, "tmp")

	own, err := toolbridge.Do(t.Context(), dir, toolbridge.Request{Op: toolbridge.OpList, Token: br.token})
	require.NoError(t, err)
	assert.True(t, own.OK, "this process's own request: %s", own.Error)

	// A detached process: its parent exits first, so init or launchd
	// adopts it before it connects.
	out := filepath.Join(short, "out")
	script := `import os,socket,time; time.sleep(0.5); os.chdir(os.environ["D"]); s=socket.socket(socket.AF_UNIX); s.connect("` + toolbridge.SocketName + `"); ` +
		`s.sendall(('{"op":"list","token":"' + os.environ["T"] + '"}\n').encode()); open(os.environ["O"],"w").write(s.makefile().readline())`
	cmd := exec.CommandContext(context.Background(), "/bin/sh", "-c", `"$P" -c "$S" >/dev/null 2>&1 &`)
	cmd.Env = append(os.Environ(), "P="+python, "S="+script, "D="+dir, "T="+br.token, "O="+out)
	require.NoError(t, cmd.Run())
	deadline := time.Now().Add(10 * time.Second)
	var got []byte
	for time.Now().Before(deadline) {
		if got, err = os.ReadFile(out); err == nil && len(got) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	require.NotEmpty(t, got, "the detached process never answered")
	assert.Contains(t, string(got), `"ok":false`)
	assert.Contains(t, string(got), "not one of this session's")
}

// TestBridge_ApprovesAToolOncePerCommand: a script's burst of calls to a
// tool that asks waits on the first call's answer, so one command asks
// once; another command asks again, and a refusal is not kept.
func TestBridge_ApprovesAToolOncePerCommand(t *testing.T) {
	m, err := mcp.NewManager(map[string]mcp.ServerConfig{"test": {Command: harnesstest.MCPServer(t)}}, mcp.Options{Workspace: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { _ = m.Close() })
	tools, err := m.Tools(t.Context())
	require.NoError(t, err)
	i := slices.IndexFunc(tools, func(tl mcp.Tool) bool { return tl.Name == "mcp__test__echo" })
	require.GreaterOrEqual(t, i, 0)
	echo := tools[i]
	echo.Approval = mcp.ApprovalPrompt

	var asks atomic.Int64
	answer := approval.Approve
	ask := func(context.Context, approval.Prompt) approval.Answer {
		asks.Add(1)
		time.Sleep(100 * time.Millisecond) // a reviewer takes a while

		return answer
	}
	br := &bridge{}
	s := bridgeState{manager: m, tools: []mcp.Tool{echo}, gate: mcpGate{ask: ask}}
	call := func(cmd string) toolbridge.Response {
		ctx := context.WithValue(t.Context(), commandKey{}, cmd)

		return br.call(ctx, s, toolbridge.Request{Op: toolbridge.OpCall, Name: echo.Name, Arguments: json.RawMessage(`{"text":"x"}`)})
	}

	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() { assert.True(t, call("cmd-1").OK) })
	}
	wg.Wait()
	assert.EqualValues(t, 1, asks.Load(), "the burst waits on the first answer")
	require.True(t, call("cmd-2").OK)
	assert.EqualValues(t, 2, asks.Load(), "another command asks again")

	answer = approval.Decline
	assert.False(t, call("cmd-3").OK)
	assert.False(t, call("cmd-3").OK)
	assert.EqualValues(t, 4, asks.Load(), "a refusal is not kept: the next call asks again")
}

// TestBridge_KeepsNoHooksAllowance: a PreToolUse hook's allowance is for
// its call, so the command's next call of the tool, which no hook allows,
// asks.
func TestBridge_KeepsNoHooksAllowance(t *testing.T) {
	m, err := mcp.NewManager(map[string]mcp.ServerConfig{"test": {Command: harnesstest.MCPServer(t)}}, mcp.Options{Workspace: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { _ = m.Close() })
	tools, err := m.Tools(t.Context())
	require.NoError(t, err)
	i := slices.IndexFunc(tools, func(tl mcp.Tool) bool { return tl.Name == "mcp__test__echo" })
	require.GreaterOrEqual(t, i, 0)
	echo := tools[i]
	echo.Approval = mcp.ApprovalPrompt
	var asks atomic.Int64
	ask := func(context.Context, approval.Prompt) approval.Answer { asks.Add(1); return approval.Decline }
	br := &bridge{}
	s := bridgeState{manager: m, tools: []mcp.Tool{echo}, gate: mcpGate{ask: ask}}
	ctx := context.WithValue(t.Context(), commandKey{}, "cmd")

	assert.Empty(t, br.approve(context.WithValue(ctx, hookAllowKey{}, true), s, echo, `{"text":"safe"}`), "the hook allows this call")
	assert.NotEmpty(t, br.approve(ctx, s, echo, `{"text":"other"}`), "the next call asks, and is declined")
	assert.EqualValues(t, 1, asks.Load())
}
