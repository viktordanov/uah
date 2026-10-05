package agents_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/agents"
	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/testing/fakellm"
)

// TestAgents_ChildrenDoNotStream: a parent that streams its own answer
// gets none of a child's text as deltas, and the child's own session
// streams nothing either, though it opens from the parent's options.
func TestAgents_ChildrenDoNotStream(t *testing.T) {
	e := newEnv(t, agents.Config{},
		fakellm.Reply{Calls: []fakellm.Call{call("spawn_agent", `{"message":"CHILD-S answer slowly"}`)}},
		callWith("wait_agent", `{"targets":["ID"]}`),
		fakellm.Reply{Deltas: []string{"parent ", "answer"}},
	)
	e.llm.Route("CHILD-S", fakellm.Reply{Deltas: []string{"child ", "answer"}})
	e.stream = true
	s, ev := e.open(t, false)
	_, err := s.Submit("delegate")
	require.NoError(t, err)
	assert.Equal(t, "parent answer", ev.finished().Answer)
	assert.Contains(t, answers(e), `{"completed":"child answer"}`)

	var streamed string
	for _, x := range ev.all {
		if d, ok := x.(engine.TextDelta); ok {
			streamed += d.Text
		}
	}
	assert.Equal(t, "parent answer", streamed)

	w, err := s.WatchAgent("ada")
	require.NoError(t, err)
	defer w.Stop()
	answered := false
	for _, x := range w.Events {
		switch x.(type) {
		case engine.TextDelta, engine.ReasoningDelta:
			t.Errorf("the child streamed: %#v", x)
		}
		if m, ok := x.(core.AssistantMessage); ok && m.Text == "child answer" {
			answered = true
		}
	}
	assert.True(t, answered, "the child answered")
}
