package goal_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/goal"
)

func active() goal.Goal {
	at := time.Unix(1_700_000_000, 0).UTC()

	return goal.Goal{ID: "g1", Objective: "fix <the> build & ship", Status: goal.StatusActive, CreatedAt: at, UpdatedAt: at}
}

// TestContinuation: Codex's continuation.md, verbatim but for the plan
// paragraph and uah's continuation line, in Codex's hidden-context wrapper,
// with the objective escaped as XML text.
func TestContinuation(t *testing.T) {
	g := active()
	g.TokenBudget, g.TokensUsed, g.Continuations, g.MaxContinuations = 50_000, 12_500, 3, 50
	text := goal.Continuation(g)

	require.True(t, strings.HasPrefix(text, "<codex_internal_context source=\"goal\">\nContinue working toward the active thread goal.\n"))
	assert.True(t, strings.HasSuffix(text, "\n</codex_internal_context>"))
	assert.Contains(t, text, "<objective>\nfix &lt;the&gt; build &amp; ship\n</objective>")
	assert.Contains(t, text, "- Tokens used: 12500\n- Token budget: 50000\n- Tokens remaining: 37500\n- Automatic continuations: 3 of 50\n")
	assert.NotContains(t, text, "update_plan")
	assert.NotContains(t, text, "Progress visibility")
	assert.NotContains(t, text, "{{")

	src, err := os.ReadFile("prompts/continuation.md")
	require.NoError(t, err)
	for _, line := range strings.Split(string(src), "\n") {
		if strings.Contains(line, "{{") || strings.Contains(line, "update_plan") || line == "Progress visibility:" {
			continue
		}
		assert.Contains(t, text, line, "Codex's line is kept")
	}

	unbounded := goal.Continuation(active())
	assert.Contains(t, unbounded, "- Token budget: none\n- Tokens remaining: unbounded\n")
	assert.NotContains(t, unbounded, "Automatic continuations")
}

func TestBudgetLimitAndObjectiveUpdated(t *testing.T) {
	g := active()
	g.TokenBudget, g.TokensUsed, g.TimeUsedSeconds = 250, 320, 42
	budget := goal.BudgetLimit(g)
	assert.Contains(t, budget, "The active thread goal has reached its token budget.")
	assert.Contains(t, budget, "- Time spent pursuing goal: 42 seconds\n- Tokens used: 320\n- Token budget: 250\n")

	updated := goal.ObjectiveUpdated(active())
	assert.Contains(t, updated, "<untrusted_objective>\nfix &lt;the&gt; build &amp; ship\n</untrusted_objective>")
	assert.Contains(t, updated, "- Tokens remaining: unknown")
}

// TestUserGoal: Codex's user_goal record: the objective as a JSON string,
// omitted whole past 700 bytes, the status, and the clear.
func TestUserGoal(t *testing.T) {
	assert.Equal(t, "<codex_internal_context source=\"user_goal\">\nUser set the goal: \"say \\\"hi\\\" <now>\"\n</codex_internal_context>", goal.UserSet(`say "hi" <now>`, ""))
	assert.Equal(t, "<codex_internal_context source=\"user_goal\">\nUser set goal status: \"paused\".\n</codex_internal_context>", goal.UserSet("", goal.StatusPaused))
	assert.Contains(t, goal.UserSet(strings.Repeat("x", 699), ""), "[objective omitted; exceeds the evidence limit]")
	assert.Equal(t, "<codex_internal_context source=\"user_goal\">\nUser cleared the goal.\n</codex_internal_context>", goal.UserCleared())
}

func TestParse(t *testing.T) {
	g := active()
	for _, tc := range []struct {
		text string
		want goal.Kind
	}{
		{goal.Continuation(g), goal.KindContinuation},
		{goal.BudgetLimit(g), goal.KindBudgetLimit},
		{goal.ObjectiveUpdated(g), goal.KindObjectiveUpdated},
		{goal.UserSet("x", ""), goal.KindUser},
		{goal.UserCleared(), goal.KindUser},
		{"  " + goal.UserCleared() + "\n", goal.KindUser},
		{"fix the build", goal.KindNone},
		{`<codex_internal_context source="other">x</codex_internal_context>`, goal.KindNone},
		{`<codex_internal_context source="goal">unterminated`, goal.KindNone},
	} {
		kind, _ := goal.Parse(tc.text)
		assert.Equal(t, tc.want, kind, tc.text)
		assert.Equal(t, tc.want != goal.KindNone, goal.IsContext(tc.text))
	}
	_, body := goal.Parse(goal.UserSet("x", goal.StatusActive))
	assert.Equal(t, "User set the goal: \"x\"\nUser set goal status: \"active\".", body)
}

func TestChecks(t *testing.T) {
	got, err := goal.CheckObjective("  ship it \n")
	require.NoError(t, err)
	assert.Equal(t, "ship it", got)
	_, err = goal.CheckObjective(" \n")
	require.EqualError(t, err, "goal objective must not be empty")
	_, err = goal.CheckObjective(strings.Repeat("é", goal.MaxObjectiveChars+1))
	require.EqualError(t, err, "goal objective must be at most 4000 characters")
	_, err = goal.CheckObjective(strings.Repeat("é", goal.MaxObjectiveChars))
	require.NoError(t, err)

	require.NoError(t, goal.CheckBudget(0, 100))
	require.NoError(t, goal.CheckBudget(100, 100))
	require.EqualError(t, goal.CheckBudget(-1, 0), "goal budgets must be positive when provided")
	require.EqualError(t, goal.CheckBudget(101, 100), "goal token budget 101 exceeds the maximum allowed goal token budget of 100")

	assert.Equal(t, int64(150), goal.TokenDelta(200, 60, 10))
	assert.Equal(t, int64(10), goal.TokenDelta(50, 80, 10))
}

// TestTools: the three tools with Codex's names, and schemas that parse.
func TestTools(t *testing.T) {
	tools := goal.Tools()
	require.Len(t, tools, 3)
	for i, tool := range tools {
		assert.Equal(t, goal.ToolNames[i], tool.Name)
		var schema map[string]any
		require.NoError(t, json.Unmarshal([]byte(tool.Schema), &schema), tool.Name)
		assert.Equal(t, "object", schema["type"])
	}
	assert.Contains(t, tools[2].Schema, `"enum":["complete","blocked","paused"]`)
	assert.True(t, strings.HasPrefix(tools[1].Description, "Create a goal only when explicitly requested"))

	args, err := goal.ParseArgs[goal.CreateArgs](`{"objective":"x","token_budget":5}`)
	require.NoError(t, err)
	assert.Equal(t, int64(5), *args.TokenBudget)
	_, err = goal.ParseArgs[goal.UpdateArgs](`{"status":`)
	require.Error(t, err)
}

// TestResult: Codex's GoalToolResponse, with null for what is missing.
func TestResult(t *testing.T) {
	assert.JSONEq(t, `{"goal":null,"remainingTokens":null,"completionBudgetReport":null}`, goal.Result(nil, "s1", false))

	g := active()
	g.Objective = "x"
	assert.JSONEq(t, `{"goal":{"threadId":"s1","objective":"x","status":"active","tokensUsed":0,"timeUsedSeconds":0,"createdAt":1700000000,"updatedAt":1700000000},"remainingTokens":null,"completionBudgetReport":null}`, goal.Result(&g, "s1", false))

	g.Status, g.TokenBudget, g.TokensUsed = goal.StatusComplete, 100, 120
	var out struct {
		Goal struct {
			TokenBudget int64 `json:"tokenBudget"`
		} `json:"goal"`
		RemainingTokens        int64  `json:"remainingTokens"`
		CompletionBudgetReport string `json:"completionBudgetReport"`
	}
	require.NoError(t, json.Unmarshal([]byte(goal.Result(&g, "s1", true)), &out))
	assert.Equal(t, int64(100), out.Goal.TokenBudget)
	assert.Equal(t, int64(0), out.RemainingTokens)
	assert.True(t, strings.HasPrefix(out.CompletionBudgetReport, "Goal achieved. Report final usage"))
}

// TestDisplay: Codex's footer words, compact time, and compact tokens.
func TestDisplay(t *testing.T) {
	g := active()
	g.TimeUsedSeconds = 60
	assert.Equal(t, "Pursuing goal (2m)", goal.Indicator(g, 60))
	g.TokenBudget, g.TokensUsed = 50_000, 12_500
	assert.Equal(t, "Pursuing goal (12.5K / 50K)", goal.Indicator(g, 0))
	g.Status = goal.StatusPaused
	assert.Equal(t, "Goal paused (/goal resume)", goal.Indicator(g, 0))
	g.Status = goal.StatusBlocked
	assert.Equal(t, "Goal stalled (/goal resume)", goal.Indicator(g, 0))
	g.Status, g.TokensUsed = goal.StatusBudgetLimited, 63_876
	assert.Equal(t, "Goal unmet (63.9K / 50K tokens)", goal.Indicator(g, 0))
	g.TokenBudget, g.Continuations, g.MaxContinuations = 0, 50, 50
	assert.Equal(t, "Goal unmet (50 / 50 continuations)", goal.Indicator(g, 0))
	g.Status, g.TimeUsedSeconds = goal.StatusComplete, 36_720
	assert.Equal(t, "Goal achieved (10h 12m)", goal.Indicator(g, 0))

	for seconds, want := range map[int64]string{0: "0s", 59: "59s", 60: "1m", 90 * 60: "1h 30m", 7200: "2h", 86_399: "23h 59m", 86_400: "1d 0h 0m"} {
		assert.Equal(t, want, goal.Elapsed(seconds))
	}
	for n, want := range map[int64]string{950: "950", 12_500: "12.5K", 50_000: "50K", 1_200_000: "1.2M"} {
		assert.Equal(t, want, goal.Tokens(n))
	}
	g.Reason = "used its 50 automatic continuations"
	assert.Equal(t, "Goal complete · Objective: fix <the> build & ship Time: 10h 12m. Stopped: used its 50 automatic continuations.", goal.Line(g))
	assert.Contains(t, goal.Summary(g), "Automatic continuations: 50 of 50")
}
