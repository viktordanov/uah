// Package toolpolicy decides which tools a session's model may use: an
// allowlist and a denylist of tool names, from the configuration files and
// the command line. Every source can only narrow what the others allow, so
// no layer, environment variable, hook, role, or subagent widens it.
package toolpolicy

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/viktordanov/uah-core/harness/tool"

	"github.com/viktordanov/uah/internal/goal"
	"github.com/viktordanov/uah/internal/mcp"
	"github.com/viktordanov/uah/internal/patch"
)

// Builtins are the names of uah's own tools, as the model sees them. A
// policy names these, MCP tools as mcp__<server>__<tool> with the server's
// exposed name and the tool's own raw name (AllowsMCP), or every tool of a
// server (mcp__<server>__*, or mcp__<server>).
var Builtins = []string{
	tool.BashName, tool.ViewImageName, tool.SkillUseName, patch.ToolName, webSearch, questionTool,
	spawnAgent, "send_input", "wait_agent", "close_agent", "resume_agent",
	goal.GetToolName, goal.CreateToolName, goal.UpdateToolName,
	mcp.ListResourcesTool, mcp.ListResourceTemplatesTool, mcp.ReadResourceTool,
}

// The names of the tools defined in packages that depend on this one.
const (
	webSearch    = "web_search"
	questionTool = "request_user_input"
	spawnAgent   = "spawn_agent"
)

// aliases are names other tools use for uah's, so a policy that names one
// says which name it means.
var aliases = map[string]string{
	"Edit": patch.ToolName, "Write": patch.ToolName, "MultiEdit": patch.ToolName, "NotebookEdit": patch.ToolName,
	"Skill": tool.SkillUseName, "Agent": spawnAgent, "Task": spawnAgent, "WebSearch": webSearch,
}

// Refused ends the error of a call the policy refused: the engine writes
// it, and the session knows by it that the call never ran.
const Refused = "the tool policy does not allow it"

// Policy is the tools a session may use. The zero Policy allows every
// tool, as uah does without one.
type Policy struct {
	// Allow are the tools allowed: nil allows every tool, and an empty,
	// non-nil list none.
	Allow []string `json:"allow"`
	// Deny are tools never allowed, even when Allow names them.
	Deny []string `json:"deny,omitempty"`
	// Within are the allowlists Allow was narrowed from (Narrow). A tool
	// must match each of them too: Allow alone, an intersection by name,
	// cannot keep a server pattern's server, so mcp__a__* narrowed with
	// mcp__a__b__c must still refuse that name from a server named a__b.
	Within [][]string `json:"within,omitempty"`
}

// Restricted reports whether the policy narrows anything: a session with
// one fails closed when a hook fails (internal/hooks).
func (p Policy) Restricted() bool { return p.Allow != nil || len(p.Deny) > 0 }

// Allows reports whether the policy allows a built-in tool, or an MCP tool
// known only by its exposed name.
func (p Policy) Allows(name string) bool { return p.AllowsTool(name, "") }

// AllowsTool reports whether the policy allows a tool by the name the model
// sees; server is an MCP tool's server ("" for a built-in tool, or when it
// is not known), so a server pattern matches that server's tools. The
// engine authorizes MCP tools with AllowsMCP, by identity, instead.
func (p Policy) AllowsTool(name, server string) bool {
	return p.allows(name, server, true)
}

// AllowsMCP reports whether the policy allows an MCP tool by its identity:
// the configured server and the server's own name for the tool, never the
// name the model sees, which mcp's namer derives and can hand to another
// tool as the tool lists change. A server pattern (mcp__<server>__*)
// matches the server's exposed name; an entry mcp__<server>__<tool>
// matches that server's tool whose raw name is <tool>, so it allows
// nothing while no such tool exists. A denylist entry also matches the
// tool's sanitized name, so it refuses more, never less.
func (p Policy) AllowsMCP(server, tool string) bool {
	for _, list := range append([][]string{p.Allow}, p.Within...) {
		if list != nil && !matchMCP(list, server, tool, false) {
			return false
		}
	}

	return !matchMCP(p.Deny, server, tool, true)
}

// matchMCP reports whether an entry of the list names the tool, by its
// server and raw name, or with broad, by its sanitized name too.
func matchMCP(list []string, server, tool string, broad bool) bool {
	s := mcp.Sanitize(server)
	for _, n := range list {
		if p, ok := serverPattern(n); ok {
			if p == s {
				return true
			}

			continue
		}
		es, et, ok := mcpEntry(n)
		if ok && es == s && (et == tool || broad && et == mcp.Sanitize(tool)) {
			return true
		}
	}

	return false
}

// mcpEntry splits mcp__<server>__<tool> at the first "__" after the
// prefix: a server's exposed name never has "__" in a name a policy can use.
func mcpEntry(n string) (server, tool string, ok bool) {
	rest, ok := strings.CutPrefix(n, mcp.Prefix)
	if !ok {
		return "", "", false
	}
	server, tool, ok = strings.Cut(rest, "__")

	return server, tool, ok && server != "" && tool != "" && tool != "*"
}

func (p Policy) allows(name, server string, exact bool) bool {
	for _, list := range append([][]string{p.Allow}, p.Within...) {
		if list != nil && !match(list, name, server, exact) {
			return false
		}
	}

	return !Match(p.Deny, name, server)
}

// AllowsServer reports whether the policy allows an MCP server as a whole:
// its allowlist, if any, has the server's pattern, and its denylist does
// not. The MCP resource tools reach only such servers under a restriction.
func (p Policy) AllowsServer(server string) bool {
	if !p.Restricted() {
		return true
	}
	pattern := mcp.Prefix + mcp.Sanitize(server) + "__*"
	for _, list := range append([][]string{p.Allow}, p.Within...) {
		if list != nil && !covers(list, pattern) {
			return false
		}
	}

	return !covers(p.Deny, pattern)
}

// Narrow returns the policy that allows only what both p and q allow: the
// allowlists intersected and the denylists joined. Neither is changed.
func (p Policy) Narrow(q Policy) Policy {
	out := Policy{Allow: Intersect(p.Allow, q.Allow), Deny: Union(p.Deny, q.Deny)}
	for _, x := range []Policy{p, q} {
		for _, list := range append(slices.Clone(x.Within), x.Allow) {
			if list != nil && !slices.ContainsFunc(out.Within, func(l []string) bool { return slices.Equal(l, list) }) {
				out.Within = append(out.Within, list)
			}
		}
	}
	if len(out.Within) == 1 && slices.Equal(out.Within[0], out.Allow) {
		out.Within = nil // one list: Allow says it all
	}

	return out
}

// String describes the policy for a notice: "Bash, mcp__docs__*; not
// Bash" or "no tools".
func (p Policy) String() string {
	var parts []string
	switch {
	case p.Allow == nil:
		parts = append(parts, "every tool")
	case len(p.Allow) == 0:
		parts = append(parts, "no tools")
	default:
		parts = append(parts, strings.Join(p.Allow, ", "))
	}
	if len(p.Deny) > 0 {
		parts = append(parts, "not "+strings.Join(p.Deny, ", "))
	}

	return strings.Join(parts, "; ")
}

// Match reports whether a tool is in the list: by its name, or, for an MCP
// tool, by its server's pattern. With server set, a pattern matches when it
// names that server as the tool's exposed name does (mcp.Sanitize), so
// mcp__a__* never matches a server named a__b; without it, by the name's
// prefix.
func Match(list []string, name, server string) bool { return match(list, name, server, true) }

// match is Match, where an exact name counts only when exact is set.
func match(list []string, name, server string, exact bool) bool {
	for _, n := range list {
		if n == name && (exact || !strings.HasPrefix(name, mcp.Prefix)) {
			return true
		}
		s, ok := serverPattern(n)
		if !ok || !strings.HasPrefix(name, mcp.Prefix) {
			continue
		}
		if server != "" {
			if mcp.Sanitize(server) == s {
				return true
			}

			continue
		}
		if strings.HasPrefix(name, mcp.Prefix+s+"__") {
			return true
		}
	}

	return false
}

// serverPattern returns the server a pattern names: S for mcp__S__* or
// mcp__S, where S has no "__".
func serverPattern(n string) (string, bool) {
	rest, ok := strings.CutPrefix(n, mcp.Prefix)
	if !ok {
		return "", false
	}
	if s, ok := strings.CutSuffix(rest, "__*"); ok {
		return s, s != "" && !strings.Contains(s, "__")
	}

	return rest, rest != "" && !strings.Contains(rest, "__")
}

// Intersect returns the names both allowlists allow, nil meaning every
// tool: the entries of each that the other covers. A server pattern
// covers the server's tools and the same pattern, so ["mcp__a__*"] and
// ["mcp__a__x", "Bash"] intersect to ["mcp__a__x"].
func Intersect(a, b []string) []string {
	switch {
	case a == nil:
		return slices.Clone(b)
	case b == nil:
		return slices.Clone(a)
	}
	out := []string{}
	add := func(n string) {
		if !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	for _, pair := range [][2][]string{{a, b}, {b, a}} {
		for _, n := range pair[0] {
			if covers(pair[1], n) {
				add(n)
			}
		}
	}

	return out
}

// covers reports whether the list allows every tool the entry names.
func covers(list []string, entry string) bool {
	s, ok := serverPattern(entry)
	if !ok {
		return Match(list, entry, "")
	}

	return slices.ContainsFunc(list, func(n string) bool {
		t, ok := serverPattern(n)

		return ok && t == s
	})
}

// Union returns the names in either list, each once.
func Union(a, b []string) []string {
	out := slices.Clone(a)
	for _, n := range b {
		if !slices.Contains(out, n) {
			out = append(out, n)
		}
	}

	return out
}

// Validate checks that every name is a built-in tool, an MCP tool's exposed
// name, or a server pattern, so a typo, which would deny nothing, is an
// error.
func Validate(names []string) error {
	var errs []error
	for _, n := range names {
		if err := validName(n); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}

func validName(n string) error {
	if slices.Contains(Builtins, n) {
		return nil
	}
	if uah, ok := aliases[n]; ok {
		return fmt.Errorf("unknown tool %q (uah calls it %s)", n, uah)
	}
	rest, ok := strings.CutPrefix(n, mcp.Prefix)
	if !ok {
		return fmt.Errorf("unknown tool %q (want one of %s, mcp__<server>__<tool>, or mcp__<server>__*)", n, strings.Join(Builtins, ", "))
	}
	server, tool, _ := strings.Cut(rest, "__")
	if server == "" || mcp.Sanitize(server) != server || strings.HasSuffix(rest, "__") ||
		strings.Contains(tool, "*") && tool != "*" || strings.ContainsFunc(tool, unicode.IsSpace) {
		return fmt.Errorf("invalid MCP tool name %q (want mcp__<server>__<tool> with the server's name as /mcp shows it and the tool's own name, or mcp__<server>__*)", n)
	}

	return nil
}

// Parse reads a comma-separated list from the command line: "" is the
// empty list, which allows no tools.
func Parse(s string) []string {
	out := []string{}
	for n := range strings.SplitSeq(s, ",") {
		if n = strings.TrimSpace(n); n != "" && !slices.Contains(out, n) {
			out = append(out, n)
		}
	}

	return out
}
