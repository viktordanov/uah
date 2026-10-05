package agents_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/agents"
	"github.com/viktordanov/uah/internal/codereview"
	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/engine/embedded"
	"github.com/viktordanov/uah/internal/instructions"
	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/testing/fakellm"
)

// reviewAnswer is a reviewer's answer in Codex's format.
const reviewAnswer = `{"findings":[{"title":"[P1] Check the error","body":"It is dropped.","confidence_score":0.8,"priority":1,
"code_location":{"absolute_file_path":"/w/a.go","line_range":{"start":3,"end":4}}}],
"overall_correctness":"patch is incorrect","overall_explanation":"One bug.","overall_confidence_score":0.7}`

// uncommitted is the start of Codex's prompt for the uncommitted changes.
const uncommitted = "Review the current code changes"

func (ev *events) reviewFinished() session.ReviewFinished {
	ev.t.Helper()
	match := func(x core.Event) bool { _, ok := x.(session.ReviewFinished); return ok }
	if i := slices.IndexFunc(ev.all, match); i >= 0 { // seen while waiting for something else
		return ev.all[i].(session.ReviewFinished)
	}

	return ev.until("ReviewFinished", match).(session.ReviewFinished)
}

func reviewerRequests(e *env) []fakellm.Request {
	return slices.DeleteFunc(e.llm.Requests(), func(r fakellm.Request) bool {
		return !slices.ContainsFunc(r.UserTexts, func(s string) bool { return strings.HasPrefix(s, uncommitted) })
	})
}

// TestReview_ReadOnlySubagent runs /review end to end: the reviewer is a
// fresh session with Codex's rubric as its system prompt, the review
// model, Bash and ViewImage only, and the read-only sandbox, whose writes
// fail and whose escalations are declined without asking. Its findings
// come back parsed, and reach the main agent with the next message in
// Codex's <user_action>.
func TestReview_ReadOnlySubagent(t *testing.T) {
	e := newEnv(t, agents.Config{ReviewModel: "gpt-review"}, fakellm.Reply{Text: "I will fix it"})
	e.llm.Route(uncommitted,
		fakellm.Reply{Commands: []string{"touch written.txt", "echo seen"}},
		fakellm.Reply{Escalated: []string{"touch escalated.txt"}},
		fakellm.Reply{Text: reviewAnswer},
	)
	s, ev := e.open(t, true, e.sandboxed(t))
	const env = "<environment_context>\n  <cwd>/w</cwd>\n</environment_context>"
	withPrompt := e.settings()
	withPrompt.SystemPrompt = instructions.HostPrompt("", "Always use tabs.", env)
	_, err := s.SetSettings(withPrompt)
	require.NoError(t, err)

	require.NoError(t, s.Review(context.Background(), codereview.Target{Kind: codereview.Uncommitted}))
	fin := ev.reviewFinished()

	require.Empty(t, fin.Err)
	assert.False(t, fin.Interrupted)
	require.Len(t, fin.Output.Findings, 1)
	assert.Equal(t, "[P1] Check the error", fin.Output.Findings[0].Title)
	assert.Equal(t, "patch is incorrect", fin.Output.OverallCorrectness)

	started := slices.IndexFunc(ev.all, func(x core.Event) bool { r, ok := x.(session.ReviewStarted); return ok && r.Hint == "current changes" })
	assert.GreaterOrEqual(t, started, 0, "ReviewStarted says what is reviewed")
	activity, responses, failures := 0, 0, 0
	for _, x := range ev.all {
		_, asked := x.(session.ApprovalRequested)
		assert.False(t, asked, "the reviewer never asks for approval")
		if a, ok := x.(session.ReviewActivity); ok && a.ID == fin.ID {
			activity++
			if _, ok := a.Event.(core.ModelResponded); ok {
				responses++
			}
			if _, ok := a.Event.(engine.ToolOutput); ok {
				failures++
			}
		}
		_, agent := x.(engine.AgentUpdated)
		assert.False(t, agent, "the reviewer is not one of the agent's subagents")
	}
	assert.Positive(t, activity, "the reviewer's tool calls are reported")
	assert.Positive(t, responses, "and its model responses, for the tokens so far")
	assert.Positive(t, failures, "and why a command failed")

	reqs := reviewerRequests(e)
	require.NotEmpty(t, reqs)
	for _, r := range reqs {
		assert.True(t, strings.HasSuffix(strings.TrimSpace(r.System), strings.TrimRight(codereview.Instructions(), "\n")+"\n\n"+env), "Codex's rubric, then the environment context")
		assert.NotContains(t, r.System, "Always use tabs.", "none of the project's instructions")
		assert.NotContains(t, r.System, strings.TrimSpace(instructions.DefaultPrompt[:200]), "none of the main agent's base instructions")
		assert.NotContains(t, r.System, instructions.SubagentNote, "its answer goes to the user, not to a parent agent")
		assert.Equal(t, "gpt-review", r.Model)
		assert.Equal(t, []string{"Bash", "ViewImage"}, r.ToolNames, "no apply_patch, MCP, or agent tools")
	}
	outputs := strings.Join(reqs[len(reqs)-1].ToolOutputs, "\n")
	assert.Contains(t, outputs, "seen")
	assert.Regexp(t, `(?i)not permitted|read-only file system`, outputs, "the write failed in the sandbox")
	assert.Contains(t, outputs, "this session never asks for approval", "the escalation was declined")
	assert.NoFileExists(t, filepath.Join(e.Workspace, "written.txt"), "the sandbox is read-only")
	assert.NoFileExists(t, filepath.Join(e.Workspace, "escalated.txt"), "an escalation is declined")
	assert.Len(t, e.llm.Requests(), len(reqs), "the main agent was not asked")

	_, err = s.Submit("fix the finding")
	require.NoError(t, err)
	assert.Equal(t, "I will fix it", ev.finished().Answer)
	main := e.llm.Requests()[len(e.llm.Requests())-1]
	require.Len(t, main.UserTexts, 2)
	assert.Equal(t, codereview.ExitMessage(fin.Output, false), main.UserTexts[0], "Codex's hand-over goes first")
	assert.Equal(t, "fix the finding", main.UserTexts[1])

	infos, err := session.Sessions(e.StateDir)
	require.NoError(t, err)
	i := slices.IndexFunc(infos, func(in session.Info) bool { return in.ID != s.ID() })
	require.GreaterOrEqual(t, i, 0)
	assert.Equal(t, s.ID(), infos[i].Parent, "the reviewer's session is a child of the main one")
}

// TestReview_Interrupted stops a review with the session's interrupt:
// the main agent gets Codex's interrupted form.
func TestReview_Interrupted(t *testing.T) {
	gate := make(chan struct{})
	t.Cleanup(func() { close(gate) })
	e := newEnv(t, agents.Config{}, fakellm.Reply{Text: "ok"})
	e.llm.Route(uncommitted, fakellm.Reply{Gate: gate, Text: reviewAnswer})
	s, ev := e.open(t, false)

	done := make(chan error, 1)
	go func() { done <- s.Review(context.Background(), codereview.Target{Kind: codereview.Uncommitted}) }()
	ev.until("ReviewStarted", func(x core.Event) bool { _, ok := x.(session.ReviewStarted); return ok })
	<-e.llm.Seen() // the reviewer's request is out
	require.NoError(t, s.Interrupt())
	fin := ev.reviewFinished()
	require.NoError(t, <-done)

	assert.True(t, fin.Interrupted)
	assert.Empty(t, fin.Output.Findings)
	_, err := s.Submit("next")
	require.NoError(t, err)
	ev.finished()
	main := e.llm.Requests()[len(e.llm.Requests())-1]
	assert.Contains(t, main.UserTexts[0], "User initiated a review task, but was interrupted.")
}

// TestReview_OneAtATime refuses a second review while one runs, and a
// review on an engine without a reviewer.
func TestReview_OneAtATime(t *testing.T) {
	gate := make(chan struct{})
	e := newEnv(t, agents.Config{})
	e.llm.Route(uncommitted, fakellm.Reply{Gate: gate, Text: reviewAnswer})
	s, ev := e.open(t, false)

	done := make(chan error, 1)
	go func() { done <- s.Review(context.Background(), codereview.Target{Kind: codereview.Uncommitted}) }()
	ev.until("ReviewStarted", func(x core.Event) bool { _, ok := x.(session.ReviewStarted); return ok })
	err := s.Review(context.Background(), codereview.Target{Kind: codereview.Custom, Instructions: "again"})
	require.ErrorContains(t, err, "a review is already running")
	close(gate)
	require.NoError(t, <-done)
	assert.Len(t, ev.reviewFinished().Output.Findings, 1)

	bare, _ := e.open(t, false, func(c *embedded.Config) { c.Subagents = nil })
	require.ErrorIs(t, bare.Review(context.Background(), codereview.Target{Kind: codereview.Uncommitted}), session.ErrNoReview)
}

// hasWebSearch reports whether a request offered the hosted web search
// tool, and whether its input carries a recorded search.
func hasWebSearch(r fakellm.Request) (offered, inserted bool) {
	offered = slices.ContainsFunc(r.ToolDefs, func(d json.RawMessage) bool { return strings.Contains(string(d), `"web_search"`) })
	inserted = slices.ContainsFunc(r.Input, func(item json.RawMessage) bool { return strings.Contains(string(item), `"web_search_call"`) })

	return offered, inserted
}

// TestReview_NoWebSearch leaves the hosted web search out of the
// reviewer's requests, as Codex turns search off for a review, while the
// parent keeps it; the reviewer's own searches are not put back into its
// later requests either.
func TestReview_NoWebSearch(t *testing.T) {
	e := newEnv(t, agents.Config{}, fakellm.Reply{Text: "Go 1.27", Searches: []fakellm.Search{{Query: "latest Go release"}}}, fakellm.Reply{Text: "ok"})
	e.llm.Route(uncommitted,
		fakellm.Reply{Commands: []string{"echo looked"}, Searches: []fakellm.Search{{Query: "reviewer search"}}},
		fakellm.Reply{Text: reviewAnswer},
	)
	s, ev := e.open(t, false, func(c *embedded.Config) { c.WebSearch = true })

	_, err := s.Submit("look it up")
	require.NoError(t, err)
	ev.finished()
	require.NoError(t, s.Review(context.Background(), codereview.Target{Kind: codereview.Uncommitted}))
	require.Len(t, ev.reviewFinished().Output.Findings, 1)
	_, err = s.Submit("and now")
	require.NoError(t, err)
	ev.finished()

	reqs := reviewerRequests(e)
	require.Len(t, reqs, 2)
	for _, r := range reqs {
		offered, inserted := hasWebSearch(r)
		assert.False(t, offered, "the reviewer is not offered web search")
		assert.False(t, inserted, "no recorded search goes into the reviewer's requests")
	}
	last := e.llm.Requests()[len(e.llm.Requests())-1]
	offered, inserted := hasWebSearch(last)
	assert.True(t, offered, "the parent keeps web search")
	assert.True(t, inserted, "and its own recorded search")
}

// TestReview_ReadOnlyUnderYolo: a parent in yolo mode still gets a
// reviewer in the read-only sandbox that never asks: its write fails and
// its escalation is declined.
func TestReview_ReadOnlyUnderYolo(t *testing.T) {
	e := newEnv(t, agents.Config{})
	e.yolo = true
	e.llm.Route(uncommitted,
		fakellm.Reply{Commands: []string{"touch written.txt"}},
		fakellm.Reply{Escalated: []string{"touch escalated.txt"}},
		fakellm.Reply{Text: reviewAnswer},
	)
	s, ev := e.open(t, true, e.sandboxed(t))

	require.NoError(t, s.Review(context.Background(), codereview.Target{Kind: codereview.Uncommitted}))
	fin := ev.reviewFinished()

	require.Empty(t, fin.Err)
	outputs := strings.Join(reviewerRequests(e)[len(reviewerRequests(e))-1].ToolOutputs, "\n")
	assert.Contains(t, outputs, "this session never asks for approval", "the escalation was declined")
	assert.NoFileExists(t, filepath.Join(e.Workspace, "written.txt"), "the sandbox is read-only")
	assert.NoFileExists(t, filepath.Join(e.Workspace, "escalated.txt"), "an escalation is declined")
}

// TestReview_Usage: the review reports the reviewer's model and effort
// when it starts and the tokens it used when it ends, and uah sessions and
// the loaded run show those tokens too.
func TestReview_Usage(t *testing.T) {
	e := newEnv(t, agents.Config{ReviewModel: "gpt-review"})
	e.llm.Route(uncommitted,
		fakellm.Reply{Commands: []string{"echo one"}},
		fakellm.Reply{Text: reviewAnswer},
	)
	s, ev := e.open(t, false)

	require.NoError(t, s.Review(context.Background(), codereview.Target{Kind: codereview.Uncommitted}))
	fin := ev.reviewFinished()

	require.Empty(t, fin.Err)
	i := slices.IndexFunc(ev.all, func(x core.Event) bool { _, ok := x.(session.ReviewStarted); return ok })
	require.GreaterOrEqual(t, i, 0)
	started := ev.all[i].(session.ReviewStarted)
	assert.Equal(t, "gpt-review", started.Model)
	assert.Equal(t, "high", started.Effort)
	assert.Positive(t, fin.Tokens.InputTokens, "the reviewer's tokens")
	assert.Positive(t, fin.Tokens.OutputTokens)

	infos, err := session.Sessions(e.StateDir)
	require.NoError(t, err)
	j := slices.IndexFunc(infos, func(in session.Info) bool { return in.ID != s.ID() })
	require.GreaterOrEqual(t, j, 0)
	assert.Equal(t, fin.Tokens, infos[j].Tokens, "uah sessions shows the reviewer's tokens")
	runs, err := session.Load(e.StateDir, infos[j].ID)
	require.NoError(t, err)
	require.Len(t, runs, 1)
	assert.Equal(t, fin.Tokens, runs[0].Record.Result.Stats.Tokens, "and so does its loaded run")
}

// TestReview_InstructionFilesLeftOut: the reviewer's system prompt
// replaces uah's without the instruction files, so its prepared context
// names them as left out instead of saying they are in the system prompt,
// while the parent's says they are.
func TestReview_InstructionFilesLeftOut(t *testing.T) {
	e := newEnv(t, agents.Config{}, fakellm.Reply{Text: "ok"})
	e.llm.Route(uncommitted, fakellm.Reply{Text: reviewAnswer})
	agentsFile := filepath.Join(e.Workspace, "AGENTS.md")
	s, ev := e.open(t, false, func(c *embedded.Config) { c.ContextPreparation, c.InstructionFiles = true, []string{agentsFile} })
	withPrompt := e.settings()
	withPrompt.SystemPrompt = instructions.HostPrompt("", "Always use tabs.", "")
	_, err := s.SetSettings(withPrompt)
	require.NoError(t, err)

	_, err = s.Submit("hello")
	require.NoError(t, err)
	ev.finished()
	require.NoError(t, s.Review(context.Background(), codereview.Target{Kind: codereview.Uncommitted}))
	require.Empty(t, ev.reviewFinished().Err)

	parent := strings.Join(e.llm.Requests()[0].DeveloperTexts, "\n")
	assert.Contains(t, parent, "Instruction files in the system prompt, in order")
	assert.Contains(t, parent, "- "+agentsFile)
	reqs := reviewerRequests(e)
	require.NotEmpty(t, reqs)
	reviewer := strings.Join(reqs[0].DeveloperTexts, "\n")
	assert.Contains(t, reviewer, "leaves out the workspace's instruction files on purpose")
	assert.Contains(t, reviewer, "- "+agentsFile)
	assert.NotContains(t, reviewer, "Instruction files in the system prompt", "the reviewer's system prompt has none")
	assert.NotContains(t, reviewer, "there are none to search for")
}
