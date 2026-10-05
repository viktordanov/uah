package bench_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/tools/agentbench/bench"
)

var price = bench.Price{Input: 1, Cached: 0.1, Output: 10}

// The fixtures are real streams of one prompt ("run ls and cat go.mod in
// parallel, then reply DONE") at low effort; codex.jsonl is stamped as the
// harness stamps it.
func TestParseUAH(t *testing.T) {
	f, err := os.Open("testdata/uah.jsonl")
	require.NoError(t, err)
	defer f.Close()
	start := time.Date(2026, 10, 1, 18, 57, 5, 700_000_000, time.UTC)
	tl, err := bench.ParseUAH(f, start, "")
	require.NoError(t, err)

	assert.Equal(t, 1, tl.Turns)
	assert.Equal(t, "DONE", tl.Answer)
	require.Len(t, tl.Requests, 2)
	assert.Equal(t, bench.Tokens{Input: 25846, Cached: 7680, Output: 70}, tl.Tokens)
	r := tl.Requests[0]
	assert.Equal(t, int64(128), r.StartMS) // responded at 10.928 after 5.1 s
	assert.Equal(t, int64(5228), r.EndMS)
	assert.Equal(t, int64(2290), r.FirstMS, "the first streamed text")
	assert.Equal(t, 2, r.ToolCalls)
	require.Len(t, tl.Calls, 2)
	assert.Equal(t, "Bash", tl.Calls[0].Name)
	assert.Equal(t, `{"command":"ls"}`, tl.Calls[0].Args)
	assert.True(t, tl.Calls[0].OK)

	m := tl.Compute(8400*time.Millisecond, price)
	assert.Equal(t, 2, m.MaxConcurrent)
	assert.Equal(t, 2, m.Requests)
	assert.Equal(t, 2, m.ToolCalls)
	assert.Equal(t, int64(0), m.OverlapMS)
	assert.Equal(t, m.WallMS, m.ModelOnlyMS+m.ToolOnlyMS+m.OverlapMS+m.IdleMS)
	assert.InDelta(t, (25846-7680)*1e-6+7680*0.1e-6+70*10e-6, m.CostUSD, 1e-9)
}

// TestParseUAH_EffortFromDiagnostics: a request's model_attempt line in the
// run's stderr.log gives its effort and, with adaptive effort, why.
func TestParseUAH_EffortFromDiagnostics(t *testing.T) {
	start := time.Date(2026, 10, 1, 18, 57, 5, 700_000_000, time.UTC)
	parse := func(stateDir string) *bench.Timeline {
		f, err := os.Open("testdata/uah.jsonl")
		require.NoError(t, err)
		defer f.Close()
		tl, err := bench.ParseUAH(f, start, stateDir)
		require.NoError(t, err)

		return tl
	}
	plain := parse("")
	assert.Equal(t, "low", plain.Requests[1].Effort, "the session's")
	state := t.TempDir()
	logDir := filepath.Join(state, "runs", "r1")
	require.NoError(t, os.MkdirAll(logDir, 0o700))
	var lines string
	for i, r := range plain.Requests {
		at := start.Add(time.Duration(r.StartMS) * time.Millisecond).Format(time.RFC3339Nano)
		effort, reason := "low", "r1: first request"
		if i == 1 {
			effort, reason = "medium", "r1: confirmations: short output"
		}
		lines += `{"diag":"model_attempt","at":"` + at + `","kind":"turn","first_byte_ms":100,"result":"ok","effort":"` + effort + `","effort_reason":"` + reason + `"}` + "\n"
	}
	require.NoError(t, os.WriteFile(filepath.Join(logDir, "stderr.log"), []byte(lines), 0o600))

	tl := parse(state)
	assert.Equal(t, "low", tl.Requests[0].Effort)
	assert.Equal(t, "medium", tl.Requests[1].Effort)
	assert.Equal(t, "r1: confirmations: short output", tl.Requests[1].EffortReason)
	b := tl.Compute(8400*time.Millisecond, price).Behavior
	assert.Equal(t, 1, b.ChangedEffortRequests)
	assert.Equal(t, 0, b.SameEffortRequests)
}

func TestParseCodex(t *testing.T) {
	f, err := os.Open("testdata/codex.jsonl")
	require.NoError(t, err)
	defer f.Close()
	start := time.Date(2026, 10, 1, 18, 57, 26, 0, time.UTC)
	tl, err := bench.ParseCodex(f, start)
	require.NoError(t, err)

	assert.Equal(t, 1, tl.Turns)
	assert.Equal(t, "DONE", tl.Answer)
	assert.Equal(t, bench.Tokens{Input: 37068, Cached: 30464, Output: 105}, tl.Tokens)
	assert.Equal(t, []string{"requests", "request_tokens"}, tl.Inferred)
	// From the turn's start to the first command, and from the last
	// command's end to the turn's end.
	require.Len(t, tl.Requests, 2)
	assert.Equal(t, bench.Request{StartMS: 14, EndMS: 8028, ToolCalls: 2, Stop: "complete", TextBytes: 37}, tl.Requests[0])
	assert.Equal(t, bench.Request{StartMS: 8049, EndMS: 11063, Stop: "complete", TextBytes: 4}, tl.Requests[1])
	require.Len(t, tl.Calls, 2)
	assert.Equal(t, "shell", tl.Calls[0].Name)
	assert.Equal(t, "/bin/zsh -lc ls", tl.Calls[0].Args)
	assert.Equal(t, "exit 0", tl.Calls[0].Detail)

	m := tl.Compute(11100*time.Millisecond, price)
	assert.Equal(t, 2, m.MaxConcurrent)
	assert.Equal(t, int64(8014+3014), m.ModelMS)
	assert.Equal(t, int64(21), m.ToolMS)
}

func TestCompute(t *testing.T) {
	tl := &bench.Timeline{
		Requests: []bench.Request{{StartMS: 0, EndMS: 1000}, {StartMS: 1500, EndMS: 3000}, {Agent: "child", StartMS: 1600, EndMS: 2000}},
		Calls: []bench.Call{
			{Name: "Bash", Kind: "tool", StartMS: 1000, EndMS: 5000, Args: "sleep 4"},
			{Name: "Bash", Kind: "tool", StartMS: 1000, EndMS: 1200},
			{Name: "wait", Kind: "wait", StartMS: 3000, EndMS: 6000},
		},
	}
	m := tl.Compute(7*time.Second, price)
	assert.Equal(t, int64(3000-500), m.ModelMS)
	assert.Equal(t, int64(4000), m.ToolMS)
	assert.Equal(t, int64(1500), m.OverlapMS)
	assert.Equal(t, int64(1000), m.ModelOnlyMS)
	assert.Equal(t, int64(2500), m.ToolOnlyMS)
	assert.Equal(t, int64(2000), m.IdleMS)
	assert.Equal(t, int64(1000), m.WaitMS, "the wait from 5 s to 6 s blocked on nothing else")
	assert.Equal(t, 2, m.MaxConcurrent)
	assert.InDelta(t, 4200.0/4000, m.AvgConcurrent, 1e-9)
	assert.Equal(t, 3, m.ToolCalls)
	assert.Equal(t, 1, m.Subagents)
	assert.Equal(t, "Bash sleep 4", m.LongestCall)
}

const tasksDir = "../testdata/tasks"

func TestTasksLoad(t *testing.T) {
	tasks, err := bench.LoadTasks(tasksDir, nil)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(tasks), 10)
	for _, task := range tasks {
		assert.NotEmpty(t, task.Tags, task.Name)
	}
}

// TestTasksValid dry-runs every task without a slow check: the check fails
// on the untouched repository and passes on the reference solution. -short
// skips it; `go run ./tools/agentbench -dry` runs every task.
func TestTasksValid(t *testing.T) {
	if testing.Short() {
		t.Skip("dry-runs the tasks' checks")
	}
	tasks, err := bench.LoadTasks(tasksDir, nil)
	require.NoError(t, err)
	tasks = slices.DeleteFunc(tasks, func(task bench.Task) bool { return slices.Contains(task.Tags, "slow") })
	// A fixed scratch directory keeps its Go build cache between runs.
	vs, err := bench.Validate(context.Background(), tasks, filepath.Join(os.TempDir(), "uah-agentbench-test"), 8)
	require.NoError(t, err)
	for _, v := range vs {
		assert.True(t, v.OK(), "%s: untouched passed=%v, solution passed=%v %s\n%s", v.Task, v.Untouched.Passed, v.Solved.Passed, v.Error, v.Solved.Output)
	}
}

func TestPlanAlternates(t *testing.T) {
	cfg := bench.Config{Tasks: []bench.Task{{Name: "a"}, {Name: "b"}}, Harnesses: []string{"uah", "codex"}, Repeat: 2, Model: "m", Effort: "low"}
	var order []string
	for _, k := range bench.Plan(cfg) {
		order = append(order, k.Task+k.Harness)
	}
	assert.Equal(t, []string{"auah", "acodex", "bcodex", "buah", "acodex", "auah", "buah", "bcodex"}, order)
}

// Codex runs a response's calls one after another: a gap shorter than a
// request between them is not a request.
func TestParseCodexSerialCalls(t *testing.T) {
	stream := `2026-10-01T10:00:00.000Z	{"type":"turn.started"}
2026-10-01T10:00:02.000Z	{"type":"item.started","item":{"id":"a","type":"command_execution","command":"ls"}}
2026-10-01T10:00:02.500Z	{"type":"item.completed","item":{"id":"a","type":"command_execution","command":"ls","exit_code":0,"status":"completed"}}
2026-10-01T10:00:02.600Z	{"type":"item.started","item":{"id":"b","type":"command_execution","command":"cat x"}}
2026-10-01T10:00:02.700Z	{"type":"item.completed","item":{"id":"b","type":"command_execution","command":"cat x","exit_code":1,"status":"failed"}}
2026-10-01T10:00:05.000Z	{"type":"item.completed","item":{"id":"c","type":"file_change","changes":[],"status":"completed"}}
2026-10-01T10:00:07.000Z	{"type":"item.completed","item":{"id":"d","type":"agent_message","text":"ok"}}
2026-10-01T10:00:07.000Z	{"type":"turn.completed","usage":{"input_tokens":10,"cached_input_tokens":0,"output_tokens":1}}
`
	tl, err := bench.ParseCodex(strings.NewReader(stream), time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	assert.Equal(t, []bench.Request{
		{StartMS: 0, EndMS: 2000, ToolCalls: 2, Stop: "complete"},
		{StartMS: 2700, EndMS: 5000, ToolCalls: 1, Stop: "complete"},
		{StartMS: 5000, EndMS: 7000, Stop: "complete", TextBytes: 2},
	}, tl.Requests)
	require.Len(t, tl.Calls, 3)
	assert.False(t, tl.Calls[1].OK)
	assert.Equal(t, "apply_patch", tl.Calls[2].Name)
}

// A call that starts while a long command runs follows a request.
func TestParseCodexBackgroundCall(t *testing.T) {
	stream := `2026-10-01T10:00:00.000Z	{"type":"turn.started"}
2026-10-01T10:00:02.000Z	{"type":"item.started","item":{"id":"a","type":"command_execution","command":"go test ./..."}}
2026-10-01T10:00:06.000Z	{"type":"item.started","item":{"id":"b","type":"command_execution","command":"cat x"}}
2026-10-01T10:00:06.100Z	{"type":"item.completed","item":{"id":"b","type":"command_execution","command":"cat x","exit_code":0,"status":"completed"}}
2026-10-01T10:00:30.000Z	{"type":"item.completed","item":{"id":"a","type":"command_execution","command":"go test ./...","exit_code":0,"status":"completed"}}
2026-10-01T10:00:32.000Z	{"type":"turn.completed","usage":{"input_tokens":10,"cached_input_tokens":0,"output_tokens":1}}
`
	tl, err := bench.ParseCodex(strings.NewReader(stream), time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	assert.Equal(t, []bench.Request{
		{StartMS: 0, EndMS: 2000, ToolCalls: 1, Stop: "complete"},
		{StartMS: 2000, EndMS: 6000, ToolCalls: 1, Stop: "complete"},
		{StartMS: 30000, EndMS: 32000, Stop: "complete"},
	}, tl.Requests)
	m := tl.Compute(32*time.Second, price)
	assert.Equal(t, int64(4000), m.OverlapMS)
	assert.Equal(t, 2, m.MaxConcurrent)
}

func TestBehavior(t *testing.T) {
	tl := &bench.Timeline{
		Requests: []bench.Request{
			{StartMS: 0, EndMS: 1000, Effort: "high", Stop: "complete", Tokens: bench.Tokens{Output: 10, Input: 100}},
			{StartMS: 1100, EndMS: 3000, Effort: "high", Stop: "complete", Tokens: bench.Tokens{Output: 110, Reasoning: 10, Input: 200, Cached: 180}},
			{StartMS: 3100, EndMS: 4000, Effort: "medium", Stop: "complete", Tokens: bench.Tokens{Output: 20, Input: 300, Cached: 150}, TextBytes: 100},
			{StartMS: 5000, EndMS: 5500, Effort: "medium", Stop: "canceled", Tokens: bench.Tokens{Input: 400, Cached: 400}},
		},
		Calls: []bench.Call{
			{Name: "SkillUse", Request: 0, ArgsBytes: 0},
			{Name: "apply_patch", Request: 1, ArgsBytes: 300, OK: true},
			{Name: "Bash", Args: `{"command":"go test ./...","sandbox_permissions":"require_escalated"}`, Request: 2, ArgsBytes: 100, IssuedMS: 4000, StartMS: 4600, EndMS: 4900, Escalated: true, OK: true},
		},
	}
	b := tl.Compute(6*time.Second, price).Behavior
	assert.Equal(t, 1, b.RitualRequests)
	assert.Equal(t, int64(1100), b.RitualMS)
	assert.Equal(t, 1, b.PatchThenVerify)
	assert.Equal(t, 1, b.Escalations)
	assert.Equal(t, int64(600), b.ReviewMS)
	assert.Equal(t, 1, b.ApprovalWaits)
	assert.Equal(t, int64(600), b.ApprovalWaitMS)
	assert.Equal(t, 1, b.Aborted)
	assert.Equal(t, int64(500), b.AbortedMS)
	assert.Equal(t, map[string]int{"high": 2, "medium": 2}, b.Efforts)
	assert.Equal(t, int64(10), b.OutputReasoning)
	assert.Equal(t, int64(100), b.OutputPatch)
	assert.Equal(t, int64(10), b.OutputToolArgs)
	assert.Equal(t, int64(20), b.OutputText)
	assert.Equal(t, 2, b.SameEffortRequests)
	assert.Equal(t, int64(600), b.SameEffortInput)
	assert.InDelta(t, 580.0/600, b.SameEffortCacheRatio, 1e-9)
	assert.Equal(t, 1, b.ChangedEffortRequests)
	assert.InDelta(t, 0.5, b.ChangedEffortCacheRatio, 1e-9)
}

// TestFixtureReadmes: each task stores its repository's READMEs as
// README.fixture.md, so Memoria tracks none of them, and the agent's
// workspace gets them as README.md, byte for byte; the reference solution's
// replace them the same way.
func TestFixtureReadmes(t *testing.T) {
	tasks, err := bench.LoadTasks(tasksDir, nil)
	require.NoError(t, err)
	var seen atomic.Int64
	t.Run("tasks", func(t *testing.T) {
		for _, task := range tasks {
			t.Run(task.Name, func(t *testing.T) {
				t.Parallel()
				checkFixtureReadmes(t, task, &seen)
			})
		}
	})
	assert.EqualValues(t, 39, seen.Load(), "every fixture README, the vendored project's nested ones too")
}

// checkFixtureReadmes prepares task's workspace and checks its READMEs,
// counting them in seen.
func checkFixtureReadmes(t *testing.T, task bench.Task, seen *atomic.Int64) {
	t.Helper()
	task.Setup, task.SolutionScript = "", "" // the copies only
	ws := t.TempDir()
	require.NoError(t, task.Prepare(context.Background(), ws, os.Environ()), task.Name)
	for _, dir := range []string{"repo", "solution"} {
		if dir == "solution" {
			require.NoError(t, task.ApplySolution(context.Background(), ws, os.Environ()), task.Name)
		}
		root := filepath.Join(task.Dir, dir)
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			assert.NotEqual(t, "README.md", d.Name(), "%s is stored as README.fixture.md", p)
			if d.Name() != "README.fixture.md" {
				return nil
			}
			seen.Add(1)
			rel, err := filepath.Rel(root, filepath.Dir(p))
			require.NoError(t, err)
			want, err := os.ReadFile(p)
			require.NoError(t, err)
			got, err := os.ReadFile(filepath.Join(ws, rel, "README.md"))
			require.NoError(t, err, p)
			assert.Equal(t, string(want), string(got), p)
			assert.NoFileExists(t, filepath.Join(ws, rel, "README.fixture.md"))

			return nil
		})
		if !errors.Is(err, fs.ErrNotExist) {
			require.NoError(t, err)
		}
	}
}
