package state_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/codereview"
	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/gitdiff"
	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/internal/tui/state"
)

var targets = state.ReviewTargetsLoaded{
	Branches: gitdiff.Branches{Names: []string{"main", "feature", "fix-login"}, Current: "feature"},
	Commits: []gitdiff.Commit{
		{SHA: "aaaaaaa1111111", Subject: "Add the parser"},
		{SHA: "bbbbbbb2222222", Subject: "Fix the login"},
	},
}

func TestDiff_Command(t *testing.T) {
	_, effects := apply(opened(), state.Submit{Text: "/diff"})
	assert.Equal(t, []state.Effect{state.EffDiff{Dir: "/workspace"}}, effects)

	t.Run("changes become an item the agent never sees", func(t *testing.T) {
		d := gitdiff.Diff{Root: "/workspace", Files: []gitdiff.File{{Untracked: true}}}
		s, effects := apply(opened(), state.DiffShown{Diff: d})
		assert.Empty(t, effects)
		require.Equal(t, []state.Kind{state.KindDiff}, kinds(s))
		assert.Equal(t, &d, s.Items[0].GitDiff)
	})

	notices := map[string]state.DiffShown{
		"/diff — not inside a git repository": {Err: gitdiff.ErrNotRepo},
		"No changes detected.":                {},
		"Failed to compute diff: boom":        {Err: errors.New("boom")},
	}
	for text, shown := range notices {
		s, _ := apply(opened(), shown)
		require.Len(t, s.Items, 1)
		assert.Equal(t, text, s.Items[0].Text)
	}
}

// TestReview_Menu is Codex's review popup as the command menu: the
// presets, then the branches or the commits, loaded once.
func TestReview_Menu(t *testing.T) {
	s := opened()
	assert.Equal(t, []string{"branch", "uncommitted", "commit", "<instructions>"}, labels(s.Suggestions("/review ")))
	assert.Equal(t, []string{"uncommitted"}, labels(s.Suggestions("/review un")))
	assert.Empty(t, s.Suggestions("/review check the error handling"), "instructions have no menu, so enter sends them")

	s, effects := apply(s, state.DraftChanged{Draft: "/review "})
	assert.Equal(t, []state.Effect{state.EffLoadReviewTargets{Dir: "/workspace"}}, effects)
	_, effects = apply(s, state.DraftChanged{Draft: "/review b"})
	assert.Empty(t, effects, "one load at a time")
	assert.Empty(t, s.Suggestions("/review branch "), "nothing until the lists arrive")

	s, _ = apply(s, targets)
	branches := s.Suggestions("/review branch ")
	assert.Equal(t, []string{"main", "feature", "fix-login"}, labels(branches))
	assert.Equal(t, "checked out", branches[1].Help)
	assert.Equal(t, []string{"feature", "fix-login"}, labels(s.Suggestions("/review branch f")))

	commits := s.Suggestions("/review commit ")
	assert.Equal(t, []string{"Add the parser", "Fix the login"}, labels(commits))
	assert.Equal(t, "aaaaaaa", commits[0].Help)
	assert.Equal(t, "/review commit aaaaaaa1111111", commits[0].Draft)
	assert.Equal(t, []string{"Fix the login"}, labels(s.Suggestions("/review commit login")))
	assert.Equal(t, []string{"Fix the login"}, labels(s.Suggestions("/review commit bbb")))

	t.Run("accepting a preset opens its list", func(t *testing.T) {
		_, effects := apply(s, state.MenuEnter{Draft: "/review "})
		assert.Equal(t, []state.Effect{state.EffSetDraft{Text: "/review branch "}}, effects)
	})
}

func TestReview_Targets(t *testing.T) {
	s, _ := apply(opened(), targets)
	cases := map[string]codereview.Target{
		"/review uncommitted":             {Kind: codereview.Uncommitted},
		"/review branch main":             {Kind: codereview.BaseBranch, Branch: "main"},
		"/review commit bbbbbbb":          {Kind: codereview.Commit, SHA: "bbbbbbb2222222", Title: "Fix the login"},
		"/review commit 1234abc":          {Kind: codereview.Commit, SHA: "1234abc"},
		"/review look at the error paths": {Kind: codereview.Custom, Instructions: "look at the error paths"},
	}
	for text, want := range cases {
		got, effects := apply(s, state.Submit{Text: text})
		assert.Equal(t, []state.Effect{state.EffReview{Target: want}}, effects, text)
		assert.Nil(t, got.Menu.Review, "the lists are read again for the next review")
	}

	for _, text := range []string{"/review", "/review branch", "/review commit"} {
		_, effects := apply(s, state.Submit{Text: text})
		require.NotEmpty(t, effects, text)
		assert.IsType(t, state.EffSetDraft{}, effects[0], "an unfinished target goes back to the menu")
	}

	busy, _ := apply(s, session.InputQueued{At: t0, Input: core.UserInput{ID: "m1", Text: "work"}})
	_, effects := apply(busy, state.Submit{Text: "/review uncommitted"})
	assert.Empty(t, effects, "as in Codex, /review waits until the agent is idle")
}

// TestReview_Item follows a review from its start to its findings; esc
// esc stops it, and a second review waits.
func TestReview_Item(t *testing.T) {
	s, _ := apply(opened(), session.ReviewStarted{At: t0, ID: "r1", Hint: "current changes"})
	require.Equal(t, []state.Kind{state.KindReview}, kinds(s))
	it := s.Items[0]
	assert.True(t, it.Live())
	assert.Equal(t, "current changes", it.Review.Hint)
	assert.True(t, s.ReviewRunning())

	s, _ = apply(s, session.ReviewActivity{At: t0, ID: "r1", Event: core.ToolCalled{At: t0, CallID: "c1", Name: "Bash", Label: "git diff", Arguments: `{"command":"git diff"}`}})
	require.Len(t, s.Items[0].Review.Steps, 1)
	assert.Equal(t, "git diff", s.Items[0].Review.Steps[0].Command)

	_, effects := apply(s, state.Submit{Text: "/review uncommitted"})
	assert.Empty(t, effects, "one review at a time")

	armed, _ := apply(s, state.Esc{})
	_, effects = apply(armed, state.Esc{})
	assert.Equal(t, []state.Effect{state.EffInterrupt{}}, effects, "esc esc stops the review")

	out := codereview.Parse(`{"findings":[{"title":"[P1] x","body":"y","code_location":{"absolute_file_path":"/workspace/a.go","line_range":{"start":1,"end":2}}}]}`)
	s, _ = apply(s, session.ReviewFinished{At: t0.Add(time.Minute), ID: "r1", Output: out})
	it = s.Items[0]
	assert.False(t, it.Live())
	assert.False(t, s.ReviewRunning())
	assert.Equal(t, out, it.Review.Output)
	assert.Equal(t, state.ToolStopped, it.Review.Steps[0].Tool, "a call the review left running is stopped")
	assert.Equal(t, "/workspace", it.Review.Workspace)
}

// TestReview_Steps keeps the reviewer's steps for the detailed view:
// each tool call shaped as the session's own, its start and end, at most
// the latest 100 and any still running, with the count of all, the tokens of its model
// responses so far, and a copy per change, so an earlier Review keeps
// its own steps.
func TestReview_Steps(t *testing.T) {
	act := func(e core.Event) session.ReviewActivity { return session.ReviewActivity{At: t0, ID: "r1", Event: e} }
	s, _ := apply(opened(), session.ReviewStarted{At: t0, ID: "r1", Hint: "current changes"})
	s, _ = apply(s,
		act(core.ToolCalled{At: t0, CallID: "c0", Name: "Bash", Arguments: `{"command":"sed -n 1,40p /workspace/a.go"}`}),
		act(core.ToolStarted{At: t0.Add(time.Second), CallID: "c0"}),
	)
	first := s.Items[0].Review
	require.Len(t, first.Steps, 1)
	step := first.Steps[0]
	assert.Equal(t, "READ", step.Verb, "shaped as the session's own commands")
	assert.Equal(t, state.ToolRunning, step.Tool)

	s, _ = apply(s,
		act(core.ToolFinished{At: t0, CallID: "c0", OK: false, Detail: "exit 2", Duration: 3 * time.Second}),
		act(core.ModelResponded{At: t0, Usage: core.Tokens{InputTokens: 1_000, OutputTokens: 50}}),
		act(core.ModelResponded{At: t0, Usage: core.Tokens{InputTokens: 2_000, OutputTokens: 70}}),
	)
	r := s.Items[0].Review
	assert.Equal(t, state.ToolFailed, r.Steps[0].Tool)
	assert.Equal(t, "exit 2", r.Steps[0].Detail)
	assert.Equal(t, 3*time.Second, r.Steps[0].Duration)
	s, _ = apply(s, act(engine.ToolOutput{At: t0, CallID: "c0", Output: "sed: a.go: No such file\n\n"}))
	r = s.Items[0].Review
	assert.Equal(t, "sed: a.go: No such file", r.Steps[0].ErrorLine, "why it failed, from its output")
	assert.Equal(t, core.Tokens{InputTokens: 3_000, OutputTokens: 120}, r.Tokens, "the tokens so far")
	assert.Equal(t, state.ToolRunning, first.Steps[0].Tool, "an earlier Review keeps its own steps")

	for i := 1; i <= 130; i++ {
		s, _ = apply(s, act(core.ToolCalled{At: t0, CallID: fmt.Sprintf("c%d", i), Name: "Bash", Arguments: fmt.Sprintf(`{"command":"echo %d"}`, i)}))
	}
	r = s.Items[0].Review
	assert.Len(t, r.Steps, 100, "bounded")
	assert.Equal(t, 131, r.StepCount, "every call counts")
	assert.Equal(t, "call:c31", r.Steps[0].Key, "the latest are kept")
	assert.Equal(t, "call:c130", r.Steps[99].Key)

	slow, _ := apply(opened(), session.ReviewStarted{At: t0, ID: "r2", Hint: "current changes"},
		session.ReviewActivity{At: t0, ID: "r2", Event: core.ToolCalled{At: t0, CallID: "slow", Name: "Bash", Arguments: `{"command":"sleep 60"}`}})
	for i := range 120 {
		id := fmt.Sprintf("q%d", i)
		slow, _ = apply(slow,
			session.ReviewActivity{At: t0, ID: "r2", Event: core.ToolCalled{At: t0, CallID: id, Name: "Bash", Arguments: `{"command":"true"}`}},
			session.ReviewActivity{At: t0, ID: "r2", Event: core.ToolFinished{At: t0, CallID: id, OK: true}})
	}
	r2 := slow.Items[0].Review
	require.Len(t, r2.Steps, 100)
	assert.Equal(t, "call:slow", r2.Steps[0].Key, "a call still running is never dropped")
	assert.Equal(t, "call:q21", r2.Steps[1].Key, "the oldest finished ones go")
	slow, _ = apply(slow, session.ReviewActivity{At: t0, ID: "r2", Event: core.ToolFinished{At: t0, CallID: "slow", OK: true}})
	assert.Equal(t, state.ToolOK, slow.Items[0].Review.Steps[0].Tool, "and its end finds it")

	before := s.Items[0].Review
	ended, _ := apply(s, session.ReviewFinished{At: t0.Add(time.Minute), ID: "r1", Interrupted: true})
	r = ended.Items[0].Review
	assert.Equal(t, core.Tokens{InputTokens: 3_000, OutputTokens: 120}, r.Tokens, "without the run's total, the sum so far stays")
	assert.Equal(t, state.ToolStopped, r.Steps[99].Tool, "the review stopped its running call")
	assert.Equal(t, state.ToolCalled, before.Steps[99].Tool, "and the earlier Review is unchanged")

	done, _ := apply(s, session.ReviewFinished{At: t0.Add(time.Minute), ID: "r1", Tokens: core.Tokens{InputTokens: 9_000, OutputTokens: 300}})
	assert.Equal(t, core.Tokens{InputTokens: 9_000, OutputTokens: 300}, done.Items[0].Review.Tokens, "the run's total wins")
	assert.Len(t, done.Items[0].Review.Steps, 100, "the steps stay after the review")
}

// TestReview_HandOverIsANote shows the message that gave the review to
// the main agent as a line, not as the user's message.
func TestReview_HandOverIsANote(t *testing.T) {
	text := codereview.ExitMessage(codereview.Output{OverallExplanation: "Fine."}, false)
	s, _ := apply(opened(), core.UserMessage{At: t0, ID: "r1", Text: text})
	require.Equal(t, []state.Kind{state.KindNotice}, kinds(s))
	assert.Equal(t, "the review went to the main agent with this message", s.Items[0].Text)
}

// TestReview_WaitingAndLimit says what a running review waits on, once a
// step has run for a minute, and which limit stopped a review.
func TestReview_WaitingAndLimit(t *testing.T) {
	act := func(e core.Event) session.ReviewActivity { return session.ReviewActivity{At: t0, ID: "r1", Event: e} }
	s, _ := apply(opened(), session.ReviewStarted{At: t0, ID: "r1", Hint: "current changes"},
		act(core.ToolCalled{At: t0, CallID: "quick", Name: "Bash", Arguments: `{"command":"git status"}`}),
		act(core.ToolStarted{At: t0, CallID: "quick"}),
		act(core.ToolFinished{At: t0, CallID: "quick", OK: true}),
		act(core.ToolCalled{At: t0, CallID: "install", Name: "Bash", Arguments: `{"command":"pnpm install --offline --frozen-lockfile --ignore-scripts"}`}),
		act(core.ToolStarted{At: t0, CallID: "install"}),
	)
	r := s.Items[0].Review
	assert.Empty(t, r.WaitingOn(t0.Add(59*time.Second)), "not for a minute")
	assert.Equal(t, "waiting on: pnpm install --offline --frozen-lockfil… (12m)", r.WaitingOn(t0.Add(12*time.Minute+30*time.Second)))
	assert.Empty(t, r.LimitNote())

	s, _ = apply(s, session.ReviewFinished{At: t0.Add(30 * time.Minute), ID: "r1", Limit: session.ReviewTimeLimit})
	r = s.Items[0].Review
	assert.Empty(t, r.WaitingOn(t0.Add(40*time.Minute)), "a review that ended waits on nothing")
	assert.Equal(t, "stopped at the time limit", r.LimitNote())
}
