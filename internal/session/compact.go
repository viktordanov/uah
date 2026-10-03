package session

import (
	"path/filepath"
	"slices"
	"time"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/compaction"
	"github.com/viktordanov/uah/internal/engine"
)

type cmdCompact struct{ focus string }

// Compact summarizes the context, keeping every user message verbatim: before
// the live run's next model request, or before the next run's first one when
// the session is idle. The engine reports it with engine.CompactionStarted
// and engine.Compacted.
func (s *Session) Compact() error { return s.CompactWith("") }

// CompactWith is Compact with focus instructions for the summary, as Claude
// Code's /compact <instructions>.
func (s *Session) CompactWith(focus string) error {
	_, err := call[struct{}](s, cmdCompact{focus: focus})

	return err
}

func (s *Session) onCompact(focus string) {
	s.compactPending, s.compactFocus = true, focus
	if s.state == StateRunning && s.run != nil && s.run.Compact(focus) == nil {
		s.emit(Notice{At: time.Now(), Level: LevelInfo, Message: "Compacting the context before the next model request"})

		return
	}
	s.emit(Notice{At: time.Now(), Level: LevelInfo, Message: "The context will be compacted before the next message"})
}

type cmdClear struct{}

// Clear drops the context, as /clear does, and stays in the same session:
// the model's next request starts fresh after the system prompt, while the
// session file keeps the history. It applies before the live run's next
// model request, or before the next run's first one when the session is
// idle. The engine reports it as a compaction with the clear trigger.
func (s *Session) Clear() error {
	_, err := call[struct{}](s, cmdClear{})

	return err
}

func (s *Session) onClear() {
	s.clearPending = true
	s.clearGoal(false) // as Codex's /clear starts a thread without one
	if s.state == StateRunning && s.run != nil {
		_ = s.run.Clear() // a run that ended already leaves it to the next
	}
}

// noteCompaction clears a pending /compact or /clear once the engine starts
// it, so a request that a run could not serve moves on to the next run.
func (s *Session) noteCompaction(e core.Event) {
	v, ok := e.(engine.CompactionStarted)
	switch {
	case !ok:
	case v.Trigger == compaction.TriggerManual:
		s.compactPending, s.compactFocus = false, ""
	case v.Trigger == compaction.TriggerClear:
		s.clearPending = false
	}
}

// withCompactions adds the session's saved compactions to its loaded runs as
// engine.Compacted events, each in the run it happened in and in time order,
// so a reloaded transcript shows them. The runner's events.jsonl stays as
// the runner wrote it; the compaction log is the source.
func withCompactions(stateDir, id string, runs []LoadedRun) ([]LoadedRun, error) {
	if len(runs) == 0 {
		return runs, nil
	}
	records, _, err := compaction.OpenLog(filepath.Join(stateDir, "sessions"), id).Records()
	if err != nil {
		return nil, err
	}
	for _, rec := range records {
		place(runs, engine.Compacted{At: rec.At, Trigger: rec.Trigger, Summary: rec.Summary, Stats: rec.Stats})
	}

	return runs, nil
}

// place adds an event to the run it happened in, the last one that started
// before it, among that run's events in time order.
func place(runs []LoadedRun, ev core.Event) {
	at := ev.OccurredAt()
	i := len(runs) - 1
	for i > 0 && runs[i].Record.Result.StartedAt.After(at) {
		i--
	}
	events := runs[i].Events
	j := slices.IndexFunc(events, func(e core.Event) bool { return e.OccurredAt().After(at) })
	if j < 0 {
		j = len(events)
	}
	runs[i].Events = slices.Insert(events, j, ev)
}
