package agents_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/agents"
	"github.com/viktordanov/uah/internal/hooks"
	"github.com/viktordanov/uah/testing/fakellm"
)

// TestAgents_SubagentStopHook runs SubagentStop when the child finishes:
// the first time the hook blocks, and its reason goes to the child as its
// next message; the second time it lets the child finish. The child's tool
// calls run PostToolUse hooks too.
func TestAgents_SubagentStopHook(t *testing.T) {
	dir := t.TempDir()
	stops, tools := filepath.Join(dir, "stops.jsonl"), filepath.Join(dir, "tools.txt")
	script := filepath.Join(dir, "stop.sh")
	require.NoError(t, os.WriteFile(script, []byte(`#!/bin/sh
input=$(cat)
printf '%s\n' "$input" >> "`+stops+`"
case "$input" in
*'"stop_hook_active":true'*) exit 0 ;;
esac
echo "also list the tests" >&2
exit 2
`), 0o700))
	runner, err := hooks.New([]hooks.Hook{
		{Event: hooks.SubagentStop, Command: script, Source: hooks.SourceUser},
		{Event: hooks.PostToolUse, Matcher: "Bash", Command: "cat >/dev/null; echo Bash >> " + tools, Source: hooks.SourceUser},
	}, nil, "")
	require.NoError(t, err)
	e := newEnv(t, agents.Config{},
		fakellm.Reply{Calls: []fakellm.Call{call("spawn_agent", `{"message":"CHILD-S review"}`)}},
		callWith("wait_agent", `{"targets":["ID"]}`),
		fakellm.Reply{Text: "done"},
	)
	e.hooks = runner
	e.llm.Route("CHILD-S", fakellm.Reply{Commands: []string{"echo looked"}}, fakellm.Reply{Text: "first answer"}, fakellm.Reply{Text: "answer with tests"})
	s, ev := e.open(t, false)

	_, err = s.Submit("delegate")
	require.NoError(t, err)
	ev.finished()

	assert.Contains(t, answers(e), `{"completed":"answer with tests"}`, "wait_agent returned after the hook let the child finish")
	var last fakellm.Request
	for _, r := range e.llm.Requests() {
		if isChild(r) {
			last = r
		}
	}
	assert.Equal(t, "also list the tests", last.UserTexts[len(last.UserTexts)-1])

	data, err := os.ReadFile(stops)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	require.Len(t, lines, 2)
	var first hooks.Input
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &first))
	childID := ids(lastParent(e))[0]
	assert.Equal(t, hooks.SubagentStop, first.Event)
	assert.Equal(t, s.ID(), first.SessionID)
	assert.Equal(t, childID, first.AgentID)
	assert.Equal(t, "default", first.AgentType)
	assert.Equal(t, "first answer", first.LastAssistantMessage)
	assert.False(t, first.StopHookActive)
	assert.Equal(t, filepath.Join(e.sessionsDir(), childID+".session.jsonl"), first.AgentTranscriptPath)
	assert.Contains(t, lines[1], `"stop_hook_active":true`)

	ran, err := os.ReadFile(tools)
	require.NoError(t, err)
	assert.Equal(t, "Bash\n", string(ran), "the child's Bash call ran the PostToolUse hook")
}
