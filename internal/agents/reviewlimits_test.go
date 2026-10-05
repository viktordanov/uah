package agents_test

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/agents"
	"github.com/viktordanov/uah/internal/codereview"
	"github.com/viktordanov/uah/internal/engine/embedded"
	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/testing/fakellm"
)

// shortHold wakes the reviewer about a call still running after 200 ms,
// not 5 minutes, so it can answer while the call runs.
func shortHold(c *embedded.Config) { c.WakeHold = 200 * time.Millisecond }

// endless is a command that runs until it is stopped, with a mark of its
// own that pids finds.
func endless(t *testing.T) string {
	t.Helper()

	return fmt.Sprintf("sleep %d", 100000+rand.IntN(800000))
}

// pids are the processes whose command line is the command.
func pids(command string) []int {
	out, _ := exec.Command("pgrep", "-f", "^"+command+"$").Output()
	var ids []int
	for _, f := range strings.Fields(string(out)) {
		if n, err := strconv.Atoi(f); err == nil {
			ids = append(ids, n)
		}
	}

	return ids
}

// gone waits until no process runs the command.
func gone(t *testing.T, command string) {
	t.Helper()
	assert.Eventually(t, func() bool { return len(pids(command)) == 0 }, 10*time.Second, 50*time.Millisecond, "%q still runs", command)
}

// review runs a review of the uncommitted changes and returns how it
// finished, within waitTimeout.
func review(t *testing.T, s *session.Session, ev *events) session.ReviewFinished {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- s.Review(context.Background(), codereview.Target{Kind: codereview.Uncommitted}) }()
	fin := ev.reviewFinished()
	require.NoError(t, <-done)

	return fin
}

// TestReview_EndsAtTheFirstAnswer is the review that ran for 1h 24m: the
// reviewer answers while a command it started still runs. The review ends
// at that answer: the command is stopped, and no heartbeat or late result
// wakes the reviewer again.
func TestReview_EndsAtTheFirstAnswer(t *testing.T) {
	stuck := endless(t)
	e := newEnv(t, agents.Config{})
	e.llm.Route(uncommitted,
		fakellm.Reply{Commands: []string{stuck}},
		fakellm.Reply{Text: reviewAnswer},
		fakellm.Reply{Text: `{"findings":[],"overall_correctness":"patch is correct","overall_explanation":"again"}`},
	)
	s, ev := e.open(t, false, shortHold)

	started := time.Now()
	fin := review(t, s, ev)

	require.Empty(t, fin.Err)
	assert.False(t, fin.Interrupted)
	assert.Empty(t, fin.Limit)
	require.Len(t, fin.Output.Findings, 1, "the first answer is the review")
	assert.Equal(t, "One bug.", fin.Output.OverallExplanation)
	assert.Len(t, reviewerRequests(e), 2, "no turn after the answer")
	assert.Less(t, time.Since(started), 15*time.Second)
	gone(t, stuck)
	time.Sleep(300 * time.Millisecond) // a late wake would ask again now
	assert.Len(t, reviewerRequests(e), 2)
}

// TestReview_TimeLimit stops a reviewer that thinks past the time limit,
// gives it one last turn without tools, and takes that turn's answer,
// marked as stopped at the limit.
func TestReview_TimeLimit(t *testing.T) {
	gate := make(chan struct{})
	t.Cleanup(func() { close(gate) })
	e := newEnv(t, agents.Config{ReviewLimits: agents.ReviewLimits{Time: time.Second}})
	e.llm.Route(uncommitted, fakellm.Reply{Gate: gate, Text: "never"}, fakellm.Reply{Text: reviewAnswer})
	s, ev := e.open(t, false)

	fin := review(t, s, ev)

	require.Empty(t, fin.Err)
	assert.Equal(t, session.ReviewTimeLimit, fin.Limit)
	require.Len(t, fin.Output.Findings, 1)
	reqs := reviewerRequests(e)
	require.Len(t, reqs, 2)
	last := reqs[1]
	assert.Empty(t, last.ToolNames, "the last turn offers no tool")
	assert.True(t, slices.ContainsFunc(last.UserTexts, func(s string) bool { return strings.Contains(s, "reached its time limit of 1s") }))
}

// TestReview_TimeLimitWithoutAnswer fails a review whose reviewer does not
// answer in its last turn either; the limit bounds that turn too.
func TestReview_TimeLimitWithoutAnswer(t *testing.T) {
	gate := make(chan struct{})
	t.Cleanup(func() { close(gate) })
	e := newEnv(t, agents.Config{ReviewLimits: agents.ReviewLimits{Time: 500 * time.Millisecond}})
	e.llm.Route(uncommitted, fakellm.Reply{Gate: gate, Text: "never"}, fakellm.Reply{Gate: gate, Text: "never"})
	s, ev := e.open(t, false)

	fin := review(t, s, ev)

	assert.Equal(t, session.ReviewTimeLimit, fin.Limit)
	assert.Contains(t, fin.Err, "did not answer after the time limit")
	assert.Len(t, reviewerRequests(e), 2)
}

// TestReview_TokenLimit stops the reviewer when its responses pass the
// token limit, its command included, and takes its last turn's answer.
func TestReview_TokenLimit(t *testing.T) {
	stuck := endless(t)
	e := newEnv(t, agents.Config{ReviewLimits: agents.ReviewLimits{Tokens: 1000}})
	e.llm.Route(uncommitted,
		fakellm.Reply{Commands: []string{stuck}, InputTokens: 1500},
		fakellm.Reply{Text: reviewAnswer},
	)
	s, ev := e.open(t, false)

	fin := review(t, s, ev)

	require.Empty(t, fin.Err)
	assert.Equal(t, session.ReviewTokenLimit, fin.Limit)
	require.Len(t, fin.Output.Findings, 1)
	reqs := reviewerRequests(e)
	require.Len(t, reqs, 2)
	assert.Empty(t, reqs[1].ToolNames)
	assert.True(t, slices.ContainsFunc(reqs[1].UserTexts, func(s string) bool { return strings.Contains(s, "limit of 1000 tokens") }))
	gone(t, stuck)
}

// TestReview_CommandTimeout stops a reviewer's command at its limit; its
// result says why, and the review goes on.
func TestReview_CommandTimeout(t *testing.T) {
	stuck := endless(t)
	e := newEnv(t, agents.Config{ReviewLimits: agents.ReviewLimits{Command: 300 * time.Millisecond}})
	e.llm.Route(uncommitted, fakellm.Reply{Commands: []string{stuck}}, fakellm.Reply{Text: reviewAnswer})
	s, ev := e.open(t, false)

	fin := review(t, s, ev)

	require.Empty(t, fin.Err)
	assert.Empty(t, fin.Limit)
	require.Len(t, fin.Output.Findings, 1)
	reqs := reviewerRequests(e)
	require.Len(t, reqs, 2)
	assert.Contains(t, strings.Join(reqs[1].ToolOutputs, "\n"), "the command ran longer than this session's limit of 300ms for one command")
	gone(t, stuck)
}

// TestReview_StopsItsOwnCommand: in the read-only sandbox, which lets a
// command signal only its own children, the reviewer kills a command it
// started earlier, and nothing asks. The hold is a second, so the
// sandboxed command has started when the reviewer hears it still runs.
func TestReview_StopsItsOwnCommand(t *testing.T) {
	stuck := endless(t)
	e := newEnv(t, agents.Config{})
	kill := fakellm.Reply{From: func(fakellm.Request) fakellm.Reply {
		ids := pids(stuck)
		if len(ids) != 1 {
			return fakellm.Reply{Text: "no pid"}
		}

		return fakellm.Reply{Commands: []string{"kill " + strconv.Itoa(ids[0])}}
	}}
	e.llm.Route(uncommitted, fakellm.Reply{Commands: []string{stuck}}, kill, fakellm.Reply{Text: reviewAnswer})
	s, ev := e.open(t, false, e.sandboxed(t), func(c *embedded.Config) { c.WakeHold = time.Second })

	fin := review(t, s, ev)

	require.Empty(t, fin.Err)
	require.Len(t, fin.Output.Findings, 1)
	reqs := reviewerRequests(e)
	require.Len(t, reqs, 3)
	outputs := strings.Join(reqs[2].ToolOutputs, "\n")
	assert.Contains(t, outputs, "Exit code: 143", "the command ended on the reviewer's SIGTERM")
	assert.NotContains(t, outputs, "not permitted")
	assert.NotContains(t, outputs, "not run")
	gone(t, stuck)
	for _, x := range ev.all {
		_, asked := x.(session.ApprovalRequested)
		assert.False(t, asked, "stopping its own command asks no one")
	}
}

// TestReview_CannotStopOthers: a kill of a process the reviewer did not
// start stays in the sandbox, which refuses it.
func TestReview_CannotStopOthers(t *testing.T) {
	other := endless(t)
	cmd := exec.Command("sh", "-c", "exec "+other)
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	e := newEnv(t, agents.Config{})
	e.llm.Route(uncommitted, fakellm.Reply{Commands: []string{"kill " + strconv.Itoa(cmd.Process.Pid)}}, fakellm.Reply{Text: reviewAnswer})
	s, ev := e.open(t, false, e.sandboxed(t))

	fin := review(t, s, ev)

	require.Empty(t, fin.Err)
	assert.Contains(t, strings.Join(reviewerRequests(e)[1].ToolOutputs, "\n"), "not permitted")
	assert.Len(t, pids(other), 1, "the other process still runs")
}
