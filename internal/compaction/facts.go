package compaction

import (
	"encoding/json/v2"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/viktordanov/uah-core/harness/llm"

	"github.com/viktordanov/uah/internal/patch"
)

// The tools whose calls the facts read, by the names the model calls them.
const (
	toolBash      = "Bash"
	toolSkillUse  = "SkillUse"
	toolViewImage = "ViewImage"
	toolSpawn     = "spawn_agent"
	toolClose     = "close_agent"
	// toolAdvisor's outputs, like SkillUse's, are never elided (Elidable).
	toolAdvisor = "advisor"
)

// Facts are what a stretch of history established that later turns rely
// on, read from its tool calls by rules, with no model: the files changed,
// the commands that still fail, the files read, the skills loaded, and the
// subagents still open. The state ledger lists them, and the offline
// evaluation checks a compacted request for them.
type Facts struct {
	Changed []FileChange
	Failing []FailedCommand
	// Read are the paths the commands named and the images viewed, oldest
	// first, without the changed files.
	Read   []string
	Skills []string
	// Agents are the subagents spawned and not closed: "id (nickname)".
	Agents []string
}

// FileChange is one file apply_patch changed, its lines summed over the
// covered patches.
type FileChange struct {
	Path           string
	Added, Removed int
	// Created or Deleted by the last patch that touched it.
	Created, Deleted bool
}

// FailedCommand is a Bash command whose last run failed: its exit code and
// the end of its stderr (else of its output).
type FailedCommand struct {
	Command string
	Exit    int
	Tail    string
}

// pathLike finds file paths in a command: a/b.ext or a known source file
// name. It is the rule the compaction research used (strategies.py).
var pathLike = regexp.MustCompile(`(?:^|[^\w/.-])((?:[\w.-]+/)+[\w.-]+\.\w+|[\w-]+\.(?:go|md|rs|py|ts|tsx|toml|json|yaml|yml|sh))(?:$|[^\w/])`)

// PathsIn are the file paths a command names, in order.
func PathsIn(command string) []string {
	var out []string
	for pos := 0; pos < len(command); {
		m := pathLike.FindStringSubmatchIndex(command[pos:])
		if m == nil {
			break
		}
		out = append(out, command[pos+m[2]:pos+m[3]])
		pos += m[3] // the next path may start right after this one's end
	}

	return out
}

// exitLine is how the runner's Bash tool (v0.1.1, tool/bash) ends the
// output of a command that failed.
var exitLine = regexp.MustCompile(`(?m)^Exit code: (-?\d+)$`)

// ExtractFacts reads the facts from items in order.
func ExtractFacts(items []llm.Item) Facts {
	x := factReader{calls: map[string]llm.ToolCall{}, failing: map[string]FailedCommand{}, changed: map[string]*FileChange{}, agents: map[string]string{}}
	for _, item := range items {
		switch d := item.Data.(type) {
		case llm.ToolCall:
			x.calls[d.CallID] = d
			x.onCall(d)
		case llm.ToolResult:
			if call, ok := x.calls[d.CallID]; ok {
				x.onResult(call, ResultText(d))
			}
		}
	}

	return x.facts()
}

type factReader struct {
	calls   map[string]llm.ToolCall
	changed map[string]*FileChange
	order   []string // changed paths, in the order first changed
	failing map[string]FailedCommand
	fails   []string // failing commands, in the order they last failed
	read    []string
	skills  []string
	agents  map[string]string
	spawned []string
}

func (x *factReader) onCall(call llm.ToolCall) {
	switch call.Name {
	case toolBash:
		x.read = appendNew(x.read, PathsIn(Argument(call.Arguments, "command"))...)
	case toolViewImage:
		x.read = appendNew(x.read, Argument(call.Arguments, "path"))
	case toolSkillUse:
		x.skills = appendNew(x.skills, Argument(call.Arguments, "name"))
	case toolClose:
		delete(x.agents, Argument(call.Arguments, "target"))
	}
}

func (x *factReader) onResult(call llm.ToolCall, text string) {
	switch call.Name {
	case toolBash:
		x.onBash(Argument(call.Arguments, "command"), text)
	case patch.ToolName:
		if strings.HasPrefix(text, "Success.") {
			x.onPatch(call.Arguments)
		}
	case toolSpawn:
		var r struct {
			AgentID  string `json:"agent_id"`
			Nickname string `json:"nickname"`
		}
		if json.Unmarshal([]byte(text), &r) == nil && r.AgentID != "" {
			x.agents[r.AgentID] = strings.TrimSpace(r.AgentID + " (" + r.Nickname + ")")
			x.spawned = append(x.spawned, r.AgentID)
		}
	}
}

// onBash notes a failed run of a command, or clears it when it now passes.
func (x *factReader) onBash(command, text string) {
	command = strings.TrimSpace(command)
	if command == "" {
		return
	}
	x.fails = slices.DeleteFunc(x.fails, func(c string) bool { return c == command })
	m := exitLine.FindAllStringSubmatch(text, -1)
	if len(m) == 0 {
		delete(x.failing, command)

		return
	}
	code, _ := strconv.Atoi(m[len(m)-1][1])
	x.failing[command] = FailedCommand{Command: command, Exit: code, Tail: failureTail(text)}
	x.fails = append(x.fails, command)
}

// failureTail is the last lines of a failed command's stderr, else of its
// output, without the exit line.
func failureTail(text string) string {
	text = exitLine.ReplaceAllString(text, "")
	if _, stderr, ok := strings.Cut(text, "Stderr:\n"); ok {
		text = stderr
	}
	lines := strings.Split(strings.TrimSpace(text), "\n")

	return strings.Join(lines[max(len(lines)-3, 0):], "\n")
}

func (x *factReader) onPatch(arguments string) {
	text, err := patch.ParseArgs(arguments)
	if err != nil {
		return
	}
	hunks, err := patch.Parse(text)
	if err != nil {
		return
	}
	for _, h := range hunks {
		c := x.changed[h.Target()]
		if c == nil {
			c = &FileChange{Path: h.Target()}
			x.changed[h.Target()] = c
			x.order = append(x.order, h.Target())
		}
		switch h.Op {
		case patch.Add:
			c.Created, c.Deleted = true, false
			c.Added += strings.Count(h.Contents, "\n")
		case patch.Delete:
			c.Deleted = true
		case patch.Update:
			for _, ch := range h.Chunks {
				same := common(ch.Old, ch.New)
				c.Added += len(ch.New) - same
				c.Removed += len(ch.Old) - same
			}
		}
	}
}

// common counts the lines before and after share (context lines), as
// multisets.
func common(before, after []string) int {
	seen := map[string]int{}
	for _, l := range before {
		seen[l]++
	}
	n := 0
	for _, l := range after {
		if seen[l] > 0 {
			seen[l]--
			n++
		}
	}

	return n
}

func (x *factReader) facts() Facts {
	f := Facts{Skills: x.skills}
	for _, p := range x.order {
		f.Changed = append(f.Changed, *x.changed[p])
	}
	for _, c := range x.fails {
		f.Failing = append(f.Failing, x.failing[c])
	}
	for _, p := range x.read {
		if x.changed[p] == nil {
			f.Read = append(f.Read, p)
		}
	}
	for _, id := range x.spawned {
		if a, ok := x.agents[id]; ok {
			f.Agents = append(f.Agents, a)
		}
	}

	return f
}

// Argument is a string argument of a tool call's JSON arguments, or "".
func Argument(arguments, name string) string {
	var args map[string]any
	if json.Unmarshal([]byte(arguments), &args) != nil {
		return ""
	}
	s, _ := args[name].(string)

	return s
}

// ResultText is a tool result's text parts, joined.
func ResultText(r llm.ToolResult) string {
	var parts []string
	for _, o := range r.Output {
		if o.Kind == llm.ToolResultText {
			parts = append(parts, o.Value)
		}
	}

	return strings.Join(parts, "\n")
}

// appendNew appends each value not yet in list, moving a repeated one to
// the end, so the list ends with the most recent.
func appendNew(list []string, values ...string) []string {
	for _, v := range values {
		if v = strings.TrimSpace(v); v == "" {
			continue
		}
		list = append(slices.DeleteFunc(list, func(s string) bool { return s == v }), v)
	}

	return list
}
