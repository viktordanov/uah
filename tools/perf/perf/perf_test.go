package perf_test

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/home"
	"github.com/viktordanov/uah/tools/perf/perf"
)

// environment are the variables perf.NewEnv sets or clears; the test
// restores them.
var environment = append([]string{
	"HOME", "UAH_HOME", "CODEX_HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "OPENAI_API_KEY", "SHELL",
}, home.Variables...)

// bound is a ceiling for one metric of one scenario: about ten times what
// the scenario costs on a laptop, so only a large regression fails the test.
type bound struct {
	scenario, metric string
	max              float64
}

var bounds = []bound{
	{"load/small", "first_request_ms", 2_000},
	{"load/small", "alloc_mb", 200},
	{"tui/small", "first_frame_ms", 2_000},
	{"tui/small", "view_p95_ms", 50},
	{"turn/small", "cpu_ms", 2_000},
	{"turn/small", "alloc_mb", 500},
	{"turn/small", "records_appended", 300},
	{"fork/small", "child_first_request_ms", 10_000},
	{"spawn/small", "child_first_request_ms", 3_000},
	{"tui-turn/small", "view_p95_ms", 50},
	// The TUI's clock stops when the turn ends: no update while idle.
	{"idle/tui", "updates_per_s", 1},
	{"idle/tui", "cpu_ms_per_s", 200},
	// The renderer checks the view 30 times a second, about 165 wakeups (60
	// would be about 320).
	{"idle/tui", "wakeups_per_s", 250},
	// After three streamed turns, the open session keeps about 0.2 MB more
	// live heap and the process about 0.3 MB more from the OS; a finished
	// run kept in memory (about 1 MB here) or a heap never returned
	// (about 8 MB) fails.
	{"memory/small", "live_mb", 0.6},
	{"memory/small", "retained_mb", 4},
	{"agents/small", "peak_goroutines", 1_000},
	{"leak/5-runs", "goroutines_left", 50},
	{"leak/5-runs", "conns_after", 20},
}

// TestPerf_SmallFixtures runs every scenario on the small fixture and
// checks generous ceilings, so CI catches a large regression without
// flaking on a slow machine. go run ./tools/perf measures properly. The
// race detector would only blur the ceilings, so it runs without one (CI
// runs it in a step of its own).
func TestPerf_SmallFixtures(t *testing.T) {
	if testing.Short() {
		t.Skip("the performance harness takes about ten seconds")
	}
	if raceEnabled {
		t.Skip("the ceilings are for a build without the race detector")
	}
	for _, k := range environment {
		t.Setenv(k, os.Getenv(k)) // restored after the test
	}
	rep, err := perf.Run(context.Background(), perf.Options{Sizes: []perf.Size{perf.Small}, Scratch: t.TempDir(), Keep: true})
	require.NoError(t, err)
	for _, b := range bounds {
		r, ok := rep.Result(b.scenario)
		if !assert.True(t, ok, "%s ran", b.scenario) {
			continue
		}
		got, ok := r.Metrics[b.metric]
		if assert.True(t, ok, "%s measured %s", b.scenario, b.metric) {
			assert.LessOrEqual(t, got, b.max, "%s %s", b.scenario, b.metric)
		}
	}
	if t.Failed() {
		var out testWriter
		rep.Write(&out)
		t.Log("\n" + string(out))
	}
}

type testWriter []byte

func (w *testWriter) Write(b []byte) (int, error) {
	*w = append(*w, b...)

	return len(b), nil
}
