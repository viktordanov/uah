package embedded

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/viktordanov/uah/internal/approval"
	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/mcp"
)

func TestScope_Tools(t *testing.T) {
	var none *scope
	assert.True(t, none.offers("Bash"), "no scope offers every tool")
	assert.Equal(t, []string{"x"}, none.disallow([]string{"x"}))

	s := newScope(engine.Scope{Tools: []string{"Bash", "mcp__docs", "mcp__gh__*", "mcp__db__query"}})
	assert.Equal(t, []string{"ViewImage", "SkillUse", "apply_patch", "request_user_input", "wait_shell", "kill_shell"}, s.disallow(nil))
	tools := []mcp.Tool{{Name: "mcp__docs__search"}, {Name: "mcp__gh__issue"}, {Name: "mcp__db__query"}, {Name: "mcp__db__drop"}, {Name: "mcp__docsx__a"}}
	var names []string
	for _, tl := range s.mcpTools(tools) {
		names = append(names, tl.Name)
	}
	assert.Equal(t, []string{"mcp__docs__search", "mcp__gh__issue", "mcp__db__query"}, names)
	assert.Len(t, tools, 5, "the engine's list is not changed")

	assert.Empty(t, newScope(engine.Scope{Tools: []string{}}).mcpTools(tools), "an empty list offers nothing")
}

func TestScope_Approves(t *testing.T) {
	s := newScope(engine.Scope{Approve: []string{"git diff", "apply_patch", "mcp__gh__get_issue", "rm -rf *"}})
	for cmd, want := range map[string]bool{
		"git diff --stat":           true,
		"git diff && git diff HEAD": true,
		"git diff && rm x":          false,
		"git push":                  false,
		"git diff $(rm x)":          false,
		"apply_patch /tmp/a.go":     true,
	} {
		assert.Equal(t, want, s.approves(approval.Prompt{Command: cmd}), cmd)
	}
	assert.False(t, s.approves(approval.Prompt{Command: "mcp__gh__get_issue {}", MCPTool: "mcp__gh__get_issue"}), "the MCP gate applies MCP approvals")
	assert.True(t, s.approvesTool("mcp__gh__get_issue"))
	assert.False(t, s.approvesTool("mcp__gh__close_issue"))
}

func TestScope_Ask(t *testing.T) {
	s := newScope(engine.Scope{Approve: []string{"curl"}})
	mode := approval.ModeWorkspace
	asked := 0
	next := func(context.Context, approval.Prompt) approval.Answer { asked++; return approval.Decline }
	ask := s.ask(next, func() approval.Mode { return mode })

	assert.Equal(t, approval.Approve, ask(context.Background(), approval.Prompt{Command: "curl example.com", Escalation: true}))
	assert.Equal(t, approval.Decline, ask(context.Background(), approval.Prompt{Command: "wget example.com", Escalation: true}))
	mode = approval.ModeReadOnly
	assert.Equal(t, approval.Decline, ask(context.Background(), approval.Prompt{Command: "curl example.com", Escalation: true}), "read only stays read only")
	assert.Equal(t, approval.Approve, ask(context.Background(), approval.Prompt{Command: "curl example.com"}), "a prompt rule's command runs in the read-only sandbox")
	assert.Equal(t, 2, asked)

	headless := s.ask(nil, func() approval.Mode { return approval.ModeWorkspace })
	reason, declined := headless(context.Background(), approval.Prompt{Command: "wget x"}).DeclineReason()
	assert.True(t, declined)
	assert.Contains(t, reason, "headless")
}
