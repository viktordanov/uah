package state

import (
	"cmp"
	"encoding/json"
	"strings"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/cmdparse"
	"github.com/viktordanov/uah/internal/engine"
)

// Tool calls read as what they do (the gallery's R6; docs/design/tool-calls.md):
// a command as READ, LIST, or SEARCH and its targets, or as the command
// without its wrappers; an MCP call as its server, tool, and arguments;
// skills loaded one after another on one line. The shape is made once,
// when the call arrives, so drawing does no parsing.

// The tools whose lines are shaped here.
const (
	toolBash  = "Bash"
	toolSkill = "SkillUse"
)

// onToolCalled adds a tool call's line.
func (s *State) onToolCalled(e core.ToolCalled) {
	it := Item{Kind: KindTool, Key: "call:" + e.CallID, Name: e.Name, Label: s.eventLabel(e), Tool: ToolCalled, Started: e.At}
	switch {
	case e.Name == toolBash:
		it.Command = bashCommand(e.Arguments, e.Label)
		sum := cmdparse.Summarize(it.Command, s.pathEnv())
		it.Verb, it.Parts = sum.Label, sum.Parts
	case isMCP(e.Name):
		it.Parts = mcpParts(e.Name, e.Arguments)
	case e.Name == toolSkill:
		s.joinSkill(&it)
	case e.Name == engine.QuestionToolName:
		it.Verb, it.Parts = "ASK", []cmdparse.Part{{Text: questionParts(e.Arguments)}}
	}
	s.put(it)
}

// pathEnv is where the session's commands run, for their paths.
func (s *State) pathEnv() cmdparse.Env {
	return cmdparse.Env{Workspace: cmp.Or(s.Settings.Workspace, s.loadedWorkspace), Home: s.Home}
}

// bashCommand is a Bash call's command from its arguments, else the
// runner's label (cut to 120 characters).
func bashCommand(arguments, label string) string {
	var args struct {
		Command string `json:"command"`
	}
	if json.Unmarshal([]byte(arguments), &args) == nil && args.Command != "" {
		return args.Command
	}

	return label
}

// joinSkill puts a skill loaded right after another on that one's line.
func (s *State) joinSkill(it *Item) {
	if len(s.Items) == 0 {
		return
	}
	prev := s.Items[len(s.Items)-1]
	if prev.MergedInto != "" {
		prev = s.Items[s.index[prev.MergedInto]]
	}
	if prev.Kind != KindTool || prev.Name != toolSkill || prev.Tool == ToolFailed || prev.Tool == ToolStopped {
		return
	}
	it.MergedInto = prev.Key
	s.update(prev.Key, func(head *Item) {
		if len(head.Group) == 0 {
			head.Group = []string{head.Label}
		}
		head.Group = append(head.Group, it.Label)
	})
}

// unmerge gives a joined call that failed its own line again, so the
// failure shows.
func (s *State) unmerge(key string) {
	i, ok := s.index[key]
	if !ok || s.Items[i].MergedInto == "" {
		return
	}
	head, label := s.Items[i].MergedInto, s.Items[i].Label
	s.update(key, func(it *Item) { it.MergedInto = "" })
	s.update(head, func(it *Item) {
		for j, name := range it.Group {
			if name == label && j > 0 {
				it.Group = append(it.Group[:j:j], it.Group[j+1:]...)

				break
			}
		}
	})
}

// onToolOutput puts a finished call's output on its line: the line that
// says why a command failed, or an MCP call's result or error.
func (s *State) onToolOutput(e engine.ToolOutput) {
	s.update("call:"+e.CallID, func(it *Item) {
		switch {
		case e.Error != "":
			it.ErrorLine = lastLine(e.Error)
		case e.Output != "":
			it.ErrorLine = lastLine(e.Output)
		case isMCP(it.Name):
			it.Result = resultSummary(e.Result, e.Size)
		}
	})
}

// attachApproval puts the auto-reviewer's approval under the call it
// approved, the latest one with that command, and reports whether it
// found it.
func (s *State) attachApproval(command, note string) bool {
	want := oneLine(command)
	name, _, _ := strings.Cut(want, " ")
	for i := len(s.Items) - 1; i >= 0 && i >= len(s.Items)-maxApprovalLookback; i-- {
		it := s.Items[i]
		if it.Kind != KindTool || it.Note != "" {
			continue
		}
		if (it.Command != "" && oneLine(it.Command) == want) || (strings.HasPrefix(name, "mcp__") && it.Name == name) {
			s.update(it.Key, func(it *Item) { it.Note = note })

			return true
		}
	}

	return false
}

// maxApprovalLookback is how many items back an approval looks for its
// call: the calls of one response, and what came between.
const maxApprovalLookback = 64
