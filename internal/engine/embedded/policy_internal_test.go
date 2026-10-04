package embedded

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/mcp"
	"github.com/viktordanov/uah/internal/toolpolicy"
)

// policyResult is one run's MCP tools as the manager names them, and what the
// policy offers of them.
type policyResult struct {
	offered []string
	warned  []string
	r       policyRegistry
}

func policyRun(t *testing.T, p toolpolicy.Policy, servers []string, raw [][2]string) policyResult {
	t.Helper()
	var tools []mcp.Tool
	for _, x := range raw {
		tools = append(tools, mcp.Tool{Server: x[0], Tool: x[1]})
	}
	tools = mcp.Qualify(tools)
	var out policyResult
	mp := newMCPPolicy(p, servers, func(msg string) { out.warned = append(out.warned, msg) })
	allowed := mp.tools(tools)
	for _, tl := range allowed {
		out.offered = append(out.offered, tl.Server+"/"+tl.Tool)
	}
	out.r = withPolicy(nil, p, allowed, tools).(policyRegistry)

	return out
}

// TestMCPPolicy_Identity: under a policy, an MCP tool is allowed by its
// server and its own name, never by the name the model sees, and no tool
// whose exposed name is not unambiguously its own is offered.
func TestMCPPolicy_Identity(t *testing.T) {
	allowAll := toolpolicy.Policy{Allow: []string{"mcp__docs__*", "mcp__a_b__*", "mcp__a__*", "mcp__long__*"}}

	t.Run("servers that share an exposed name, or whose name has __", func(t *testing.T) {
		got := policyRun(t, allowAll, []string{"a-b", "a.b", "a__x", "docs"},
			[][2]string{{"a-b", "read"}, {"a.b", "delete"}, {"a__x", "y"}, {"docs", "search"}})
		assert.Equal(t, []string{"docs/search"}, got.offered)
		assert.Len(t, got.warned, 2, "each left-out tool the policy names is reported; no entry can name a server with __")
		mp := newMCPPolicy(allowAll, []string{"a-b", "a.b", "a__x", "docs"}, nil)
		assert.False(t, mp.allowsServer("a-b"))
		assert.False(t, mp.allowsServer("a__x"))
		assert.True(t, mp.allowsServer("docs"))
	})

	t.Run("two tools with the same exposed name: both left out", func(t *testing.T) {
		got := policyRun(t, toolpolicy.Policy{Allow: []string{"mcp__docs__read-file", "mcp__docs__read.file", "mcp__docs__list"}}, []string{"docs"},
			[][2]string{{"docs", "read-file"}, {"docs", "read.file"}, {"docs", "list"}})
		assert.Equal(t, []string{"docs/list"}, got.offered)
		assert.False(t, got.r.allows("mcp__docs__read_file"))
	})

	t.Run("a tools/list_changed rename: the entry stays with the raw tool", func(t *testing.T) {
		p := toolpolicy.Policy{Allow: []string{"mcp__docs__read-file"}}
		before := policyRun(t, p, []string{"docs"}, [][2]string{{"docs", "read-file"}})
		after := policyRun(t, p, []string{"docs"}, [][2]string{{"docs", "read.file"}})
		assert.Equal(t, []string{"docs/read-file"}, before.offered)
		assert.Empty(t, after.offered, "read.file now has the name read_file, but the policy named read-file")
		assert.False(t, after.r.allows("mcp__docs__read_file"))
		assert.Empty(t, policyRun(t, toolpolicy.Policy{Allow: []string{"mcp__docs__read_file"}}, []string{"docs"}, [][2]string{{"docs", "read-file"}}).offered,
			"the sanitized name is not the tool's own")
	})

	t.Run("a shortened name", func(t *testing.T) {
		long := "a_tool_name_long_enough_to_need_a_hash_suffix_in_the_exposed_name"
		got := policyRun(t, allowAll, []string{"long"}, [][2]string{{"long", long}})
		assert.Empty(t, got.offered)
		require.Len(t, got.warned, 1)
		assert.Contains(t, got.warned[0], "shortened")
	})

	t.Run("an entry for a tool that does not exist allows nothing", func(t *testing.T) {
		got := policyRun(t, toolpolicy.Policy{Allow: []string{"mcp__docs__ghost"}}, []string{"docs"}, [][2]string{{"docs", "search"}})
		assert.Empty(t, got.offered)
		assert.Empty(t, got.warned, "a tool the policy does not allow anyway is not reported")
		assert.False(t, got.r.allows("mcp__docs__ghost"))
		assert.False(t, got.r.allows("mcp__docs__search"))
	})

	t.Run("a deny entry refuses by the sanitized name too", func(t *testing.T) {
		got := policyRun(t, toolpolicy.Policy{Allow: []string{"mcp__docs__*"}, Deny: []string{"mcp__docs__read_file"}}, []string{"docs"},
			[][2]string{{"docs", "read-file"}, {"docs", "list"}})
		assert.Equal(t, []string{"docs/list"}, got.offered)
	})
}
