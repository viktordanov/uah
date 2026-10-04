package embedded_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/engine/embedded"
	"github.com/viktordanov/uah/internal/hooks"
	"github.com/viktordanov/uah/internal/mcp"
	"github.com/viktordanov/uah/internal/sandbox"
	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/internal/toolpolicy"
	"github.com/viktordanov/uah/testing/fakellm"
)

// hookLog is a PreToolUse hook on every tool that appends each call's
// payload to log and answers "allow", and a PostToolUse hook that appends
// each finished call's payload to log too.
func hookLog(t *testing.T, workspace, log string) *hooks.Runner {
	t.Helper()
	record := `cat >> ` + log + `; echo >> ` + log
	runner, err := hooks.New([]hooks.Hook{
		{Event: hooks.PreToolUse, Source: hooks.SourceUser, Command: record + `; echo '{"hookSpecificOutput":{"permissionDecision":"allow"}}'`},
		{Event: hooks.PostToolUse, Source: hooks.SourceUser, Command: record},
	}, nil, workspace)
	require.NoError(t, err)

	return runner
}

// TestEmbedded_ToolPolicy: the model is offered only the tools the policy
// allows, and a call to any other, forced or made up, is refused before a
// PreToolUse hook, an approval, or a job sees it, even though the hook
// answers "allow" for every tool.
func TestEmbedded_ToolPolicy(t *testing.T) {
	t.Parallel()
	marker := filepath.Join(t.TempDir(), "patched")
	e := newEnv(t,
		fakellm.Reply{Calls: []fakellm.Call{
			call("Bash", `{"command":"echo allowed"}`),
			call("mcp__test__echo", `{"text":"hi"}`),
			call("mcp__test__env", `{"text":"HOME"}`),
			call("apply_patch", "*** Begin Patch\n*** Add File: "+marker+"\n+x\n*** End Patch\n"),
			call("ViewImage", `{"path":"x.png"}`),
			call("read_mcp_resource", `{"server":"test","uri":"test://greeting"}`),
			call("list_mcp_resources", `{}`),
			call("request_user_input", `{"questions":[]}`),
			call("get_goal", `{}`),
			call("made_up", `{}`),
		}},
		fakellm.Reply{Text: "done"},
	)
	log := filepath.Join(t.TempDir(), "hook.log")
	runner := hookLog(t, e.Workspace, log)
	policy := toolpolicy.Policy{Allow: []string{"Bash", "mcp__test__echo", "web_search", "ViewImage"}, Deny: []string{"ViewImage"}}
	eng := embedded.New(embedded.Config{
		StateDir: e.StateDir, Provider: "openai", Getenv: e.getenv, Hooks: runner, MCP: mcpManager(t, e, mcp.ServerConfig{}),
		WebSearch: true, AskUser: true, Goals: true, Tools: policy,
	})
	s, err := session.Open(context.Background(), eng, session.Options{Settings: e.settings(), Hooks: runner, Tools: policy})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	ev := &events{t: t, s: s}

	_, err = s.Submit("use the tools")
	require.NoError(t, err)
	assert.Equal(t, core.StatusOK, ev.finished().Status)

	reqs := e.llm.Requests()
	require.Len(t, reqs, 2)
	named := slices.DeleteFunc(slices.Clone(reqs[0].ToolNames), func(n string) bool { return n == "" }) // the hosted search has no name
	assert.Equal(t, []string{"Bash", "mcp__test__echo"}, slices.Sorted(slices.Values(named)), "only the allowed tools are offered")
	assert.True(t, offersWebSearch(reqs[0]), "the hosted search is allowed too")
	outputs := reqs[1].ToolOutputs
	require.Len(t, outputs, 10)
	assert.Contains(t, strings.Join(outputs, "\n"), "allowed")
	assert.Contains(t, strings.Join(outputs, "\n"), "echo: hi")
	for _, name := range []string{"mcp__test__env", "apply_patch", "read_mcp_resource", "list_mcp_resources", "request_user_input", "get_goal"} {
		assert.Contains(t, strings.Join(outputs, "\n"), `tool "`+name+`" is not available in this session: the tool policy does not allow it`, name)
	}
	// The runner never registers a built-in tool the request disallows.
	assert.Contains(t, strings.Join(outputs, "\n"), `tool "ViewImage" is not available`)
	assert.Contains(t, strings.Join(outputs, "\n"), `tool "made_up" is not available`)
	assert.NoFileExists(t, marker, "the refused patch never ran")
	data, err := os.ReadFile(log)
	require.NoError(t, err)
	assert.Equal(t, 2, strings.Count(string(data), `"hook_event_name":"PreToolUse"`), "the hooks ran for the two allowed calls only")
	assert.Equal(t, 2, strings.Count(string(data), `"hook_event_name":"PostToolUse"`), "before and after them")
	for _, name := range []string{"mcp__test__env", "apply_patch", "ViewImage", "read_mcp_resource", "request_user_input", "get_goal", "made_up"} {
		assert.NotContains(t, string(data), `"tool_name":"`+name+`"`, "no hook saw the refused call")
	}
}

// TestEmbedded_ToolPolicyNone: an empty allowlist offers no tools at all,
// and a forced Bash call does not run.
func TestEmbedded_ToolPolicyNone(t *testing.T) {
	t.Parallel()
	marker := filepath.Join(t.TempDir(), "ran")
	e := newEnv(t, fakellm.Reply{Commands: []string{"touch " + marker}}, fakellm.Reply{Text: "done"})
	writeSkill(t, filepath.Join(e.Workspace, ".agents", "skills"), "release", "Cut a release")
	eng := embedded.New(embedded.Config{StateDir: e.StateDir, Provider: "openai", Getenv: e.getenv, WebSearch: true, Tools: toolpolicy.Policy{Allow: []string{}}})
	s, ev := e.open(t, eng, "")

	_, err := s.Submit("hi")
	require.NoError(t, err)
	assert.Equal(t, core.StatusOK, ev.finished().Status)

	reqs := e.llm.Requests()
	require.Len(t, reqs, 2)
	assert.Empty(t, reqs[0].ToolDefs, "no tools, hosted ones included")
	assert.NotContains(t, reqs[0].System, "Cut a release", "no SkillUse, so no skills in the prompt")
	assert.Contains(t, reqs[1].ToolOutputs[0], `tool "Bash" is not available`)
	assert.NoFileExists(t, marker)
}

// TestEmbedded_ToolPolicyResources: the MCP resource tools reach only a
// server the policy allows as a whole.
func TestEmbedded_ToolPolicyResources(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		allow []string
		want  string
	}{
		{allow: []string{"mcp__test__*", "read_mcp_resource", "list_mcp_resources"}, want: "hello from the resource"},
		{allow: []string{"mcp__test__echo", "read_mcp_resource", "list_mcp_resources"}, want: "for this server, the tool policy does not allow it"},
	} {
		e := newEnv(t, fakellm.Reply{Calls: []fakellm.Call{
			call("read_mcp_resource", `{"server":"test","uri":"test://greeting"}`),
			call("list_mcp_resources", `{}`),
		}}, fakellm.Reply{Text: "done"})
		eng := embedded.New(embedded.Config{StateDir: e.StateDir, Provider: "openai", Getenv: e.getenv, MCP: mcpManager(t, e, mcp.ServerConfig{}), Tools: toolpolicy.Policy{Allow: c.allow}})
		s, ev := e.open(t, eng, "")
		_, err := s.Submit("read")
		require.NoError(t, err)
		assert.Equal(t, core.StatusOK, ev.finished().Status)
		outputs := e.llm.Requests()[1].ToolOutputs
		require.Len(t, outputs, 2)
		assert.Contains(t, strings.Join(outputs, "\n"), c.want, c.allow)
		assert.Contains(t, strings.Join(outputs, "\n"), "that the tool policy allows as a whole", "listing every server is refused")
	}
}

// TestEmbedded_ToolPolicyHookFailure: under a policy, a PreToolUse hook that
// fails blocks an allowed call (the runner fails closed, as app.Setup sets
// it); without one, the failure is ignored and the call runs.
func TestEmbedded_ToolPolicyHookFailure(t *testing.T) {
	t.Parallel()
	for _, restricted := range []bool{false, true} {
		marker := filepath.Join(t.TempDir(), "ran")
		e := newEnv(t, fakellm.Reply{Commands: []string{"touch " + marker}}, fakellm.Reply{Text: "done"})
		runner, err := hooks.New([]hooks.Hook{{Event: hooks.PreToolUse, Matcher: "Bash", Source: hooks.SourceUser, Command: "echo broken >&2; exit 1"}}, nil, e.Workspace)
		require.NoError(t, err)
		var policy toolpolicy.Policy
		if restricted {
			policy = toolpolicy.Policy{Allow: []string{"Bash"}}
			runner.FailClosed()
		}
		eng := embedded.New(embedded.Config{StateDir: e.StateDir, Provider: "openai", Getenv: e.getenv, Hooks: runner, Tools: policy})
		s, ev := e.open(t, eng, "")
		_, err = s.Submit("touch it")
		require.NoError(t, err)
		ev.finished()
		if restricted {
			assert.NoFileExists(t, marker)
			assert.Contains(t, e.llm.Requests()[1].ToolOutputs[0], "blocked by a PreToolUse hook: the hook failed (broken)")
		} else {
			assert.FileExists(t, marker, "Claude Code's and Codex's semantics: exit 1 is a non-blocking error")
		}
	}
}

// TestEmbedded_InterruptedPreToolUseHook: when the user interrupts the run
// while a PreToolUse hook still decides, the call does not run unchecked.
func TestEmbedded_InterruptedPreToolUseHook(t *testing.T) {
	t.Parallel()
	for _, sandboxed := range []bool{false, true} {
		ws := t.TempDir()
		started := filepath.Join(t.TempDir(), "started")
		e := newEnv(t, fakellm.Reply{Commands: []string{"touch ran"}}, fakellm.Reply{Text: "done"})
		e.Workspace = ws
		runner, err := hooks.New([]hooks.Hook{{Event: hooks.PreToolUse, Matcher: "Bash", Source: hooks.SourceUser, Command: "touch " + started + "; sleep 10"}}, nil, ws)
		require.NoError(t, err)
		cfg := embedded.Config{StateDir: e.StateDir, Provider: "openai", Getenv: e.getenv, Hooks: runner}
		if sandboxed {
			policy := sandbox.Policy{Mode: sandbox.WorkspaceWrite, Workspace: ws}
			if _, err := policy.Wrap([]string{"/bin/sh"}); err != nil {
				t.Skipf("no sandbox here: %v", err)
			}
			cfg.Sandbox, cfg.SandboxDir = &policy, filepath.Join(e.StateDir, "sandbox")
		}
		s, ev := e.open(t, embedded.New(cfg), "")

		_, err = s.Submit("touch it")
		require.NoError(t, err)
		require.Eventually(t, func() bool { _, err := os.Stat(started); return err == nil }, waitTimeout, 10*time.Millisecond)
		require.NoError(t, s.Interrupt())
		ev.finished()
		time.Sleep(300 * time.Millisecond) // a command that started would have touched it by now
		assert.NoFileExists(t, filepath.Join(ws, "ran"), "sandboxed %v: the interrupted hook never decided, so the command did not run", sandboxed)
	}
}

// TestEmbedded_NoSkills: with skills off, the workspace's skills are not
// discovered, offered, or listed in the prompt.
func TestEmbedded_NoSkills(t *testing.T) {
	t.Parallel()
	e := newEnv(t, fakellm.Reply{Text: "ok"})
	writeSkill(t, filepath.Join(e.Workspace, ".agents", "skills"), "release", "Cut a release from the project")
	eng := embedded.New(embedded.Config{StateDir: e.StateDir, Provider: "openai", Getenv: e.getenv, NoSkills: true})
	s, ev := e.open(t, eng, "")
	_, err := s.Submit("hi")
	require.NoError(t, err)
	ev.finished()

	req := e.llm.Requests()[0]
	assert.NotContains(t, req.Tools, "SkillUse")
	assert.NotContains(t, req.System, "Cut a release from the project")
	assert.Contains(t, req.Tools, "Bash", "the other tools stay")
}
