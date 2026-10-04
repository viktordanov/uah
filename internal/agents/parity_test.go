package agents_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/agents"
	"github.com/viktordanov/uah/internal/hooks"
	"github.com/viktordanov/uah/internal/instructions"
	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/testing/fakellm"
)

// TestParity_ChildOptions pins that a child opens with the options the
// root session opened with, differing only in its ID, its sidecar's source
// and parent, approvals through the parent, the parent's grants, a hook
// runner of its own with the same hooks, and no streaming. The settings are the parent run's,
// with the default base instructions as the root's: Codex's subagent note
// goes before the child's task instead.
func TestParity_ChildOptions(t *testing.T) {
	runner, err := hooks.New([]hooks.Hook{{Event: hooks.Stop, Command: "true", Source: hooks.SourceUser}}, nil, "")
	require.NoError(t, err)
	e := newEnv(t, agents.Config{}, fakellm.Reply{Text: "done"})
	e.hooks, e.stream = runner, true
	s, ev := e.open(t, true)
	_, err = s.Submit("hello")
	require.NoError(t, err)
	ev.finished()

	root := session.Options{
		Settings: e.settings(), Hooks: runner, SessionsDir: e.sessionsDir(), Source: session.SourceTUI, Interactive: true, Stream: true,
	}
	child := e.mgr.ChildOptions(s.ID())

	assert.Equal(t, "child-id", child.ID)
	assert.Equal(t, session.SourceSubagent, child.Source)
	assert.Equal(t, s.ID(), child.Parent)
	assert.NotNil(t, child.Ask, "approvals go through the parent")
	assert.NotSame(t, runner, child.Hooks, "a runner of its own, so its results stay its own")
	assert.Equal(t, runner.Hooks(), child.Hooks.Hooks())
	assert.False(t, child.Stream, "a child's text does not stream")
	assert.NotNil(t, child.Grants, "the parent's grants")
	assert.Equal(t, instructions.DefaultPrompt, child.Settings.SystemPrompt)
	root.Settings.SystemPrompt = child.Settings.SystemPrompt
	child.ID, child.Source, child.Parent, child.Ask, child.Hooks, child.Stream = root.ID, root.Source, root.Parent, root.Ask, root.Hooks, root.Stream
	child.Grants = root.Grants
	assert.Equal(t, root, child)
}

// TestParity_ChildSharesTheParentsSystemPrompt pins that a child's model
// requests carry its parent's system prompt byte for byte, for the cache,
// and that Codex's subagent note reaches the child once, with its task.
func TestParity_ChildSharesTheParentsSystemPrompt(t *testing.T) {
	e := newEnv(t, agents.Config{},
		fakellm.Reply{Calls: []fakellm.Call{call("spawn_agent", `{"message":"CHILD-P task"}`)}},
		callWith("wait_agent", `{"targets":["ID"]}`),
		fakellm.Reply{Text: "done"},
	)
	e.llm.Route("CHILD-P", fakellm.Reply{Text: "child answer"})
	s, ev := e.open(t, false)
	_, err := s.Submit("delegate")
	require.NoError(t, err)
	assert.Equal(t, "done", ev.finished().Answer)

	parent, child := parentRequest(t, e, 0), requestWith(t, e, "CHILD-P")
	assert.Equal(t, parent.System, child.System, "the child's system prompt is its parent's")
	assert.NotContains(t, child.System, instructions.SubagentNote)
	assert.Equal(t, []string{"CHILD-P task\n\n" + instructions.SubagentNote}, child.UserTexts)
}
