package app_test

import (
	"context"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/app"
	"github.com/viktordanov/uah/internal/contextprep"
	"github.com/viktordanov/uah/internal/goal"
	"github.com/viktordanov/uah/internal/instructions"
	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/testing/fakellm"
	"github.com/viktordanov/uah/testing/harnesstest"
)

// agentTools are the tools only a session that may spawn is offered.
var agentTools = []string{"spawn_agent", "send_input", "wait_agent", "close_agent", "resume_agent"}

// TestSetup_SubagentParity pins that a subagent is the root agent in every
// way but what makes it a child: set up with instructions, a skill, an MCP
// server, hooks, and fast mode, its model request carries the root's
// system prompt, model, effort, service tier, and tools (less the agent
// tools, at max_depth 1, and the goal tools, since a goal is the root's
// alone), and its session has the same hooks, of which it
// fires the subagent ones only (see TestSetup_SubagentHooks).
func TestSetup_SubagentParity(t *testing.T) {
	e, in := setupEnv(t)
	t.Setenv("OPENAI_API_KEY", "test-key")
	require.NoError(t, os.WriteFile(filepath.Join(e.Workspace, "AGENTS.md"), []byte("Use tabs in Go files."), 0o600))
	skill := filepath.Join(e.Workspace, ".agents", "skills", "release")
	require.NoError(t, os.MkdirAll(skill, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("---\nname: release\ndescription: Cut a release\n---\n\nSteps.\n"), 0o600))
	stops := filepath.Join(e.StateDir, "stops.txt")
	require.NoError(t, os.MkdirAll(filepath.Dir(in.ConfigPath), 0o700))
	require.NoError(t, os.WriteFile(in.ConfigPath, []byte(`
[mcp_servers.test]
command = "`+harnesstest.MCPServer(t)+`"

[[hooks.PreToolUse]]
command = "true"

[[hooks.Stop]]
command = "grep -o '\"session_id\":\"[^\"]*\"' >> `+stops+`"
`), 0o600))
	llm := fakellm.New(t,
		fakellm.Reply{Calls: []fakellm.Call{{Name: "spawn_agent", Args: `{"message":"CHILD-P check the build"}`}}},
		fakellm.Reply{From: func(req fakellm.Request) fakellm.Reply {
			id := strings.Split(strings.Split(req.ToolOutputs[0], `"agent_id":"`)[1], `"`)[0]
			return fakellm.Reply{Calls: []fakellm.Call{{Name: "wait_agent", Args: `{"targets":["` + id + `"]}`}}}
		}},
		fakellm.Reply{Text: "done"},
	)
	llm.Route("CHILD-P", fakellm.Reply{Text: "the build is fine"})
	in.Provider, in.Model, in.Effort, in.BaseURL = "openai", "gpt-test", "medium", llm.URL
	in.Fast, in.FastSet = true, true

	res, err := app.Setup(context.Background(), in, io.Discard)
	require.NoError(t, err)
	opts := res.Options
	opts.Source = session.SourceTUI
	s, err := session.Open(context.Background(), res.Engine, opts)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	_, err = s.Submit("delegate")
	require.NoError(t, err)
	waitFinished(t, s)
	require.NoError(t, s.Close())

	var root, child *fakellm.Request
	for _, r := range llm.Requests() {
		isChild := slices.ContainsFunc(r.UserTexts, func(t string) bool { return strings.HasPrefix(t, "CHILD-P") })
		switch {
		case isChild && child == nil:
			child = &r
		case !isChild && root == nil:
			root = &r
		}
	}
	require.NotNil(t, root)
	require.NotNil(t, child)
	assert.Contains(t, root.System, "Use tabs in Go files.")
	assert.Contains(t, root.System, instructions.DefaultPrompt, "uah's default base instructions reach the model")
	assert.Contains(t, root.System, "<cwd>"+e.Workspace+"</cwd>", "and so does the environment")
	assert.Equal(t, root.System, child.System, "the parent's system prompt, byte for byte, for the cache")
	require.Len(t, child.DeveloperTexts, 1)
	assert.True(t, contextprep.IsPrepared(child.DeveloperTexts[0]), "a subagent's session starts with prepared context, a developer message")
	require.Len(t, child.UserTexts, 1)
	assert.True(t, strings.HasSuffix(child.UserTexts[0], "\n\n"+instructions.SubagentNote), "Codex's note that the final answer reaches the parent follows the task")
	assert.Equal(t, [3]string{root.Model, root.Effort, root.ServiceTier}, [3]string{child.Model, child.Effort, child.ServiceTier})
	assert.Equal(t, "priority", child.ServiceTier)
	rootTools := maps.Clone(root.Tools)
	for _, name := range slices.Concat(agentTools, goal.ToolNames) {
		assert.Contains(t, rootTools, name)
		delete(rootTools, name)
	}
	assert.Equal(t, rootTools, child.Tools, "the same tools with the same schemas, less the goal's, which no child inherits")
	assert.Contains(t, child.Tools, "mcp__test__echo")
	assert.Contains(t, child.Tools, "SkillUse")

	require.Len(t, sessionIDs(t, e.StateDir), 2)
	data, err := os.ReadFile(stops)
	require.NoError(t, err)
	assert.Equal(t, `"session_id":"`+s.ID()+`"`+"\n", string(data), "Stop hooks ran for the root only")
}

func waitFinished(t *testing.T, s *session.Session) {
	t.Helper()
	deadline := time.After(30 * time.Second)
	for {
		select {
		case e := <-s.Events():
			if _, ok := e.(session.Idle); ok {
				return
			}
			if f, ok := e.(core.RunFinished); ok {
				require.Equal(t, core.StatusOK, f.Result.Status, f.Result.Answer)
			}
		case <-deadline:
			t.Fatal("the run did not finish")
		}
	}
}

func sessionIDs(t *testing.T, stateDir string) []string {
	t.Helper()
	infos, err := session.Sessions(stateDir)
	require.NoError(t, err)
	var out []string
	for _, in := range infos {
		out = append(out, in.ID)
	}

	return out
}
