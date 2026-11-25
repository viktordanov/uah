package render_test

import (
	"encoding/json"
	"fmt"
	"image/color"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/internal/tui/render"
	"github.com/viktordanov/uah/internal/tui/state"
)

const ws = "/workspace/proj"

// call is one tool call of galleryTurn: its events, and what it left.
type call struct {
	id, name, args string
	ok             bool
	detail         string
	took           time.Duration
	output         *engine.ToolOutput
}

func (c call) events() []any {
	evs := []any{
		core.ToolCalled{At: t0, CallID: c.id, Name: c.name, Label: c.args, Arguments: c.args},
		core.ToolStarted{At: t0, CallID: c.id, OpID: c.id},
		core.ToolFinished{At: t0, CallID: c.id, OpID: c.id, OK: c.ok, Detail: c.detail, Duration: c.took},
	}
	if c.output != nil {
		o := *c.output
		o.CallID = c.id
		evs = append(evs, o)
	}

	return evs
}

func bash(id, command string, ok bool, detail string, took time.Duration, stderr string) call {
	args, _ := json.Marshal(map[string]string{"command": command})
	c := call{id: id, name: "Bash", args: string(args), ok: ok, detail: detail, took: took}
	if stderr != "" {
		c.output = &engine.ToolOutput{Output: stderr}
	}

	return c
}

// galleryTurn is the sample turn of the tool-call gallery (R6, spacing
// b): commands in their wrappers with absolute paths, two skills, failures
// with their errors, an auto-approved command, a listing, a search, and two
// MCP calls, one with a result and one that fails.
func galleryTurn(t *testing.T) state.State {
	t.Helper()
	fetch := "rtk proxy python3 perspective-research/fetch_ontology.py"
	calls := []call{
		bash("c1", "rtk proxy sh -c 'for p in /Users/AGENTS.md /home/me/AGENTS.md "+ws+"/AGENTS.md; do [ -f \"$p\" ] && cat \"$p\"; done'", true, "exit 0", 0, ""),
		bash("c2", "rtk proxy sh -c 'cat "+ws+"/README.md "+ws+"/api/README.md'", true, "exit 0", 0, ""),
		{id: "s1", name: "SkillUse", args: `{"name":"web-architecture-patterns"}`, ok: true, detail: "completed"},
		{id: "s2", name: "SkillUse", args: `{"name":"go-infrastructure-patterns"}`, ok: true, detail: "completed"},
		bash("c3", "rtk proxy sh -c 'git -C "+ws+" diff -- api/rest/server.go; diff "+ws+"/api/rest/server.go "+ws+"/api/rest/server.go.orig'", false, "exit 2", 0,
			"--- a\n+++ b\ndiff: api/rest/server.go.orig: No such file or directory\n"),
		bash("c4", "rtk proxy python3 - <<'PY'\nfrom pathlib import Path\nimport re,json\np=Path('"+ws+"/api/ontology/ontology.go')\nPY", false, "exit 127", 0,
			"env: python3 -: No such file or directory\n"),
	}
	later := []call{
		bash("c5", "rtk proxy sh -c 'sed -n \"1,360p\" "+ws+"/api/ontology/ontology.go; sed -n \"356,365p\" "+ws+"/api/ontology/loader.go'", true, "exit 0", 0, ""),
		bash("c7", fetch, true, "exit 0", 3*time.Second, ""),
		bash("c8", "rtk proxy sh -c 'rg --files "+ws+"/web/src -g \"*CI*\" -g \"*OUS*\" -g \"*Study*\"'", true, "exit 0", 0, ""),
		bash("c9", "rtk proxy sh -c 'rg -n \"RootData|ontologyVersion\" "+ws+"/api "+ws+"/web/src'", true, "exit 0", 0, ""),
		{
			id: "m1", name: "mcp__docs__search", args: `{"query":"ontology RootData version","limit":5}`, ok: true, detail: "completed",
			output: &engine.ToolOutput{Result: `{"results":[{"title":"RootData schema (v8)"}],"total":3}`, Size: 2300},
		},
		bash("c10", "rtk proxy sh -c 'cat "+ws+"/web/src/scopes/interview/externalDataAvailability.ts'", false, "exit 1", 0,
			"cat: web/src/scopes/interview/externalDataAvailability.ts: No such file or directory\n"),
		bash("c11", "rtk proxy sh -c 'cat "+ws+"/web/src/scopes/home/CreateStudyDialog.tsx | head -160; cat "+ws+"/web/src/scopes/home/useStudies.ts'", true, "exit 0", 0, ""),
		bash("c12", "rg -n OUS_ENABLED "+ws+"/web/src", false, "exit 1", 0, ""),
		{
			id: "m2", name: "mcp__linear__get_issue", args: `{"id":"PER-412"}`, ok: false, detail: "failed",
			output: &engine.ToolOutput{Error: "401 Unauthorized: token expired; run `uah mcp login linear`"},
		},
	}
	s := base()
	s.Home = "/home/me"
	evs := []any{
		session.InputQueued{At: t0, Input: core.UserInput{ID: "m1", Text: "Map how the ontology config reaches the UI."}},
		session.InputSent{At: t0, IDs: []string{"m1"}},
		core.RunStarted{At: t0, RunID: "20260924-120000-3f2a1b2c"},
		core.AssistantMessage{At: t0, Text: "I'll read the repo instructions first."},
	}
	for _, c := range calls {
		evs = append(evs, c.events()...)
	}
	evs = append(evs, core.AssistantMessage{At: t0, Text: "Now the ontology code."})
	for _, c := range later {
		evs = append(evs, c.events()...)
		if c.id == "c7" {
			evs = append(evs, engine.AutoReviewed{
				At: t0, Command: fetch, Outcome: "allow", Risk: "medium",
				Reason: "The action makes read-only requests for the dev ontology data the user asked for and saves a local snapshot.",
			})
		}
	}
	evs = append(evs,
		core.AssistantMessage{At: t0, Text: "The live fetch succeeded.", Final: true},
		core.RunFinished{At: t0.Add(5 * time.Second), Result: core.Result{Request: core.Request{RunID: "20260924-120000-3f2a1b2c"}, Status: core.StatusOK, Wall: 5 * time.Second}},
		session.Idle{At: t0.Add(5 * time.Second)},
	)

	return apply(s, evs...)
}

func galleryScreen(s state.State, theme render.Theme) string {
	out, _ := render.Screen(s, render.NewCache(theme), render.Frame{Width: 110, Height: 60, Composer: "λ ", ComposerHeight: 1})

	return out
}

func plainScreen(out string) string {
	lines := strings.Split(ansi.Strip(out), "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " ")
	}

	return strings.Join(lines, "\n") + "\n"
}

// TestToolCalls draws the gallery's turn in both views and both themes.
// The color goldens name the theme colors each line uses.
func TestToolCalls(t *testing.T) {
	s := galleryTurn(t)
	golden(t, "tools", plainScreen(galleryScreen(s, render.Amber)))
	golden(t, "tools-details", plainScreen(galleryScreen(apply(s, state.ToggleDetails{}), render.Amber)))
	golden(t, "tools-colors", colorRoles(galleryScreen(s, render.Amber), render.Amber))
	golden(t, "tools-colors-light", colorRoles(galleryScreen(s, render.AmberLight), render.AmberLight))
	assert.Equal(t, plainScreen(galleryScreen(s, render.Amber)), plainScreen(galleryScreen(s, render.AmberLight)))
}

// colorRoles is a screen's text with the theme colors each line uses, by
// name, before it.
func colorRoles(out string, th render.Theme) string {
	roles := []struct {
		name string
		c    color.Color
	}{{"accent", th.Accent}, {"dim", th.Dim}, {"bad", th.Bad}, {"notice", th.Notice}, {"comment", th.Comment}, {"good", th.Good}}
	lines := strings.Split(out, "\n")
	for i, l := range lines {
		text := strings.TrimRight(ansi.Strip(l), " ")
		if strings.TrimSpace(text) == "" {
			lines[i] = ""

			continue
		}
		var used []string
		for _, r := range roles {
			if strings.Contains(l, fg(r.c)+"m") && !slices.Contains(used, r.name) {
				used = append(used, r.name)
			}
		}
		lines[i] = fmt.Sprintf("%-22s│ %s", strings.Join(used, " "), text)
	}

	return strings.Join(lines, "\n") + "\n"
}

// TestToolCalls_Spacing: an entry with a second line has a blank line
// above and below it, one-line entries stay together, and no two blank
// lines follow each other.
func TestToolCalls_Spacing(t *testing.T) {
	lines := transcriptOf(t, galleryTurn(t))
	text := strings.Join(lines, "\n")
	for i := 1; i < len(lines); i++ {
		assert.False(t, lines[i] == "" && lines[i-1] == "", "two blank lines at %d:\n%s", i, text)
	}
	read := index(t, lines, 0, "READ           README.md, api/README.md")
	assert.Contains(t, lines[read-1], "RAN            for p in", "one-liners stay together")
	assert.Contains(t, lines[read+1], "SKILL          web-architecture-patterns, go-infrastructure-patterns", "skills on one line")
	fail := index(t, lines, read, "git diff -- api/rest/server.go")
	assert.Equal(t, "", lines[fail-1], text)
	assert.Equal(t, "                 diff: api/rest/server.go.orig: No such file or directory", lines[fail+1])
	assert.Equal(t, "", lines[fail+2], "a blank line after the entry, which is before the next")
	heredoc := index(t, lines, fail, "python3 <<PY")
	assert.Equal(t, "", lines[heredoc+2], text)
}

// TestToolCalls_Approval: the auto-reviewer's approval goes under the
// command it approved, even when it arrives after later calls, and a
// notice says it only when no call matches.
func TestToolCalls_Approval(t *testing.T) {
	s := galleryTurn(t)
	i := slices.IndexFunc(s.Items, func(it state.Item) bool { return it.Key == "call:c7" })
	require.GreaterOrEqual(t, i, 0)
	assert.Equal(t, "auto-approved · medium risk · The action makes read-only requests for the dev ontology data the user asked for and saves a local snapshot.", s.Items[i].Note)
	for _, it := range s.Items {
		assert.NotContains(t, it.Text, "auto-approved", "no separate notice")
	}

	later := apply(base(),
		bash("a", "make test", true, "exit 0", 0, "").events()[0],
		bash("b", "make lint", true, "exit 0", 0, "").events()[0],
		engine.AutoReviewed{At: t0, Command: "make test", Outcome: "allow", Risk: "low", Reason: "runs the tests"},
		engine.AutoReviewed{At: t0, Command: "rm -rf /tmp/x", Outcome: "allow", Risk: "low", Reason: "a scratch folder"},
	)
	assert.Equal(t, "auto-approved · low risk · runs the tests", later.Items[0].Note)
	assert.Empty(t, later.Items[1].Note)
	assert.Equal(t, "auto-approved (low risk): rm -rf /tmp/x — a scratch folder", later.Items[2].Text)
}

// TestToolCalls_MCP: an MCP call reads as its server, tool, and arguments,
// with its result's summary under it in the comment color, or its error in
// the error color and the status at the line's end.
func TestToolCalls_MCP(t *testing.T) {
	out := galleryScreen(galleryTurn(t), render.Amber)
	lines := strings.Split(out, "\n")
	plain := strings.Split(plainScreen(out), "\n")
	ok := index(t, plain, 0, "MCP            docs · search  query \"ontology RootData version\", limit 5")
	assert.Equal(t, "                 {results, total} · 2.2 KB", plain[ok+1])
	assert.Contains(t, lines[ok+1], fg(render.Amber.Comment))
	bad := index(t, plain, ok, "MCP    fail    linear · get_issue  id PER-412")
	assert.True(t, strings.HasSuffix(plain[bad], "  401"), plain[bad])
	assert.Equal(t, "                 401 Unauthorized: token expired; run `uah mcp login linear`", plain[bad+1])
	assert.Contains(t, lines[bad+1], fg(render.Amber.Bad))
}

// TestToolCalls_Copy: copying a call and its error line keeps the text,
// the error under the command as drawn.
func TestToolCalls_Copy(t *testing.T) {
	s := galleryTurn(t)
	c := render.NewCache(render.Amber)
	f := render.Frame{Width: 110, Height: 60, Composer: "λ ", ComposerHeight: 1}
	render.Screen(s, c, f)
	key := "call:c3"
	s = apply(s,
		state.MousePress{At: state.TextPos{Key: key, Line: 1, Col: 0}, When: t0},
		state.MouseDrag{At: state.TextPos{Key: key, Line: 2, Col: 110}},
	)
	text, n := render.SelectedText(s, c, f)
	assert.Equal(t, 2, n)
	assert.Equal(t, "RAN    fail    git diff -- api/rest/server.go; diff api/rest/server.go api/rest/server.go.orig  exit 2\n"+
		"               diff: api/rest/server.go.orig: No such file or directory", text)
}

// TestToolCalls_Advisor: an advisor call reads ADVISE with its time and
// the advice's first line under it; the detailed view shows the advice
// whole, line by line.
func TestToolCalls_Advisor(t *testing.T) {
	advice := "1. Check the empty input.\n2. Run go test ./... before you finish."
	s := base()
	evs := []any{core.RunStarted{At: t0, RunID: "20260924-120000-3f2a1b2c"}}
	evs = append(evs, call{id: "a1", name: engine.AdvisorToolName, args: "{}", ok: true, detail: "completed", took: 12 * time.Second, output: &engine.ToolOutput{Result: advice, Size: len(advice)}}.events()...)
	s = apply(s, evs...)

	plain := strings.Split(plainScreen(galleryScreen(s, render.Amber)), "\n")
	at := index(t, plain, 0, "ADVISE 12.0s   advisor")
	assert.Equal(t, "                 1. Check the empty input.", plain[at+1], "the first line")

	s.Details = true
	plain = strings.Split(plainScreen(galleryScreen(s, render.Amber)), "\n")
	at = index(t, plain, 0, "advisor")
	assert.Contains(t, plain[at+1], "1. Check the empty input.")
	assert.Contains(t, plain[at+2], "2. Run go test ./... before you finish.")
}

// TestToolCalls_AdvisorFailed: a call that got no advice shows why under
// it; the detailed view shows what the agent read, the command's stderr
// included.
func TestToolCalls_AdvisorFailed(t *testing.T) {
	read := "The advisor is unavailable (the command uah failed with exit status 2: unknown key); go on without it.\n" +
		"config: line 3: unknown key"
	s := base()
	evs := []any{core.RunStarted{At: t0, RunID: "20260924-120000-3f2a1b2c"}}
	evs = append(evs, call{id: "a1", name: engine.AdvisorToolName, args: "{}", detail: "failed", took: time.Second,
		output: &engine.ToolOutput{Error: "the command uah failed with exit status 2: unknown key", Result: read}}.events()...)
	s = apply(s, evs...)

	plain := strings.Split(plainScreen(galleryScreen(s, render.Amber)), "\n")
	at := index(t, plain, 0, "advisor")
	assert.Contains(t, plain[at+1], "the command uah failed with exit status 2: unknown key")
	assert.NotContains(t, strings.Join(plain, "\n"), "config: line 3")

	s.Details = true
	text := plainScreen(galleryScreen(s, render.Amber))
	assert.Contains(t, text, "The advisor is unavailable (the command uah failed")
	assert.Contains(t, text, "config: line 3: unknown key")
}
