package review_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/viktordanov/uah-core/harness/llm"
	"github.com/viktordanov/uah-core/harness/llm/clients/openai"

	"github.com/viktordanov/uah/internal/review"
	"github.com/viktordanov/uah/testing/fakellm"
)

func client(t *testing.T, srv *fakellm.Server) llm.Adapter {
	t.Helper()
	attempts := 1
	c, err := openai.NewClient(openai.Config{APIKey: "test-key", BaseURL: srv.URL, MaxAttempts: &attempts})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	return c
}

func newReviewer(t *testing.T, replies ...fakellm.Reply) (*review.Reviewer, *fakellm.Server) {
	t.Helper()
	srv := fakellm.New(t, replies...)

	return review.New(client(t, srv), review.Config{Model: "codex-auto-review", Effort: llm.ReasoningEffortLow}), srv
}

func sample() review.Request {
	return review.Request{
		Action: review.Action{
			Tool: "Bash", Command: "git push origin feature", Cwd: "/ws", SandboxMode: "workspace-write",
			SandboxPermissions: "require_escalated", Justification: "Push the fix the user asked for.",
		},
		Transcript: review.Transcript{Entries: []review.Entry{
			{Kind: review.EntryUser, Text: "Fix the typo and push it to my feature branch."},
			{Kind: review.EntryCall, Tool: "Bash", Text: `{"command":"git commit -am typo"}`},
			{Kind: review.EntryResult, Tool: "Bash", Text: "exit 0"},
		}},
		SessionID: "s1",
	}
}

func TestReview_Allow(t *testing.T) {
	r, srv := newReviewer(t, fakellm.Reply{Text: `{"outcome":"allow"}`})

	v, err := r.Review(context.Background(), sample())

	require.NoError(t, err)
	assert.Equal(t, review.Allow, v.Outcome)
	assert.Equal(t, review.RiskLow, v.Risk)
	assert.False(t, v.Failed)
	assert.Equal(t, int64(100), v.Usage.InputTokens)
	reqs := srv.Requests()
	require.Len(t, reqs, 1)
	assert.Equal(t, "codex-auto-review", reqs[0].Model)
	assert.Equal(t, "low", reqs[0].Effort)
	assert.Empty(t, reqs[0].Tools)
	assert.Contains(t, reqs[0].System, "You are judging one planned coding-agent action.")
	require.Len(t, reqs[0].UserTexts, 1)
	assert.Contains(t, reqs[0].UserTexts[0], "git push origin feature")
}

func TestReview_Deny(t *testing.T) {
	r, _ := newReviewer(t, fakellm.Reply{
		Text: `{"risk_level":"critical","user_authorization":"unknown","outcome":"deny","rationale":"Sends ~/.ssh to an unknown host."}`,
	})

	v, err := r.Review(context.Background(), sample())

	require.NoError(t, err)
	assert.Equal(t, review.Verdict{
		Outcome: review.Deny, Risk: review.RiskCritical, Authorization: "unknown",
		Reason: "Sends ~/.ssh to an unknown host.", Usage: v.Usage,
	}, v)
}

func TestReview_MalformedAnswerFailsClosed(t *testing.T) {
	r, srv := newReviewer(t, fakellm.Reply{Text: "looks fine to me"}, fakellm.Reply{Text: `{"outcome":"maybe"}`}, fakellm.Reply{Text: `{"outcome":`})

	v, err := r.Review(context.Background(), sample())

	require.NoError(t, err)
	assert.Equal(t, review.Deny, v.Outcome)
	assert.True(t, v.Failed)
	assert.Contains(t, v.Reason, "auto-review failed")
	assert.Len(t, srv.Requests(), 3, "a bad answer is retried, as in Codex")
}

func TestReview_RetriesABadAnswer(t *testing.T) {
	r, srv := newReviewer(t, fakellm.Reply{Text: "allow"}, fakellm.Reply{Text: "```json\n{\"outcome\":\"allow\"}\n```"})

	v, err := r.Review(context.Background(), sample())

	require.NoError(t, err)
	assert.Equal(t, review.Allow, v.Outcome)
	assert.Len(t, srv.Requests(), 2)
	assert.Equal(t, int64(100+200), v.Usage.InputTokens, "usage adds up over attempts")
}

func TestReview_TimeoutFailsClosed(t *testing.T) {
	gate := make(chan struct{})
	t.Cleanup(func() { close(gate) })
	srv := fakellm.New(t, fakellm.Reply{Text: `{"outcome":"allow"}`, Gate: gate})
	r := review.New(client(t, srv), review.Config{Model: "m", Timeout: 200 * time.Millisecond})
	start := time.Now()

	v, err := r.Review(context.Background(), sample())

	require.NoError(t, err)
	assert.Less(t, time.Since(start), 10*time.Second)
	assert.Equal(t, review.Deny, v.Outcome)
	assert.True(t, v.Failed)
	assert.Contains(t, v.Reason, "did not finish before its deadline")
}

func TestReview_CancelledIsAnError(t *testing.T) {
	gate := make(chan struct{})
	t.Cleanup(func() { close(gate) })
	r, srv := newReviewer(t, fakellm.Reply{Text: `{"outcome":"allow"}`, Gate: gate})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-srv.Seen()
		cancel()
	}()

	_, err := r.Review(ctx, sample())

	require.ErrorIs(t, err, context.Canceled)
}

func TestReview_BreakerAsksTheUserAfterThreeDenials(t *testing.T) {
	deny := fakellm.Reply{Text: `{"outcome":"deny","risk_level":"high","rationale":"no"}`}
	r, srv := newReviewer(t, deny, fakellm.Reply{Text: `{"outcome":"allow"}`}, deny, deny, deny, fakellm.Reply{Text: `{"outcome":"allow"}`})
	ctx := context.Background()

	var got []review.Outcome
	for range 6 {
		v, err := r.Review(ctx, sample())
		require.NoError(t, err)
		got = append(got, v.Outcome)
	}

	assert.Equal(t, []review.Outcome{review.Deny, review.Allow, review.Deny, review.Deny, review.Deny, review.AskUser}, got)
	assert.Len(t, srv.Requests(), 5, "an open breaker does not call the model")

	r.Reset()
	v, err := r.Review(ctx, sample())
	require.NoError(t, err)
	assert.Equal(t, review.Allow, v.Outcome, "a new turn reviews again")
}

func TestBreaker_RecentWindow(t *testing.T) {
	var b review.Breaker
	for i := range review.MaxRecentDenials - 1 {
		assert.False(t, b.Record(true), "denial %d", i)
		assert.False(t, b.Record(false))
	}
	assert.True(t, b.Record(true), "10 denials within the last 50 reviews")
}

func TestParse(t *testing.T) {
	tests := []struct {
		name, text string
		want       review.Verdict
		err        string
	}{
		{name: "minimal allow", text: `{"outcome":"allow"}`, want: review.Verdict{Outcome: review.Allow, Risk: review.RiskLow, Authorization: "unknown", Reason: "Auto-review returned a low-risk allow decision."}},
		{name: "deny defaults to high", text: ` {"outcome":"deny"} `, want: review.Verdict{Outcome: review.Deny, Risk: review.RiskHigh, Authorization: "unknown", Reason: "Auto-review returned a deny decision without a rationale."}},
		{name: "fenced", text: "```json\n{\"outcome\":\"allow\",\"risk_level\":\"medium\",\"rationale\":\"ok\"}\n```", want: review.Verdict{Outcome: review.Allow, Risk: review.RiskMedium, Authorization: "unknown", Reason: "ok"}},
		{name: "prose", text: `Sure: {"outcome":"allow"}`, err: "failed to parse"},
		{name: "unknown field", text: `{"outcome":"allow","confidence":1}`, err: "failed to parse"},
		{name: "missing outcome", text: `{"risk_level":"low"}`, err: "invalid review outcome"},
		{name: "bad risk", text: `{"outcome":"allow","risk_level":"none"}`, err: "invalid review risk_level"},
		{name: "bad authorization", text: `{"outcome":"allow","user_authorization":"total"}`, err: "invalid review user_authorization"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := review.Parse(tt.text)
			if tt.err != "" {
				require.ErrorContains(t, err, tt.err)

				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestDefaultModel(t *testing.T) {
	assert.Equal(t, "codex-auto-review", review.DefaultModel("openai-codex", "gpt-6-sol"))
	assert.Equal(t, "gpt-6-astra", review.DefaultModel("openai", "gpt-6-astra"))
}

// TestReview_ContinuesTheConversation sends a session's later reviews as
// Codex's delta on the same conversation: the earlier request and answer
// stay as the prefix, the reminder comes once, and only the new entries
// and the action follow.
func TestReview_ContinuesTheConversation(t *testing.T) {
	allow := fakellm.Reply{Text: `{"outcome":"allow"}`}
	r, srv := newReviewer(t, allow, allow, allow)
	conv := &review.Conversation{}
	req := sample()
	req.Conversation = conv
	ctx := context.Background()

	v, err := r.Review(ctx, req)
	require.NoError(t, err)
	assert.False(t, v.Delta)
	req.Transcript.Entries = append(req.Transcript.Entries, review.Entry{Kind: review.EntryUser, Text: "push it now"})
	v, err = r.Review(ctx, req)
	require.NoError(t, err)
	assert.True(t, v.Delta)
	assert.False(t, v.Forked)
	_, err = r.Review(ctx, req)
	require.NoError(t, err)

	reqs := srv.Requests()
	require.Len(t, reqs, 3)
	require.Len(t, reqs[1].UserTexts, 2)
	assert.Equal(t, reqs[0].UserTexts[0], reqs[1].UserTexts[0], "the first review's message stays")
	assert.Equal(t, reqs[0].Input, reqs[1].Input[:len(reqs[0].Input)], "the earlier request is the prefix")
	assert.Contains(t, reqs[1].UserTexts[1], ">>> TRANSCRIPT DELTA START\n[4] user: push it now\n>>> TRANSCRIPT DELTA END")
	assert.NotContains(t, reqs[1].UserTexts[1], "Fix the typo")
	require.Len(t, reqs[1].DeveloperTexts, 1)
	assert.Contains(t, reqs[1].DeveloperTexts[0], "Use prior reviews as context, not binding precedent.")
	assert.Len(t, reqs[2].DeveloperTexts, 1, "the reminder is added once")
	assert.Contains(t, reqs[2].UserTexts[2], "<no retained transcript delta entries>")
	assert.Equal(t, reqs[1].Input, reqs[2].Input[:len(reqs[1].Input)])
}

// TestReview_ParallelReviewsFork runs a review that starts while another
// holds the conversation on a copy of the last finished state, as Codex
// forks its trunk; the copy is dropped, so the next review continues from
// the conversation's own review.
func TestReview_ParallelReviewsFork(t *testing.T) {
	gate := make(chan struct{})
	allow := fakellm.Reply{Text: `{"outcome":"allow"}`}
	r, srv := newReviewer(t, fakellm.Reply{Text: `{"outcome":"allow"}`, Gate: gate}, allow, allow)
	conv := &review.Conversation{}
	first := sample()
	first.Conversation = conv
	ctx := context.Background()

	done := make(chan review.Verdict)
	go func() {
		v, err := r.Review(ctx, first)
		assert.NoError(t, err)
		done <- v
	}()
	<-srv.Seen()
	second := first
	second.Transcript.Entries = append(slices.Clone(first.Transcript.Entries), review.Entry{Kind: review.EntryCall, Tool: "Bash", Text: "second"})
	v, err := r.Review(ctx, second)
	require.NoError(t, err)
	assert.True(t, v.Forked)
	assert.False(t, v.Delta, "no review had finished, so the fork starts anew")
	close(gate)
	v = <-done
	assert.False(t, v.Forked)

	v, err = r.Review(ctx, second)
	require.NoError(t, err)
	assert.True(t, v.Delta)
	reqs := srv.Requests()
	require.Len(t, reqs, 3)
	assert.Contains(t, reqs[2].UserTexts[1], "[4] tool Bash call: second", "the delta starts after the first review, not the fork")
}

// TestReview_FailureEndsTheConversation starts anew after a failed review,
// as Codex discards a reviewer that did not finish.
func TestReview_FailureEndsTheConversation(t *testing.T) {
	allow := fakellm.Reply{Text: `{"outcome":"allow"}`}
	bad := fakellm.Reply{Text: "maybe"}
	r, _ := newReviewer(t, allow, bad, bad, bad, allow)
	req := sample()
	req.Conversation = &review.Conversation{}
	ctx := context.Background()

	_, err := r.Review(ctx, req)
	require.NoError(t, err)
	v, err := r.Review(ctx, req)
	require.NoError(t, err)
	assert.True(t, v.Failed)
	v, err = r.Review(ctx, req)
	require.NoError(t, err)
	assert.False(t, v.Delta)
}

// TestReview_RunsReadOnlyCommands gives the reviewer Codex's exec_command
// when it has a runner, runs its calls, and returns their output to it.
func TestReview_RunsReadOnlyCommands(t *testing.T) {
	r, srv := newReviewer(t,
		fakellm.Reply{Calls: []fakellm.Call{{Name: "exec_command", Args: `{"cmd":"ls build","workdir":"/ws"}`}}},
		fakellm.Reply{Text: `{"outcome":"allow","risk_level":"low","rationale":"build is a small output folder"}`},
	)
	var ran []string
	req := sample()
	req.Run = func(_ context.Context, command, workdir string) (string, int, error) {
		ran = append(ran, workdir+": "+command)

		return "main.o\n", 0, nil
	}

	v, err := r.Review(context.Background(), req)

	require.NoError(t, err)
	assert.Equal(t, review.Allow, v.Outcome)
	assert.Equal(t, 1, v.Commands)
	assert.Equal(t, []string{"/ws: ls build"}, ran)
	reqs := srv.Requests()
	require.Len(t, reqs, 2)
	assert.Equal(t, []string{"exec_command"}, reqs[0].ToolNames)
	assert.Contains(t, reqs[0].System, "you can only run read-only commands, with `exec_command`")
	require.Len(t, reqs[1].ToolOutputs, 1)
	assert.Contains(t, reqs[1].ToolOutputs[0], "Process exited with code 0")
	assert.Contains(t, reqs[1].ToolOutputs[0], "main.o")
}
