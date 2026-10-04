package session

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/toolpolicy"
)

// TestRefused: a call the tool policy refused fires no PostToolUse hook,
// known by its name or, for an MCP tool refused by its server, by the
// engine's refusal; any other call does.
func TestRefused(t *testing.T) {
	p := toolpolicy.Policy{Allow: []string{"Bash", "mcp__a_b__*"}}
	assert.False(t, refused(p, "Bash", core.ToolFinished{OK: true}))
	assert.True(t, refused(p, "apply_patch", core.ToolFinished{}))
	assert.True(t, refused(p, "mcp__a_b__delete", core.ToolFinished{Detail: `tool "mcp__a_b__delete" is not available in this session: ` + toolpolicy.Refused}))
	assert.False(t, refused(p, "mcp__a_b__read", core.ToolFinished{Detail: "it failed"}))
	assert.False(t, refused(toolpolicy.Policy{}, "made_up", core.ToolFinished{Detail: `tool "made_up" is not available`}), "no policy: as before")
}
