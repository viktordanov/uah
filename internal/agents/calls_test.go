package agents_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/agents"
	"github.com/viktordanov/uah/internal/engine"
)

// TestCall_Errors checks the tools' argument errors and unknown agents,
// with Codex's messages.
func TestCall_Errors(t *testing.T) {
	m := agents.New(agents.Config{MaxDepth: 1})
	for _, tc := range []struct{ tool, args, want string }{
		{"spawn_agent", `{"message":"  "}`, "empty message can't be sent to an agent"},
		{"spawn_agent", `{"message":"hi"}`, "subagents are not available in this session"},
		{"spawn_agent", `{"message":"hi","fork_context":true,"agent_type":"reviewer"}`, "Full-history forked agents inherit the parent agent type; omit agent_type, or spawn without a full-history fork."},
		{"send_input", `{"target":"x","message":"hi"}`, "agent with id x not found"},
		{"close_agent", `{"target":"x"}`, "agent with id x not found"},
		{"resume_agent", `{"id":"x"}`, "agent with id x not found"},
		{"wait_agent", `{"targets":[]}`, "agent ids must be non-empty"},
		{"wait_agent", `{"targets":["x"],"timeout_ms":0}`, "timeout_ms must be greater than zero"},
		{"wait_agent", `{"targets":"x"}`, "invalid arguments"},
		{"wait", `{"ids":["x"]}`, `unknown agent tool "wait"`},
	} {
		_, err := m.Call(t.Context(), engine.AgentCall{ParentID: "p", Tool: tc.tool, Args: json.RawMessage(tc.args)})
		require.Error(t, err, tc.tool+" "+tc.args)
		assert.Contains(t, err.Error(), tc.want, tc.tool+" "+tc.args)
	}
}

// TestCall_WaitUnknownID reports an unknown agent as not_found at once.
func TestCall_WaitUnknownID(t *testing.T) {
	m := agents.New(agents.Config{})
	out, err := m.Call(t.Context(), engine.AgentCall{ParentID: "p", Tool: "wait_agent", Args: json.RawMessage(`{"targets":["missing"]}`)})
	require.NoError(t, err)
	assert.JSONEq(t, `{"status":{"missing":"not_found"},"timed_out":false}`, out)
}

// TestCall_WaitTimedOutSaysTheAgentsAreFine: a wait that times out tells
// the parent the agents are still working and to wait again, and a wait
// that returns a status adds nothing.
func TestCall_WaitTimedOutSaysTheAgentsAreFine(t *testing.T) {
	var out struct {
		TimedOut bool   `json:"timed_out"`
		Note     string `json:"note"`
	}
	require.NoError(t, json.Unmarshal([]byte(agents.WaitResult(map[string]agents.Status{}, true)), &out))
	assert.True(t, out.TimedOut)
	assert.Contains(t, out.Note, "still working, and nothing is wrong")
	assert.Contains(t, out.Note, "call wait_agent again")
	assert.JSONEq(t, `{"status":{"a":"not_found"},"timed_out":false}`, agents.WaitResult(map[string]agents.Status{"a": {State: "not_found"}}, false))
}

// TestTools_Offered offers Codex's v1 tools while the depth allows it, and
// resolves the name uah used before.
func TestTools_Offered(t *testing.T) {
	m := agents.New(agents.Config{MaxDepth: 1, Roles: []agents.Role{{Name: "reviewer", Description: "Reviews diffs."}}})
	var names []string
	for _, tl := range m.Attach(engine.AgentParent{SessionID: "p"}) {
		names = append(names, tl.Name)
		assert.Equal(t, "object", tl.Parameters["type"])
		if tl.Name == "spawn_agent" {
			assert.Contains(t, tl.Description, "- `reviewer`: Reviews diffs.")
		}
	}
	assert.Equal(t, []string{"spawn_agent", "send_input", "wait_agent", "close_agent", "resume_agent"}, names)
	assert.Contains(t, m.ToolNames(), "wait")

	off := agents.New(agents.Config{MaxDepth: 0})
	assert.Empty(t, off.Attach(engine.AgentParent{SessionID: "p"}), "max_depth 0 offers nothing")
	_, err := off.Call(t.Context(), engine.AgentCall{ParentID: "p", Tool: "spawn_agent", Args: json.RawMessage(`{"message":"hi"}`)})
	require.EqualError(t, err, "Agent depth limit reached. Solve the task yourself.", "a call past the depth is refused with Codex's words")
	_, err = off.Call(t.Context(), engine.AgentCall{ParentID: "p", Tool: "resume_agent", Args: json.RawMessage(`{"id":"x"}`)})
	require.EqualError(t, err, "Agent depth limit reached. Solve the task yourself.")
}

// TestStatus_JSON encodes statuses as Codex's AgentStatus.
func TestStatus_JSON(t *testing.T) {
	for _, tc := range []struct {
		status agents.Status
		want   string
	}{
		{agents.Status{State: engine.AgentRunning}, `"running"`},
		{agents.Status{State: engine.AgentPendingInit}, `"pending_init"`},
		{agents.Status{State: engine.AgentInterrupted}, `"interrupted"`},
		{agents.Status{State: engine.AgentShutdown}, `"shutdown"`},
		{agents.Status{State: engine.AgentNotFound}, `"not_found"`},
		{agents.Status{State: engine.AgentCompleted, Message: "done"}, `{"completed":"done"}`},
		{agents.Status{State: engine.AgentCompleted}, `{"completed":null}`},
		{agents.Status{State: engine.AgentErrored, Message: "boom"}, `{"errored":"boom"}`},
	} {
		data, err := json.Marshal(tc.status)
		require.NoError(t, err)
		assert.JSONEq(t, tc.want, string(data))
	}
	assert.False(t, agents.Status{State: engine.AgentPendingInit}.Final())
	assert.True(t, agents.Status{State: engine.AgentInterrupted}.Final())
}

// TestCall_UnknownModel refuses a model the provider does not offer at
// spawn_agent, and the configured default model too.
func TestCall_UnknownModel(t *testing.T) {
	known := func(_ context.Context, model string) error {
		if model == "gpt-6-luna" {
			return nil
		}

		return errors.New("Unknown model `" + model + "` for spawn_agent")
	}
	m := agents.New(agents.Config{MaxDepth: 1, Validate: known})
	_, err := m.Call(t.Context(), engine.AgentCall{ParentID: "p", Tool: "spawn_agent", Args: json.RawMessage(`{"message":"hi","model":"gpt-luna-6"}`)})
	require.EqualError(t, err, "Unknown model `gpt-luna-6` for spawn_agent")
	_, err = m.Call(t.Context(), engine.AgentCall{ParentID: "p", Tool: "spawn_agent", Args: json.RawMessage(`{"message":"hi","model":"gpt-6-luna"}`)})
	require.EqualError(t, err, "subagents are not available in this session", "a known model passes the check")

	withDefault := agents.New(agents.Config{MaxDepth: 1, Validate: known, Model: "gpt-typo"})
	_, err = withDefault.Call(t.Context(), engine.AgentCall{ParentID: "p", Tool: "spawn_agent", Args: json.RawMessage(`{"message":"hi"}`)})
	require.ErrorContains(t, err, "Unknown model `gpt-typo`")

	anyModel := agents.New(agents.Config{MaxDepth: 1})
	_, err = anyModel.Call(t.Context(), engine.AgentCall{ParentID: "p", Tool: "spawn_agent", Args: json.RawMessage(`{"message":"hi","model":"anything"}`)})
	require.EqualError(t, err, "subagents are not available in this session", "without a check any model passes")
}
