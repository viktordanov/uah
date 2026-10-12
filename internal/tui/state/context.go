package state

import (
	"fmt"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/compaction"
	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/session"
)

// ContextLeft is Codex's "N% context left": the last response's tokens
// against the model's window. ok is false before the first response and
// right after a compaction.
func (s State) ContextLeft() (percent int, ok bool) {
	if s.ContextUsed <= 0 {
		return 0, false
	}
	window := compaction.ContextWindow(s.Settings.Model, s.Settings.ContextWindow, s.Windows)

	return compaction.PercentLeft(s.ContextUsed, window), true
}

// noteUsage records the context a response used.
func (s *State) noteUsage(e core.ModelResponded) {
	if used := e.Usage.InputTokens + e.Usage.OutputTokens; e.Failure == "" && used > 0 {
		s.ContextUsed = used
	}
}

// onEngineEvent handles the engine's own events and reports whether ev was one.
func (s *State) onEngineEvent(ev core.Event) bool {
	switch e := ev.(type) {
	case engine.CompactionStarted:
		s.live().Aside = &Wait{What: "Compacting the context", Since: e.At}
		if e.Trigger == compaction.TriggerClear {
			return true // /clear already said so
		}
		text := "Compacting the context"
		if e.Trigger == compaction.TriggerAuto {
			text += " (automatic: the context is nearly full)"
		}
		s.notice(session.LevelInfo, text)
	case engine.Compacted:
		s.live().Aside = nil
		s.onCompacted(e)
	case engine.Reconnecting:
		s.live().Retry = &e
		s.notice(LevelDebug, fmt.Sprintf("reconnecting, attempt %d of %d: %s", e.Attempt, e.MaxAttempts, e.Reason))
	case engine.ReconnectEnded:
		s.live().Retry = nil
	case engine.EffortUpdatesOff:
		s.notice(session.LevelWarning, e.Text())
	case engine.ModelProgress:
		s.onProgress(e)
	case engine.AutoReviewing:
		s.live().Aside = &Wait{What: "Auto-reviewing the command · " + s.shownCommand(e.Command), Since: e.At}
	case engine.AutoReviewed:
		s.live().Aside = nil
		s.onAutoReviewed(e)
	case engine.AgentUpdated:
		s.onAgentUpdated(e)
	case engine.AgentActivity:
		s.onAgentActivity(e)
	case engine.ShellUpdated:
		s.onShellUpdated(e)
	case engine.PatchApplied:
		s.onPatchApplied(e)
	case engine.ToolOutput:
		s.onToolOutput(e)
	case engine.QuestionsAnswered:
		s.onQuestionsAnswered(e)
	case engine.Rewound:
		s.onRewound(e)
	case engine.WebSearch:
		s.onWebSearch(e)
	default:
		return s.onStream(ev)
	}

	return true
}

// cmdClear drops the context and stays in the same session: the screen
// clears, and the agent's next request starts fresh. /new starts a new
// session instead.
func cmdClear(s *State, _ string) []Effect {
	s.Items, s.index, s.agentIDs, s.Scroll, s.ContextUsed = nil, map[string]int{}, nil, 0, 0
	s.notice(session.LevelInfo, "Context cleared: the agent starts fresh in this session. The session keeps its history; /new starts a new session")

	return []Effect{EffClear{}}
}

// cmdCompact compacts; its argument is what the summary should focus on,
// as Claude Code's /compact [instructions].
func cmdCompact(_ *State, args string) []Effect {
	return []Effect{EffCompact{Focus: args}}
}

// onCompacted reports a compaction that ended.
func (s *State) onCompacted(e engine.Compacted) {
	if e.Interrupted {
		s.notice(session.LevelInfo, "Compaction interrupted")

		return
	}
	if e.Err != "" {
		s.notice(session.LevelWarning, e.Trigger.Verb()+" failed: "+e.Err)

		return
	}
	s.ContextUsed = 0
	if e.Stats != nil && e.Stats.Strategy == compaction.StrategyElide {
		s.notice(session.LevelInfo, fmt.Sprintf("Old tool outputs elided to save context (%d in all)", e.Stats.Elided))

		return
	}
	if e.Trigger == compaction.TriggerClear {
		s.notice(LevelDebug, "context cleared")

		return
	}
	if e.Stats != nil && e.Stats.Strategy == compaction.StrategyRemote {
		s.notice(session.LevelInfo, "Context compacted by the provider; your messages stay as written")

		return
	}
	s.notice(session.LevelInfo, "Context compacted; your messages stay as written")
	if e.Warning != "" {
		s.notice(session.LevelWarning, e.Warning)
	}
	s.notice(LevelDebug, "summary: "+e.Summary)
}
