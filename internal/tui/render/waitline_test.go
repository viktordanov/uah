package render_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/internal/tui/state"
)

// TestWaitLine: the status line names the wait, how long it has lasted and
// the run has run, and counts a retry down; the detailed view shows a
// retry or a stall in the footer.
func TestWaitLine(t *testing.T) {
	s := apply(base(),
		core.RunStarted{At: t0, RunID: "r1"},
		core.TurnStarted{At: t0.Add(60 * time.Second), Turn: 1},
		engine.ModelProgress{At: t0.Add(61 * time.Second), Phase: engine.PhaseStreaming, Tool: "apply_patch", Target: "internal/foo.go", ToolBytes: 4200},
		state.Tick{Now: t0.Add(72 * time.Second)},
	)
	assert.Contains(t, screen(s, ""), "Writing a patch · internal/foo.go · 4.2 kB (12s · 1m 12s • esc to interrupt)")

	retry := apply(s, engine.Reconnecting{At: t0.Add(72 * time.Second), Attempt: 3, MaxAttempts: 10, Delay: 8 * time.Second, Reason: "connection reset by peer"})
	assert.Contains(t, screen(retry, ""), "Reconnecting · connection reset by peer · attempt 3/10 · retry in ~8s (12s · 1m 12s")
	assert.Contains(t, screen(apply(retry, state.Tick{Now: t0.Add(74500 * time.Millisecond)}), ""), "retry in ~6s", "rounded up")
	assert.Contains(t, screen(apply(retry, state.Tick{Now: t0.Add(81 * time.Second)}), ""), "attempt 3/10 · connecting")
	assert.Contains(t, screen(apply(retry, state.ToggleDetails{}), ""), " Reconnecting · connection reset by peer · attempt 3/10 · retry in ~8s")

	after := screen(apply(retry, engine.ReconnectEnded{At: t0, OK: true}), "")
	assert.NotContains(t, after, "Reconnecting")
	assert.Contains(t, after, "Writing a patch", "back to the request's progress")

	stopping := apply(s, session.InputQueued{At: t0}, state.Esc{}, state.Esc{}, state.Tick{Now: t0.Add(75 * time.Second)})
	assert.Contains(t, screen(stopping, ""), "Stopping (3s · 1m 15s • esc again to force)")

	// A wait that began within the run's first second reads as the run:
	// one time, not "6s · 6s".
	early := apply(base(),
		core.RunStarted{At: t0, RunID: "r1"},
		session.QuestionsAsked{At: t0.Add(400 * time.Millisecond), ID: "q1", CallID: "c1", Questions: []engine.Question{{ID: "a", Header: "A", Question: "Which?", Options: []engine.QuestionOption{{Label: "X"}}}}},
		state.Tick{Now: t0.Add(6 * time.Second)},
	)
	assert.Contains(t, screen(early, ""), "Waiting for your answer (6s • esc to interrupt)")

	// It stays one time at every tick within a second.
	for ms := 6000; ms < 7000; ms += 100 {
		got := screen(apply(early, state.Tick{Now: t0.Add(time.Duration(ms) * time.Millisecond)}), "")
		assert.Regexp(t, `Waiting for your answer \([67]s • esc to interrupt\)`, got, "at %dms", ms)
	}
}

// TestWaitLine_Narrow: a long path, a long command or an approval gives
// way before the times: the path to ~, its last folders and its file
// name, then the hint, then the words. The times show at every width.
func TestWaitLine_Narrow(t *testing.T) {
	run := func(evs ...any) state.State {
		s := base()
		s.Home = "/home/ada"
		evs = append([]any{core.RunStarted{At: t0, RunID: "r1"}, core.TurnStarted{At: t0.Add(80 * time.Second), Turn: 1}}, evs...)

		return apply(s, append(evs, state.Tick{Now: t0.Add(109 * time.Second)})...)
	}
	long := "/home/ada/work/clients/northwind/artifacts/release-0.8.0-run/dispositions.md"
	cmd := "go test -race -count=1 -run 'TestWaitLine|TestScreen' ./internal/tui/render/... ./internal/tui/state/..."
	cases := []struct {
		name string
		s    state.State
	}{
		{"patch", run(engine.ModelProgress{At: t0.Add(81 * time.Second), Phase: engine.PhaseStreaming, Tool: "apply_patch", Target: long, ToolBytes: 9200})},
		{"hook", run(core.ModelResponded{At: t0.Add(81 * time.Second), Turn: 1}, session.HookRan{At: t0.Add(82 * time.Second), Event: "PreToolUse", Command: cmd, Outcome: "running"})},
		{"approval", run(session.ApprovalRequested{At: t0.Add(82 * time.Second), ID: "a1", Command: cmd})},
		{"answer", run(session.QuestionsAsked{At: t0.Add(82 * time.Second), ID: "q1", CallID: "c1", Questions: []engine.Question{{ID: "a", Header: "A", Question: "Which?", Options: []engine.QuestionOption{{Label: "X"}}}}})},
	}
	var b strings.Builder
	for _, c := range cases {
		for _, w := range []int{60, 80, 120} {
			line := waitLine(t, c.s, w)
			assert.LessOrEqual(t, ansi.StringWidth(line), w, "%s at %d", c.name, w)
			assert.Regexp(t, `\((\d+s · )?1m 49s( • esc to interrupt)?\)$`, line, "%s at %d: the times show", c.name, w)
			fmt.Fprintf(&b, "%-8s %3d │%s\n", c.name, w, line)
		}
	}
	golden(t, "waitline-narrow", b.String())

	at80 := waitLine(t, cases[0].s, 80)
	assert.Contains(t, at80, "…/dispositions.md", "the file name stays")
	assert.Contains(t, waitLine(t, cases[0].s, 120), "…/northwind/artifacts/release-0.8.0-run/dispositions.md", "the folders that fit stay")
	assert.Contains(t, waitLine(t, cases[0].s, 160), "· ~/work/clients/", "the home directory shows as ~")
}

// waitLine is the status line above the composer at width w.
func waitLine(t *testing.T, s state.State, w int) string {
	t.Helper()
	for _, l := range strings.Split(screenAt(s, "", w), "\n") {
		if strings.Contains(l, "1m 49s") {
			return l
		}
	}
	t.Fatalf("no status line at %d", w)

	return ""
}
