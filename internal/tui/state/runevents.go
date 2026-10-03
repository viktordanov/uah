package state

import (
	"cmp"
	"fmt"
	"slices"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/contextprep"
	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/images"
	"github.com/viktordanov/uah/internal/session"
)

// onRunEvent folds one of the runner's events into the transcript.
func (s *State) onRunEvent(ev core.Event) { //nolint:gocyclo // a dispatch switch over a closed set; see docs/documentation/architecture.md
	switch e := ev.(type) {
	case core.RunStarted:
		s.Live = &Live{RunID: e.RunID, Started: e.At}
		s.put(Item{Kind: KindRun, Key: "run:" + e.RunID, RunID: e.RunID, Status: core.StatusRunning, Started: e.At})
	case core.PreflightWarning:
		s.notice(session.LevelWarning, e.Message)
	case core.UserMessage:
		// The runner's echo delivers a message this session sent; any other
		// message comes from history or another client.
		if s.shellMessage(e) {
			return
		}
		if it, ok := goalItem(e.ID, e.Text); ok {
			s.put(it)

			return
		}
		note, ok := s.agentNote(e.Text)
		if !ok {
			note, ok = reviewNote(e.Text)
		}
		if !ok && contextprep.IsPrepared(e.Text) {
			note, ok = preparedNote(e.Text), true // a session from before the developer role
		}
		if ok {
			s.put(Item{Kind: KindNotice, Key: "msg:" + e.ID, Text: note, Level: session.LevelInfo})

			return
		}
		if !s.update("msg:"+e.ID, func(it *Item) { it.Input = InputDelivered }) {
			s.put(Item{Kind: KindUser, Key: "msg:" + e.ID, Text: images.Display(e.Text), Raw: e.Text, Input: InputDelivered})
		}
	case core.DeveloperMessage:
		if it, ok := goalItem(e.ID, e.Text); ok {
			s.put(it)

			return
		}
		s.put(Item{Kind: KindNotice, Key: "msg:" + e.ID, Text: preparedNote(e.Text), Level: session.LevelInfo})
	case core.TurnStarted:
		l := s.live()
		l.Turn, l.Aside = cmp.Or(l.Turn, e.At), nil
		s.put(Item{Kind: KindTurn, Key: s.turnKey(e.Turn), Turn: e.Turn, Pending: true, Started: e.At})
	case core.ModelResponded:
		s.onProgress(engine.ModelProgress{Phase: engine.PhaseDone})
		s.noteUsage(e)
		s.update(s.turnKey(e.Turn), func(it *Item) {
			it.Pending, it.In, it.Out, it.Duration = false, e.Usage.InputTokens, e.Usage.OutputTokens, e.Duration
		})
		if e.Failure != "" {
			s.notice(session.LevelError, "model failure: "+e.Failure)
		}
	case core.ToolCalled:
		s.onToolCalled(e)
	case core.ToolStarted:
		s.live().Aside = nil
		s.update("call:"+e.CallID, func(it *Item) { it.Tool, it.Started = ToolRunning, e.At })
	case core.ToolFinished:
		state := ToolOK
		if !e.OK {
			state = ToolFailed
		}
		if !s.update("call:"+e.CallID, func(it *Item) { it.Tool, it.Detail, it.Duration = state, e.Detail, e.Duration }) {
			s.put(Item{Kind: KindTool, Key: "call:" + e.CallID, Name: e.Name, Label: e.Label, Tool: state, Detail: e.Detail, Duration: e.Duration})
		}
		if state == ToolFailed {
			s.unmerge("call:" + e.CallID)
		}
	case core.AssistantMessage:
		s.put(Item{Kind: KindAssistant, Key: s.finalKey(KindAssistant, "text"), Text: e.Text, Final: e.Final})
	case core.ReasoningSummary:
		s.put(Item{Kind: KindReasoning, Key: s.finalKey(KindReasoning, "reason"), Text: e.Text})
	case core.RunnerError:
		s.notice(session.LevelError, e.Message)
	case core.RunFinished:
		s.dropStreamed()
		s.finishRun(e.Result)
		s.Queue = slices.Clone(s.Queue)
		for i := range s.Queue {
			s.Queue[i].AfterTool = false // the session drops the marks: the queue waits for no tool call now
		}
	}
}

func (s *State) turnKey(turn int) string { return fmt.Sprintf("turn:%s:%d", s.live().RunID, turn) }

// preparedNote stands for a developer message, uah's rather than the
// user's: the prepared context a new session starts with, or another.
func preparedNote(text string) string {
	if !contextprep.IsPrepared(text) {
		return fmt.Sprintf("uah sent the model context (%.1f KB)", float64(len(text))/1024)
	}

	return fmt.Sprintf("uah prepared the session's context (%.1f KB)", float64(len(text))/1024)
}
