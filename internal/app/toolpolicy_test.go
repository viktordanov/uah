package app_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/agents"
	"github.com/viktordanov/uah/internal/app"
	"github.com/viktordanov/uah/internal/config"
	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/goal"
	"github.com/viktordanov/uah/internal/mcp"
	"github.com/viktordanov/uah/internal/patch"
	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/internal/toolpolicy"
	"github.com/viktordanov/uah/testing/fakellm"
)

// toolNames are a request's named tools, sorted (the hosted search has
// no name).
func toolNames(req fakellm.Request) []string {
	return slices.Sorted(slices.Values(slices.DeleteFunc(slices.Clone(req.ToolNames), func(n string) bool { return n == "" })))
}

// TestSetup_ToolPolicy runs a session whose configuration files each set
// [tools] allow: the layers can only narrow the user file's list. A
// subagent spawned with a role that names more tools gets the policy's
// intersection with the role's, a forced call to another tool is refused,
// and a PreToolUse hook that fails blocks an allowed call, because a
// session under a policy fails closed.
func TestSetup_ToolPolicy(t *testing.T) {
	e, in := setupEnv(t)
	t.Setenv("OPENAI_API_KEY", "test-key")
	dir := filepath.Dir(in.ConfigPath)
	write := func(path, text string) {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, os.WriteFile(path, []byte(text), 0o600))
	}
	write(in.ConfigPath, `[tools]
allow = ["Bash", "apply_patch", "spawn_agent", "wait_agent", "mcp__docs__*"]

[[hooks.PreToolUse]]
matcher = "apply_patch"
command = "echo broken >&2; exit 1"
`)
	write(filepath.Join(dir, "config.d", "host.toml"), "[tools]\nallow = [\"Bash\", \"apply_patch\", \"spawn_agent\", \"wait_agent\", \"ViewImage\"]\n")
	extra := filepath.Join(t.TempDir(), "extra.toml")
	write(extra, "[tools]\nallow = [\"Bash\", \"apply_patch\", \"spawn_agent\", \"wait_agent\", \"request_user_input\", \"web_search\"]\n")
	t.Setenv("UAH_EXTRA_CONFIG", extra)
	write(filepath.Join(dir, "agents", "wide.toml"), "name = \"wide\"\ndescription = \"Wants every tool\"\ndeveloper_instructions = \"Look around.\"\ntools = [\"Bash\", \"apply_patch\", \"ViewImage\", \"mcp__docs__*\"]\n")

	llm := fakellm.New(t,
		fakellm.Reply{Calls: []fakellm.Call{{Name: "spawn_agent", Args: `{"message":"CHILD-P look","agent_type":"wide"}`}}},
		fakellm.Reply{From: func(req fakellm.Request) fakellm.Reply {
			_, rest, ok := strings.Cut(req.ToolOutputs[0], `"agent_id":"`)
			if !ok {
				return fakellm.Reply{Text: "spawn failed: " + req.ToolOutputs[0]}
			}
			id, _, _ := strings.Cut(rest, `"`)

			return fakellm.Reply{Calls: []fakellm.Call{{Name: "wait_agent", Args: `{"targets":["` + id + `"]}`}}}
		}},
		fakellm.Reply{Text: "done"},
	)
	llm.Route("CHILD-P",
		fakellm.Reply{Calls: []fakellm.Call{
			{Name: "ViewImage", Args: `{"path":"x.png"}`},
			{Name: "apply_patch", Args: "*** Begin Patch\n*** Add File: patched.txt\n+x\n*** End Patch\n"},
			{Name: "spawn_agent", Args: `{"message":"grandchild"}`},
		}},
		fakellm.Reply{Text: "looked"},
	)
	in.Provider, in.Model, in.BaseURL = "openai", "gpt-test", llm.URL

	res, err := app.Setup(context.Background(), in, io.Discard)
	require.NoError(t, err)
	want := []string{"Bash", "apply_patch", "spawn_agent", "wait_agent"}
	assert.Equal(t, want, res.Options.Tools.Allow, "each layer narrowed the user file's list")
	opts := res.Options
	opts.Source = session.SourceTUI
	s, err := session.Open(context.Background(), res.Engine, opts)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	_, err = s.Submit("delegate")
	require.NoError(t, err)
	waitFinished(t, s)

	var root, child []fakellm.Request
	for _, req := range llm.Requests() {
		if strings.Contains(strings.Join(req.UserTexts, "\n"), "CHILD-P") {
			child = append(child, req)
		} else {
			root = append(root, req)
		}
	}
	require.NotEmpty(t, root)
	require.Len(t, child, 2, root[len(root)-1].ToolOutputs)
	assert.Equal(t, want, toolNames(root[0]))
	assert.Equal(t, []string{"Bash", "apply_patch"}, toolNames(child[0]), "the role narrows the policy and adds nothing")
	outputs := strings.Join(child[1].ToolOutputs, "\n")
	assert.Contains(t, outputs, `tool "ViewImage" is not available`)
	assert.Contains(t, outputs, "blocked by a PreToolUse hook: the hook failed (broken)")
	assert.Contains(t, outputs, `tool "spawn_agent" is not available in this session`)
	assert.NoFileExists(t, filepath.Join(e.Workspace, "patched.txt"))
}

// TestResolve_ToolPolicy: the flags and a resumed session's policy narrow
// the configured one; nothing widens it.
func TestResolve_ToolPolicy(t *testing.T) {
	allow := []string{"Bash", "mcp__docs__*"}
	cfg := config.Config{Tools: config.Tools{Allow: &allow, Deny: []string{"mcp__docs__write"}}}
	for _, c := range []struct {
		name    string
		in      app.Inputs
		resumed *toolpolicy.Policy
		want    toolpolicy.Policy
	}{
		{name: "the files", want: toolpolicy.Policy{Allow: allow, Deny: []string{"mcp__docs__write"}}},
		{name: "--tools adds nothing", in: app.Inputs{Tools: []string{"Bash", "apply_patch"}}, want: toolpolicy.Policy{Allow: []string{"Bash"}, Deny: []string{"mcp__docs__write"}}},
		{name: "--tools \"\" allows none", in: app.Inputs{Tools: []string{}}, want: toolpolicy.Policy{Allow: []string{}, Deny: []string{"mcp__docs__write"}}},
		{name: "--deny-tools", in: app.Inputs{DenyTools: []string{"Bash"}}, want: toolpolicy.Policy{Allow: allow, Deny: []string{"mcp__docs__write", "Bash"}}},
		{
			name: "a resumed session keeps its narrower policy", resumed: &toolpolicy.Policy{Allow: []string{"mcp__docs__read"}},
			want: toolpolicy.Policy{Allow: []string{"mcp__docs__read"}, Deny: []string{"mcp__docs__write"}},
		},
	} {
		in := c.in
		in.Workspace, in.MaxDisk = "/ws", "5G"
		r, err := app.Resolve(in, session.Info{Tools: c.resumed}, cfg)
		require.NoError(t, err, c.name)
		assert.Equal(t, c.want, r.Tools, c.name)
	}

	r, err := app.Resolve(app.Inputs{Workspace: "/ws", MaxDisk: "5G"}, session.Info{}, config.Config{})
	require.NoError(t, err)
	assert.False(t, r.Tools.Restricted(), "unset: every tool, as before")
	assert.True(t, r.RequestUserInput)

	r, err = app.Resolve(app.Inputs{Workspace: "/ws", MaxDisk: "5G", Tools: []string{"Bash"}}, session.Info{}, config.Config{})
	require.NoError(t, err)
	assert.False(t, r.RequestUserInput, "the prompt does not name a question tool the agent lacks")

	_, err = app.Resolve(app.Inputs{Workspace: "/ws", MaxDisk: "5G", DenyTools: []string{"bash"}}, session.Info{}, config.Config{})
	var usage *app.UsageError
	require.ErrorAs(t, err, &usage, "a typo would deny nothing, so it is an error")
	assert.Contains(t, err.Error(), `unknown tool "bash"`)
}

// TestSetup_ToolPolicyResume: a session started under a policy keeps it
// when it resumes without the flag, and a flag can only narrow it.
func TestSetup_ToolPolicyResume(t *testing.T) {
	_, in := setupEnv(t)
	in.Tools = []string{"Bash", "ViewImage"}
	res, err := app.Setup(context.Background(), in, io.Discard)
	require.NoError(t, err)
	opts := res.Options
	opts.Source = session.SourceRun
	s, err := session.Open(context.Background(), res.Engine, opts)
	require.NoError(t, err)
	id := s.ID()
	require.NoError(t, s.Close())

	in.Tools, in.SessionRef = nil, id
	res, err = app.Setup(context.Background(), in, io.Discard)
	require.NoError(t, err)
	assert.Equal(t, []string{"Bash", "ViewImage"}, res.Options.Tools.Allow, "resuming does not widen the session's tools")

	in.Tools = []string{"Bash", "apply_patch"}
	res, err = app.Setup(context.Background(), in, io.Discard)
	require.NoError(t, err)
	assert.Equal(t, []string{"Bash"}, res.Options.Tools.Allow)
}

// TestToolPolicy_KnowsEveryTool keeps toolpolicy.Builtins in step with the
// tools the engine offers, so a policy can name each of them.
func TestToolPolicy_KnowsEveryTool(t *testing.T) {
	names := append([]string{engine.QuestionToolName, "web_search", "Bash", "ViewImage", "SkillUse", patch.ToolName}, goal.ToolNames...)
	names = append(names, mcp.ListResourcesTool, mcp.ListResourceTemplatesTool, mcp.ReadResourceTool)
	for _, n := range agents.ToolNames() {
		if n != "wait" { // wait_agent's old name: resolved for old sessions, never offered
			names = append(names, n)
		}
	}
	for _, n := range names {
		assert.Contains(t, toolpolicy.Builtins, n)
	}
	assert.Len(t, toolpolicy.Builtins, len(names), "and names nothing else")
}
