package compaction_test

import (
	"context"
	"encoding/json/jsontext"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/viktordanov/uah-core/harness/llm"
	"github.com/viktordanov/uah-core/harness/llm/clients/openai"

	"github.com/viktordanov/uah/internal/compaction"
	"github.com/viktordanov/uah/internal/llmcall"
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

// longHistory is a system message and n user messages of 100 tokens each.
func longHistory(n int) []llm.Item {
	items := []llm.Item{msg(llm.RoleSystem, "sys")}
	for i := range n {
		items = append(items, msg(llm.RoleUser, fmt.Sprintf("%03d", i)+strings.Repeat(".", 397)))
	}

	return items
}

func TestSummarize_TrimsAHistoryLargerThanTheWindow(t *testing.T) {
	srv := fakellm.New(t, fakellm.Reply{Text: "S"})
	got, err := compaction.Summarize(context.Background(), compaction.SummaryCall{
		Adapter: client(t, srv), Model: "gpt-test", Window: 1_000, Prompt: compaction.CodexPrompt,
	}, longHistory(20))
	require.NoError(t, err)
	assert.Equal(t, "S", got.Text)
	req := srv.Requests()[0]
	require.Len(t, req.UserTexts, 1+8, "about 850 tokens of history fit next to Codex's prompt")
	assert.True(t, strings.HasPrefix(req.UserTexts[0], "012"), "the oldest messages go first")
	assert.Equal(t, compaction.CodexPrompt, req.UserTexts[len(req.UserTexts)-1])
}

func TestSummarize_RetriesWithLessOnAnOverflow(t *testing.T) {
	srv := fakellm.New(t,
		fakellm.Reply{Fail: 400, FailCode: "context_length_exceeded"},
		fakellm.Reply{Text: "S"},
	)
	got, err := compaction.Summarize(context.Background(), compaction.SummaryCall{
		Adapter: client(t, srv), Model: "gpt-test", Window: 272_000,
	}, longHistory(20))
	require.NoError(t, err)
	assert.Equal(t, "S", got.Text)
	reqs := srv.Requests()
	require.Len(t, reqs, 2)
	assert.Len(t, reqs[0].UserTexts, 21)
	assert.Len(t, reqs[1].UserTexts, 1+15, "the retry sends three quarters of the history")
}

func TestSummarize_Failures(t *testing.T) {
	for name, reply := range map[string]fakellm.Reply{
		"provider error": {Fail: 400, FailCode: "invalid_prompt"},
		"no text":        {},
	} {
		t.Run(name, func(t *testing.T) {
			srv := fakellm.New(t, reply)
			_, err := compaction.Summarize(context.Background(), compaction.SummaryCall{
				Adapter: client(t, srv), Model: "gpt-test", Window: 272_000,
			}, longHistory(2))
			require.Error(t, err)
			assert.Len(t, srv.Requests(), 1, "only an overflow is retried")
		})
	}
	srv := fakellm.New(t, fakellm.Reply{})
	_, err := compaction.Summarize(context.Background(), compaction.SummaryCall{Adapter: client(t, srv), Window: 1}, longHistory(1))
	require.ErrorIs(t, err, llmcall.ErrNoText)
}

func TestSummarize_GivesUpAfterRepeatedOverflows(t *testing.T) {
	var replies []fakellm.Reply
	for range 10 {
		replies = append(replies, fakellm.Reply{Fail: 400, FailCode: "context_length_exceeded"})
	}
	srv := fakellm.New(t, replies...)
	_, err := compaction.Summarize(context.Background(), compaction.SummaryCall{
		Adapter: client(t, srv), Model: "gpt-test", Window: 272_000,
	}, longHistory(40))
	require.ErrorIs(t, err, llmcall.ErrContextWindow)
	assert.Len(t, srv.Requests(), 5)
}

func TestTrim_DetachesOutputsOfDroppedCalls(t *testing.T) {
	items := []llm.Item{call("a"), msg(llm.RoleAssistant, strings.Repeat("x", 400)), result("a", "out"), msg(llm.RoleUser, "last")}
	got := compaction.Trim(items, 10)
	assert.Equal(t, []string{"user: Output of the earlier tool call a, which the summary covers:\nout", "user: last"}, texts(got))
	assert.Equal(t, items, compaction.Trim(items, 1_000), "nothing is dropped when it fits")
	assert.Equal(t, []string{"user: last"}, texts(compaction.Trim(items, 0)), "the last item always stays")
}

func TestInUse_CountsWhatCameAfterTheLastResponse(t *testing.T) {
	view := []llm.Item{
		msg(llm.RoleSystem, strings.Repeat("s", 400)), msg(llm.RoleUser, "hi"), call("a"),
		result("a", strings.Repeat("o", 4_000)), msg(llm.RoleUser, strings.Repeat("u", 40)),
	}
	assert.Equal(t, int64(5_000+1_001+10), compaction.InUse(view, 5_000), "last total plus the output and the message after the call")
	assert.Equal(t, compaction.EstimateTokens(view), compaction.InUse(view, 0), "no usage: the whole request is estimated")
	assert.Equal(t, int64(100+1+2+1_001+10), compaction.EstimateTokens(view))

	encrypted := `{"type":"reasoning","encrypted_content":"` + strings.Repeat("e", 4_000) + `"}`
	reasoning := llm.Item{Type: llm.ItemReasoning, Data: llm.Reasoning{Raw: jsontext.Value(encrypted)}}
	assert.Equal(t, int64((4_000*3/4-650+3)/4), compaction.EstimateTokens([]llm.Item{reasoning}), "Codex's estimate of encrypted reasoning")
	image := llm.Item{Type: llm.ItemToolResult, Data: llm.ToolResult{CallID: "c", Output: []llm.ToolResultOutput{{Kind: llm.ToolResultImage, Value: "data:x"}}}}
	assert.Equal(t, int64((7_373+1+3)/4), compaction.EstimateTokens([]llm.Item{image}))
}

func TestSummarize_StubsTheNewestOutputsBeforeDroppingItems(t *testing.T) {
	srv := fakellm.New(t, fakellm.Reply{Text: "S"})
	view := append([]llm.Item{msg(llm.RoleSystem, "sys"), msg(llm.RoleUser, "the task")}, calls(3, func(int) int { return 400 })...)
	_, err := compaction.Summarize(context.Background(), compaction.SummaryCall{
		Adapter: client(t, srv), Model: "gpt-test", Window: compaction.EstimateTokens(view), Prompt: compaction.CodexPrompt,
	}, view)
	require.NoError(t, err)
	req := srv.Requests()[0]
	assert.Equal(t, []string{"the task", compaction.CodexPrompt}, req.UserTexts, "no item was dropped")
	assert.Equal(t, []string{"c0", "c1", "c2"}, req.CallIDs)
	require.Len(t, req.ToolOutputs, 3)
	assert.Equal(t, strings.Repeat("x", 1_600), req.ToolOutputs[0])
	assert.Equal(t, strings.Repeat("x", 1_600), req.ToolOutputs[1], "only as many stubs as it takes")
	assert.True(t, strings.HasPrefix(req.ToolOutputs[2], "[uah elided this output"), "the newest output is a stub")
}

// deadlines records whether each call's context had a deadline.
type deadlines struct{ had []bool }

func (d *deadlines) Respond(ctx context.Context, _ llm.Request, _ llm.RequestOptions) (llm.Response, error) {
	_, ok := ctx.Deadline()
	d.had = append(d.had, ok)

	return llm.Response{Output: []llm.Item{msg(llm.RoleAssistant, "S")}}, nil
}

func TestSummarize_HasNoDeadlineOfItsOwn(t *testing.T) {
	d := &deadlines{}
	got, err := compaction.Summarize(context.Background(), compaction.SummaryCall{Adapter: d, Window: 272_000}, longHistory(2))
	require.NoError(t, err)
	assert.Equal(t, "S", got.Text)
	assert.Equal(t, []bool{false}, d.had, "llmcall.DefaultTimeout does not bound a summary; the adapter ends a stalled stream")
}
