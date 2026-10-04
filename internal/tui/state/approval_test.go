package state_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/approval"
	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/internal/tui/state"
)

func TestApproval(t *testing.T) {
	opened := session.SessionOpened{At: t0, ID: "s1", Settings: settings()}
	asked := session.ApprovalRequested{At: t0, ID: "a1", Command: "curl x", Escalation: true, ProposedPrefix: []string{"curl", "x"}}
	s, _ := apply(state.New(t0), opened, asked, session.ApprovalRequested{At: t0, ID: "a2", Command: "git push"})

	a, ok := s.PendingApproval()
	require.True(t, ok)
	assert.Equal(t, "a1", a.ID, "the first request shows first")

	s, effects := apply(s, state.Answer{Answer: approval.ApprovePrefix}, state.Answer{Answer: approval.Decline})
	assert.Equal(t, []state.Effect{state.EffResolve{ID: "a1", Answer: approval.ApprovePrefix}}, effects, "one answer per approval")

	s, _ = apply(s, session.ApprovalResolved{At: t0, ID: "a1", Decision: approval.ApprovePrefix})
	a, _ = s.PendingApproval()
	assert.Equal(t, "a2", a.ID)
	assert.Contains(t, s.Items[len(s.Items)-1].Text, "commands that start with `curl x`")

	s, effects = apply(s, state.Answer{Answer: approval.ApprovePrefix})
	assert.Empty(t, effects, "no prefix to allow")
	s, effects = apply(s, state.Answer{Answer: approval.Decline}, session.ApprovalResolved{At: t0, ID: "a2", Decision: approval.Decline})
	assert.Equal(t, []state.Effect{state.EffResolve{ID: "a2", Answer: approval.Decline}}, effects)
	_, ok = s.PendingApproval()
	assert.False(t, ok)
	assert.Equal(t, "✗ declined: git push", s.Items[len(s.Items)-1].Text)
}

// An MCP prompt offers "don't ask again for this tool"; a command prompt
// does not.
func TestApproval_MCPTool(t *testing.T) {
	opened := session.SessionOpened{At: t0, ID: "s1", Settings: settings()}
	s, _ := apply(state.New(t0), opened,
		session.ApprovalRequested{At: t0, ID: "a1", Command: "git push"},
		session.ApprovalRequested{At: t0, ID: "a2", Command: `mcp__docs__search {}`, MCPTool: "mcp__docs__search"})

	s, effects := apply(s, state.Answer{Answer: approval.ApproveTool})
	assert.Empty(t, effects, "not an MCP tool")
	s, _ = apply(s, state.Answer{Answer: approval.Approve}, session.ApprovalResolved{At: t0, ID: "a1", Decision: approval.Approve})

	s, effects = apply(s, state.Answer{Answer: approval.ApproveTool})
	assert.Equal(t, []state.Effect{state.EffResolve{ID: "a2", Answer: approval.ApproveTool}}, effects)
	s, _ = apply(s, session.ApprovalResolved{At: t0, ID: "a2", Decision: approval.ApproveTool})
	assert.Equal(t, "✔ approved, and from now on the tool mcp__docs__search: mcp__docs__search {}", s.Items[len(s.Items)-1].Text)
}

// A patch prompt with a directory to grant offers "allow writes to it for
// this session"; one without does not.
func TestApproval_GrantRoot(t *testing.T) {
	opened := session.SessionOpened{At: t0, ID: "s1", Settings: settings()}
	s, _ := apply(state.New(t0), opened,
		session.ApprovalRequested{At: t0, ID: "a1", Command: "apply_patch /x/a.txt"},
		session.ApprovalRequested{At: t0, ID: "a2", Command: "apply_patch /work/b/a.txt", GrantRoot: "/work/b"})

	s, effects := apply(s, state.Answer{Answer: approval.ApproveGrant})
	assert.Empty(t, effects, "no directory offered")
	s, _ = apply(s, state.Answer{Answer: approval.Approve}, session.ApprovalResolved{At: t0, ID: "a1", Decision: approval.Approve})

	a, _ := s.PendingApproval()
	assert.Equal(t, "/work/b", a.GrantRoot)
	s, effects = apply(s, state.Answer{Answer: approval.ApproveGrant})
	assert.Equal(t, []state.Effect{state.EffResolve{ID: "a2", Answer: approval.ApproveGrant}}, effects)
	s, _ = apply(s, session.ApprovalResolved{At: t0, ID: "a2", Decision: approval.ApproveGrant})
	assert.Equal(t, "✔ approved, and writes to /work/b for this session: apply_patch /work/b/a.txt", s.Items[len(s.Items)-1].Text)
}
