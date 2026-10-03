package app_test

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/app"
	"github.com/viktordanov/uah/internal/goal"
	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/testing/fakellm"
)

// goalRun is a session on the embedded engine and fakellm, as the TUI
// opens it, for the /goal tests.
type goalRun struct {
	t   *testing.T
	in  app.Inputs
	llm *fakellm.Server
	s   *session.Session
}

func openGoalRun(t *testing.T, config string, replies ...fakellm.Reply) *goalRun {
	t.Helper()
	_, in := setupEnv(t)
	t.Setenv("OPENAI_API_KEY", "test-key")
	writeConfig(t, &in, config)
	llm := fakellm.New(t, replies...)
	in.Provider, in.Model, in.BaseURL, in.Interactive = "openai", "gpt-test", llm.URL, true
	g := &goalRun{t: t, in: in, llm: llm}
	g.open("")

	return g
}

// open opens the session, or resumes the session id.
func (g *goalRun) open(id string) {
	g.t.Helper()
	in := g.in
	in.SessionRef = id
	res, err := app.Setup(context.Background(), in, io.Discard)
	require.NoError(g.t, err)
	opts := res.Options
	opts.Source = session.SourceTUI
	g.s, err = session.Open(context.Background(), res.Engine, opts)
	require.NoError(g.t, err)
	s := g.s
	g.t.Cleanup(func() { _ = s.Close() })
}

// idle collects the session's events until it is idle.
func (g *goalRun) idle() []core.Event {
	g.t.Helper()
	var events []core.Event
	deadline := time.After(30 * time.Second)
	for {
		select {
		case e := <-g.s.Events():
			events = append(events, e)
			if _, ok := e.(session.Idle); ok {
				return events
			}
		case <-deadline:
			g.t.Fatal("the session did not go idle")
		}
	}
}

func (g *goalRun) goal() goal.Goal {
	g.t.Helper()
	got, ok := g.s.Goal()
	require.True(g.t, ok, "the session has a goal")

	return got
}

func countOf[T any](events []core.Event) int {
	n := 0
	for _, e := range events {
		if _, ok := e.(T); ok {
			n++
		}
	}

	return n
}

// TestGoal_ContinuesUntilComplete: /goal starts a run at once with Codex's
// user_goal record and continuation message; the session continues when a
// run ends with the goal active, and stops when the model calls
// update_goal complete. Every request extends the one before it, with the
// same system prompt and tools, so the prompt cache holds.
func TestGoal_ContinuesUntilComplete(t *testing.T) {
	g := openGoalRun(t, "",
		fakellm.Reply{Commands: []string{"echo step one"}},
		fakellm.Reply{Text: "Step one is done; step two remains."},
		fakellm.Reply{Calls: []fakellm.Call{{Name: goal.UpdateToolName, Args: `{"status":"complete"}`}}},
		fakellm.Reply{Text: "The goal is complete."},
	)

	set, err := g.s.SetGoal("  make the build pass  ")
	require.NoError(t, err)
	assert.Equal(t, "make the build pass", set.Objective)
	events := g.idle()

	got := g.goal()
	assert.Equal(t, goal.StatusComplete, got.Status)
	assert.Equal(t, 2, got.Continuations)
	assert.Equal(t, 2, countOf[session.GoalContinued](events))
	assert.Equal(t, 2, countOf[core.RunFinished](events))
	reqs := g.llm.Requests()
	require.Len(t, reqs, 4, "no run after the goal is complete")

	first := reqs[0]
	for _, name := range goal.ToolNames {
		assert.Contains(t, first.ToolNames, name)
	}
	require.Len(t, first.UserTexts, 2)
	assert.Equal(t, goal.UserSet("make the build pass", ""), first.UserTexts[0], "the user's goal, recorded as Codex records it")
	assert.Equal(t, goal.KindContinuation, kindOf(first.UserTexts[1]))
	assert.Contains(t, first.UserTexts[1], "<objective>\nmake the build pass\n</objective>")
	assert.Contains(t, first.UserTexts[1], "- Automatic continuations: 1 of 50")
	assert.NotContains(t, first.UserTexts[1], "update_plan", "uah has no plan tool")
	assert.Equal(t, goal.KindContinuation, kindOf(reqs[2].UserTexts[len(reqs[2].UserTexts)-1]), "the second run is a continuation")
	assert.Contains(t, reqs[3].ToolOutputs[len(reqs[3].ToolOutputs)-1], `"status":"complete"`)

	for i := 1; i < len(reqs); i++ {
		assert.Equal(t, reqs[0].System, reqs[i].System)
		assert.Equal(t, reqs[0].ToolNames, reqs[i].ToolNames)
		require.GreaterOrEqual(t, len(reqs[i].Input), len(reqs[i-1].Input))
		for j, item := range reqs[i-1].Input {
			assert.JSONEq(t, string(item), string(reqs[i].Input[j]), "request %d extends request %d at item %d", i, i-1, j)
		}
	}
}

// TestGoal_StopsAtTokenBudget: the default budget ([goals]
// max_goal_token_budget) runs out mid-run; the goal is budget-limited, the
// model gets Codex's budget message once its tool call finishes, and
// nothing continues.
func TestGoal_StopsAtTokenBudget(t *testing.T) {
	g := openGoalRun(t, "[goals]\nmax_goal_token_budget = 250\n",
		fakellm.Reply{Commands: []string{"echo one"}},
		fakellm.Reply{Commands: []string{"echo two"}},
	) // then text only

	_, err := g.s.SetGoal("count to ten")
	require.NoError(t, err)
	events := g.idle()

	got := g.goal()
	assert.Equal(t, goal.StatusBudgetLimited, got.Status)
	assert.Equal(t, "used its token budget", got.Reason)
	assert.Equal(t, int64(250), got.TokenBudget)
	assert.Equal(t, int64(110+210), got.TokensUsed, "uncached input plus output, per response, until the limit")
	assert.Equal(t, 1, countOf[session.GoalContinued](events), "no continuation after the budget")
	reqs := g.llm.Requests()
	require.Len(t, reqs, 3, "the budget message cancels no request")
	dev := reqs[2].DeveloperTexts
	require.NotEmpty(t, dev)
	assert.Equal(t, goal.KindBudgetLimit, kindOf(dev[len(dev)-1]), "the run's next request tells the model to wrap up")
	assert.Contains(t, dev[len(dev)-1], "- Token budget: 250")
	assert.Len(t, reqs[2].ToolOutputs, 2, "with the second command's output")
}

// TestGoal_StopsAtContinuationCap: [goals] max_continuations stops a goal
// that keeps working without finishing.
func TestGoal_StopsAtContinuationCap(t *testing.T) {
	g := openGoalRun(t, "[goals]\nmax_continuations = 2\n",
		fakellm.Reply{Commands: []string{"echo a"}}, fakellm.Reply{Text: "more to do"},
		fakellm.Reply{Commands: []string{"echo b"}}, fakellm.Reply{Text: "still more"},
	)

	_, err := g.s.SetGoal("never finish")
	require.NoError(t, err)
	events := g.idle()

	got := g.goal()
	assert.Equal(t, goal.StatusBudgetLimited, got.Status)
	assert.Equal(t, 2, got.Continuations)
	assert.Equal(t, "used its 2 automatic continuations", got.Reason)
	assert.Len(t, g.llm.Requests(), 4)
	assert.Equal(t, 2, countOf[session.GoalContinued](events))
}

// TestGoal_StopsWithoutProgress: three automatic turns in a row without a
// tool call stall the goal, as Codex stalls it after empty turns.
func TestGoal_StopsWithoutProgress(t *testing.T) {
	g := openGoalRun(t, "") // every reply is text only

	_, err := g.s.SetGoal("wait for something")
	require.NoError(t, err)
	g.idle()

	got := g.goal()
	assert.Equal(t, goal.StatusBlocked, got.Status)
	assert.Equal(t, "3 automatic turns in a row made no tool call", got.Reason)
	assert.Len(t, g.llm.Requests(), 3)
}

// TestGoal_FailedRunStalls: a run that fails stalls the goal instead of
// continuing into the same failure.
func TestGoal_FailedRunStalls(t *testing.T) {
	g := openGoalRun(t, "", fakellm.Reply{Fail: 400, FailCode: "invalid_request_error"})

	_, err := g.s.SetGoal("anything")
	require.NoError(t, err)
	g.idle()

	got := g.goal()
	assert.Equal(t, goal.StatusBlocked, got.Status)
	assert.Equal(t, "the run failed", got.Reason)
	assert.Len(t, g.llm.Requests(), 1)
}

// TestGoal_InterruptPausesAndResumeKeepsIt: an interrupt pauses the goal
// and nothing continues; the goal survives a restart; /goal resume
// continues it, recorded for the model; /clear removes it.
func TestGoal_InterruptPausesAndResumeKeepsIt(t *testing.T) {
	gate := make(chan struct{})
	g := openGoalRun(t, "",
		fakellm.Reply{Gate: gate},
		fakellm.Reply{Calls: []fakellm.Call{{Name: goal.UpdateToolName, Args: `{"status":"complete"}`}}},
		fakellm.Reply{Text: "done"},
	)
	_, err := g.s.SetGoal("refactor the parser")
	require.NoError(t, err)
	<-g.llm.Seen()
	require.NoError(t, g.s.Interrupt())
	g.idle()
	close(gate)

	paused := g.goal()
	assert.Equal(t, goal.StatusPaused, paused.Status)
	assert.Equal(t, "interrupted", paused.Reason)
	assert.Len(t, g.llm.Requests(), 1, "an interrupt stops the goal")

	id := g.s.ID()
	require.NoError(t, g.s.Close())
	g.open(id)
	restored := g.goal()
	assert.Equal(t, goal.StatusPaused, restored.Status)
	assert.Equal(t, paused.ID, restored.ID)
	assert.Equal(t, "refactor the parser", restored.Objective)
	var opened []core.Event
	for e := range g.s.Events() {
		opened = append(opened, e)
		if u, ok := e.(session.GoalUpdated); ok {
			assert.Equal(t, session.GoalRestored, u.Change)

			break
		}
	}
	require.NotEmpty(t, opened)

	_, err = g.s.ResumeGoal()
	require.NoError(t, err)
	g.idle()
	assert.Equal(t, goal.StatusComplete, g.goal().Status)
	reqs := g.llm.Requests()
	require.Len(t, reqs, 3)
	resumed := reqs[1].UserTexts
	assert.Contains(t, resumed, goal.UserSet("", goal.StatusActive), "the resume is recorded for the model")
	assert.Equal(t, goal.KindContinuation, kindOf(resumed[len(resumed)-1]))
	assert.Contains(t, resumed[0], "refactor the parser", "the resumed session's history keeps the goal")

	require.NoError(t, g.s.Clear())
	_, ok := g.s.Goal()
	assert.False(t, ok, "/clear removes the goal")
}

// TestGoal_ModelCreatesGoal: create_goal makes the run that called it a
// goal run, so the session continues when it ends; the model's goal gets
// the default budget, and a second create_goal is refused while it is
// unfinished.
func TestGoal_ModelCreatesGoal(t *testing.T) {
	g := openGoalRun(t, "[goals]\nmax_goal_token_budget = 100000\n",
		fakellm.Reply{Calls: []fakellm.Call{{Name: goal.CreateToolName, Args: `{"objective":"ship the release"}`}}},
		fakellm.Reply{Calls: []fakellm.Call{{Name: goal.CreateToolName, Args: `{"objective":"another"}`}}},
		fakellm.Reply{Text: "Started on the release."},
		fakellm.Reply{Calls: []fakellm.Call{{Name: goal.GetToolName, Args: `{}`}}},
		fakellm.Reply{Calls: []fakellm.Call{{Name: goal.UpdateToolName, Args: `{"status":"complete"}`}}},
		fakellm.Reply{Text: "Released."},
	)

	_, err := g.s.Submit("set a goal to ship the release")
	require.NoError(t, err)
	g.idle()

	got := g.goal()
	assert.Equal(t, goal.StatusComplete, got.Status)
	assert.Equal(t, "ship the release", got.Objective)
	assert.Equal(t, int64(100_000), got.TokenBudget)
	assert.Equal(t, 1, got.Continuations)
	reqs := g.llm.Requests()
	require.Len(t, reqs, 6)
	assert.Contains(t, reqs[2].ToolOutputs[1], goal.ErrUnfinished)
	assert.Contains(t, reqs[4].ToolOutputs[2], `"objective":"ship the release"`)
	assert.Contains(t, reqs[5].ToolOutputs[3], "completionBudgetReport")
	for _, text := range reqs[0].UserTexts {
		assert.NotEqual(t, goal.KindUser, kindOf(text), "a goal the model created is not the user's")
	}
}

func kindOf(text string) goal.Kind { kind, _ := goal.Parse(text); return kind }

// TestGoal_Off: [features] goals = false offers no goal tools and refuses
// /goal.
func TestGoal_Off(t *testing.T) {
	g := openGoalRun(t, "[features]\ngoals = false\n")
	_, err := g.s.SetGoal("anything")
	require.ErrorIs(t, err, session.ErrGoalsOff)
	_, err = g.s.Submit("hi")
	require.NoError(t, err)
	g.idle()
	for _, name := range goal.ToolNames {
		assert.NotContains(t, g.llm.Requests()[0].ToolNames, name)
	}
	assert.False(t, bytes.Contains(g.llm.Requests()[0].ToolDefs[0], []byte("goal")))
	assert.False(t, strings.Contains(g.llm.Requests()[0].System, "update_goal"))
}

// TestGoal_EditWhileRunning: /goal edit during a goal run tells the model
// after its tool call, with Codex's objective_updated message, and keeps
// the goal's usage.
func TestGoal_EditWhileRunning(t *testing.T) {
	gate := make(chan struct{})
	g := openGoalRun(t, "",
		fakellm.Reply{Commands: []string{"echo a"}, Gate: gate},
		fakellm.Reply{Calls: []fakellm.Call{{Name: goal.UpdateToolName, Args: `{"status":"complete"}`}}},
		fakellm.Reply{Text: "done"},
	)
	set, err := g.s.SetGoal("ship v1")
	require.NoError(t, err)
	<-g.llm.Seen()
	edited, err := g.s.EditGoal("ship v2")
	require.NoError(t, err)
	assert.Equal(t, set.ID, edited.ID)
	assert.Equal(t, goal.StatusActive, edited.Status)
	close(gate)
	g.idle()

	got := g.goal()
	assert.Equal(t, "ship v2", got.Objective)
	assert.Equal(t, goal.StatusComplete, got.Status)
	var told bool
	for _, r := range g.llm.Requests() {
		for _, text := range r.DeveloperTexts {
			told = told || (kindOf(text) == goal.KindObjectiveUpdated && strings.Contains(text, "<untrusted_objective>\nship v2\n</untrusted_objective>"))
		}
	}
	assert.True(t, told, "the running model was told the objective changed")
	_, err = g.s.SetGoal("another")
	require.NoError(t, err, "a complete goal is replaced without clearing")
	g.idle()
}
