package embedded

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/viktordanov/uah/internal/mcp"
	"github.com/viktordanov/uah/internal/toolpolicy"
)

// TestMCPPolicy_SharedServerNames: two servers whose exposed names are the
// same (a-b and a.b are both a_b) cannot be told apart by a pattern, so a
// pattern allows neither, an exact name only its own tool, and the
// resource tools reach neither. A server with a name of its own is
// matched by its pattern, and never by another server's.
func TestMCPPolicy_SharedServerNames(t *testing.T) {
	tools := []mcp.Tool{
		{Name: "mcp__a_b__read", Server: "a-b", Tool: "read"},
		{Name: "mcp__a_b__delete", Server: "a.b", Tool: "delete"},
		{Name: "mcp__docs__search", Server: "docs", Tool: "search"},
		{Name: "mcp__docs__x__y", Server: "docs__x", Tool: "y"},
	}
	servers := []string{"a-b", "a.b", "docs", "docs__x"}
	names := func(p toolpolicy.Policy) []string {
		var out []string
		for _, tl := range newMCPPolicy(p, servers).tools(tools) {
			out = append(out, tl.Name)
		}

		return out
	}

	assert.Equal(t, []string{"mcp__docs__search"}, names(toolpolicy.Policy{Allow: []string{"mcp__a_b__*", "mcp__docs__*"}}))
	assert.Empty(t, names(toolpolicy.Policy{Allow: []string{"mcp__a_b__read"}}), "nor does an exact name: it can pass to the other server")
	assert.Len(t, names(toolpolicy.Policy{}), 4, "no policy: every tool")
	m := newMCPPolicy(toolpolicy.Policy{Allow: []string{"mcp__a_b__*", "mcp__docs__*"}}, servers)
	assert.False(t, m.allowsServer("a-b"))
	assert.False(t, m.allowsServer("a.b"))
	assert.True(t, m.allowsServer("docs"))
	assert.False(t, m.allowsServer("docs__x"))

	r := withPolicy(nil, m, tools).(policyRegistry)
	assert.False(t, r.allows("mcp__a_b__delete"))
	assert.False(t, r.allows("mcp__docs__x__y"))
	assert.True(t, r.allows("mcp__docs__search"))
}
