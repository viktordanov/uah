package state_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/viktordanov/uagent/core"
	uaharness "github.com/viktordanov/uagent/harness"

	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/internal/tui/state"
)

// savedRuns is a session's history of n runs, each a message, a turn with
// a tool call, and an answer.
func savedRuns(n int) state.HistoryLoaded {
	runs := make([]session.LoadedRun, n)
	for i := range runs {
		id := fmt.Sprintf("run-%d", i)
		runs[i] = session.LoadedRun{
			Record: uaharness.RunRecord{Complete: true, Result: core.Result{
				Request: core.Request{RunID: id, SessionID: "sess-1"}, Status: core.StatusOK, StartedAt: t0, Wall: time.Second,
			}},
			Events: []core.Event{
				core.UserMessage{At: t0, ID: fmt.Sprintf("m%d", i), Text: "list the files"},
				core.TurnStarted{At: t0, Turn: 1},
				core.ToolCalled{At: t0, CallID: fmt.Sprintf("c%d", i), Name: "Bash", Label: "ls"},
				core.ToolFinished{At: t0, CallID: fmt.Sprintf("c%d", i), Name: "Bash"},
				core.ModelResponded{At: t0, Turn: 1},
				core.AssistantMessage{At: t0, Text: "Here they are.", Final: true},
			},
		}
	}

	return state.HistoryLoaded{SessionID: "sess-1", Runs: runs}
}

// replayTime is the shortest of three replays of the history.
func replayTime(h state.HistoryLoaded) time.Duration {
	best := time.Duration(1<<63 - 1)
	for range 3 {
		start := time.Now()
		state.Reduce(opened(), h)
		best = min(best, time.Since(start))
	}

	return best
}

// TestReplay_GrowsLinearly: replaying eight times the runs takes about
// eight times as long. A run's end that scanned the whole transcript made
// it about sixty times.
func TestReplay_GrowsLinearly(t *testing.T) {
	if testing.Short() {
		t.Skip("times replays of 8,000 runs")
	}
	small, large := replayTime(savedRuns(1000)), replayTime(savedRuns(8000))
	if ratio := float64(large) / float64(small); ratio > 20 {
		t.Errorf("8,000 runs replay in %v, %.0f times 1,000 runs' %v", large, ratio, small)
	}
}

// BenchmarkReplay loads a resumed session's history.
func BenchmarkReplay(b *testing.B) {
	for _, n := range []int{1000, 4000, 16000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			h := savedRuns(n)
			b.ReportAllocs()
			for b.Loop() {
				state.Reduce(opened(), h)
			}
		})
	}
}
