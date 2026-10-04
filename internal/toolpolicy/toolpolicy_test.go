package toolpolicy_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/toolpolicy"
)

func TestPolicy_Allows(t *testing.T) {
	t.Parallel()
	var unset toolpolicy.Policy
	assert.False(t, unset.Restricted())
	assert.True(t, unset.Allows("Bash"), "no policy allows every tool")
	assert.True(t, unset.AllowsTool("mcp__gh__close", "gh"))

	none := toolpolicy.Policy{Allow: []string{}}
	assert.True(t, none.Restricted(), "an empty allowlist is a policy")
	for _, name := range toolpolicy.Builtins {
		assert.False(t, none.Allows(name), name)
	}

	p := toolpolicy.Policy{Allow: []string{"Bash", "mcp__docs__*", "mcp__gh", "mcp__db__query"}, Deny: []string{"mcp__docs__write"}}
	for _, c := range []struct {
		name, server string
		want         bool
	}{
		{"Bash", "", true},
		{"apply_patch", "", false},
		{"mcp__docs__search", "docs", true},
		{"mcp__docs__write", "docs", false},   // the denylist wins
		{"mcp__gh__issue", "gh", true},        // mcp__<server> is the server's pattern
		{"mcp__db__query", "db", true},        // one tool
		{"mcp__db__drop", "db", false},        // and not its neighbor
		{"mcp__docs__x__y", "docs__x", false}, // another server whose name starts the same
		{"mcp__docsx__a", "docsx", false},
		{"mcp__docs_v2__a", "docs.v2", false},
	} {
		assert.Equal(t, c.want, p.AllowsTool(c.name, c.server), c.name)
	}
	assert.True(t, p.AllowsServer("docs"))
	assert.False(t, p.AllowsServer("db"), "one tool of a server is not the server")
	assert.False(t, toolpolicy.Policy{Deny: []string{"mcp__docs__*"}}.AllowsServer("docs"))
	assert.True(t, toolpolicy.Policy{Deny: []string{"mcp__docs__write"}}.AllowsServer("docs"))
	assert.True(t, toolpolicy.Policy{Allow: []string{"mcp__a.b__*"}}.Restricted())
	assert.True(t, toolpolicy.Policy{Allow: []string{"mcp__a_b__*"}}.AllowsServer("a.b"), "a server's pattern uses its exposed name")
}

func TestPolicy_Narrow(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name       string
		a, b, want []string
	}{
		{"unset and unset", nil, nil, nil},
		{"unset narrows to the other", nil, []string{"Bash"}, []string{"Bash"}},
		{"the other way", []string{"Bash"}, nil, []string{"Bash"}},
		{"empty stays empty", []string{}, nil, []string{}},
		{"empty wins", []string{"Bash"}, []string{}, []string{}},
		{"names", []string{"Bash", "ViewImage"}, []string{"ViewImage", "apply_patch"}, []string{"ViewImage"}},
		{"a server's tool", []string{"mcp__a__*", "Bash"}, []string{"mcp__a__x", "mcp__b__y"}, []string{"mcp__a__x"}},
		{"the same server", []string{"mcp__a__*"}, []string{"mcp__a"}, []string{"mcp__a__*", "mcp__a"}},
		{"nothing shared", []string{"Bash"}, []string{"apply_patch"}, []string{}},
	} {
		got := toolpolicy.Policy{Allow: c.a}.Narrow(toolpolicy.Policy{Allow: c.b}).Allow
		assert.Equal(t, c.want, got, c.name)
		assert.Equal(t, c.want == nil, got == nil, c.name+": nil is every tool, empty none")
	}
	p := toolpolicy.Policy{Deny: []string{"Bash"}}.Narrow(toolpolicy.Policy{Deny: []string{"apply_patch", "Bash"}})
	assert.Equal(t, []string{"Bash", "apply_patch"}, p.Deny, "denylists add up")

	// Narrowing never allows what either side does not.
	a := toolpolicy.Policy{Allow: []string{"mcp__a__*", "Bash", "web_search"}, Deny: []string{"mcp__a__drop"}}
	b := toolpolicy.Policy{Allow: []string{"mcp__a__read", "mcp__a__drop", "mcp__b__*", "web_search"}, Deny: []string{"web_search"}}
	both := a.Narrow(b)
	for _, tool := range [][2]string{{"Bash", ""}, {"web_search", ""}, {"mcp__a__read", "a"}, {"mcp__a__drop", "a"}, {"mcp__b__x", "b"}, {"mcp__a__x", "a"}} {
		assert.Equal(t, a.AllowsTool(tool[0], tool[1]) && b.AllowsTool(tool[0], tool[1]), both.AllowsTool(tool[0], tool[1]), tool[0])
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()
	require.NoError(t, toolpolicy.Validate(append([]string{"mcp__docs__search", "mcp__docs__*", "mcp__docs", "mcp__a_b__c__d"}, toolpolicy.Builtins...)))
	for name, want := range map[string]string{
		"bash":        "unknown tool",
		"Edit":        "uah calls it apply_patch",
		"Agent":       "uah calls it spawn_agent",
		"*":           "unknown tool",
		"mcp__":       "invalid MCP tool name",
		"mcp__*":      "invalid MCP tool name",
		"mcp__a__b*":  "invalid MCP tool name",
		"mcp__a.b__c": "invalid MCP tool name",
		"mcp____x":    "invalid MCP tool name",
		"mcp__a__":    "invalid MCP tool name",
	} {
		err := toolpolicy.Validate([]string{name})
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), want, name)
	}
}

func TestParse(t *testing.T) {
	t.Parallel()
	assert.Equal(t, []string{}, toolpolicy.Parse(""), "an empty list allows nothing")
	assert.Equal(t, []string{"Bash", "mcp__a__*"}, toolpolicy.Parse(" Bash, mcp__a__*,,Bash "))
}
