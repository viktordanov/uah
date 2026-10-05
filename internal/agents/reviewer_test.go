package agents_test

import (
	"context"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uagent/core"
	"github.com/viktordanov/uagent/harness"

	"github.com/viktordanov/uah/internal/agents"
	"github.com/viktordanov/uah/internal/codereview"
	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/testing/fakellm"
)

// finishedReviewer runs a review in the parent session and returns the
// reviewer's session ID, from its sidecar.
func finishedReviewer(t *testing.T, e *env, s *session.Session, ev *events) string {
	t.Helper()
	fin := review(t, s, ev)
	require.Empty(t, fin.Err)
	infos, err := session.Sessions(e.StateDir)
	require.NoError(t, err)
	for _, info := range infos {
		if info.ID != s.ID() && info.Parent == s.ID() {
			sc, found, err := session.ReadSidecar(e.sessionsDir(), info.ID)
			require.NoError(t, err)
			require.True(t, found)
			assert.True(t, sc.Review, "the sidecar marks the reviewer")
			assert.True(t, strings.HasPrefix(sc.FirstPrompt, uncommitted), "the reviewer's own first prompt, not the parent's: %q", sc.FirstPrompt)

			return info.ID
		}
	}
	t.Fatal("no reviewer session")

	return ""
}

// TestReview_ResumeAFinishedReviewer refuses resume_agent on a reviewer
// whose review ended: a reviewer is reached as a live agent while its
// review runs, never through its session, and nothing starts.
func TestReview_ResumeAFinishedReviewer(t *testing.T) {
	e := newEnv(t, agents.Config{})
	e.llm.Route(uncommitted, fakellm.Reply{Text: reviewAnswer})
	s, ev := e.open(t, false)
	id := finishedReviewer(t, e, s, ev)

	e.llm.Script(
		fakellm.Reply{Calls: []fakellm.Call{call(agents.ToolResume, `{"id":"`+id+`"}`)}},
		fakellm.Reply{Calls: []fakellm.Call{call(agents.ToolSend, `{"target":"`+id+`","message":"stop"}`)}},
		fakellm.Reply{Text: "ok"},
	)
	_, err := s.Submit("talk to the reviewer")
	require.NoError(t, err)
	ev.finished()

	outputs := strings.Join(lastParent(e).ToolOutputs, "\n")
	assert.Contains(t, outputs, "is the reviewer of a /review the user started, which is not running here: it cannot be resumed")
	assert.Contains(t, outputs, "is closed", "send_input says the reviewer is closed, not that the message went out")
	assert.NotContains(t, outputs, "submission_id")
	assert.NoFileExists(t, filepath.Join(e.sessionsDir(), id+".agent.json"), "no agent record: nothing was resumed")
}

// reviewerID is the reviewer's agent ID from the note the main agent got.
var reviewerID = regexp.MustCompile(`reviewer is agent (subagent-[0-9a-f-]{36})`)

// steerReviewer is a main agent's reply that calls tool on the running
// reviewer, with args where ID is its ID.
func steerReviewer(tool, args string) fakellm.Reply {
	return fakellm.Reply{From: func(req fakellm.Request) fakellm.Reply {
		id := ""
		for _, d := range req.DeveloperTexts {
			if m := reviewerID.FindStringSubmatch(d); m != nil {
				id = m[1]
			}
		}

		return fakellm.Reply{Calls: []fakellm.Call{call(tool, strings.ReplaceAll(args, "ID", id))}}
	}}
}

// TestReview_TheReviewerIsAnAgent: while a /review runs, its reviewer is
// one of the main agent's agents, marked as the user's. send_input reaches
// its live run (here, telling it to answer now), the review's answer is
// the one that follows, and wait_agent returns it.
func TestReview_TheReviewerIsAnAgent(t *testing.T) {
	gate := make(chan struct{})
	t.Cleanup(func() { close(gate) })
	e := newEnv(t, agents.Config{},
		steerReviewer(agents.ToolSend, `{"target":"ID","message":"The user asks you to answer now."}`),
		steerReviewer(agents.ToolWait, `{"targets":["ID"]}`),
		fakellm.Reply{Text: "it answered"},
	)
	e.llm.Route(uncommitted, fakellm.Reply{Gate: gate, Text: "never"}, fakellm.Reply{Text: reviewAnswer})
	s, ev := e.open(t, false)

	done := make(chan error, 1)
	go func() { done <- s.Review(context.Background(), codereview.Target{Kind: codereview.Uncommitted}) }()
	ev.until("ReviewStarted", func(x core.Event) bool { _, ok := x.(session.ReviewStarted); return ok })
	<-e.llm.Seen() // the reviewer thinks
	_, err := s.Submit("tell the reviewer to answer now")
	require.NoError(t, err)
	assert.Equal(t, "it answered", ev.finished().Answer)
	fin := ev.reviewFinished()
	require.NoError(t, <-done)

	require.Empty(t, fin.Err)
	require.Len(t, fin.Output.Findings, 1)
	outputs := strings.Join(mainRequest(e, "tell the reviewer to answer now").ToolOutputs, "\n")
	assert.Contains(t, outputs, "submission_id")
	assert.Contains(t, outputs, `"completed"`, "wait_agent returns the review's answer")
	reqs := reviewerRequests(e)
	assert.Contains(t, reqs[len(reqs)-1].UserTexts, "The user asks you to answer now.", "the message reached the reviewer's run")
	update := ev.agentState(engine.AgentRunning)
	assert.Equal(t, "review", update.Role)
	assert.Contains(t, update.Task, "started by the user: leave it alone unless the user asks you to act on it")
}

// TestReview_CloseAgentStopsTheReview: close_agent on the reviewer stops
// the review, which ends as interrupted.
func TestReview_CloseAgentStopsTheReview(t *testing.T) {
	gate := make(chan struct{})
	t.Cleanup(func() { close(gate) })
	e := newEnv(t, agents.Config{}, steerReviewer(agents.ToolClose, `{"target":"ID"}`), fakellm.Reply{Text: "stopped"})
	e.llm.Route(uncommitted, fakellm.Reply{Gate: gate, Text: "never"})
	s, ev := e.open(t, false)

	done := make(chan error, 1)
	go func() { done <- s.Review(context.Background(), codereview.Target{Kind: codereview.Uncommitted}) }()
	ev.until("ReviewStarted", func(x core.Event) bool { _, ok := x.(session.ReviewStarted); return ok })
	<-e.llm.Seen()
	_, err := s.Submit("the user wants the review stopped")
	require.NoError(t, err)
	fin := ev.reviewFinished()
	require.NoError(t, <-done)

	assert.True(t, fin.Interrupted)
	assert.Empty(t, fin.Err)
}

// TestAgents_ResumeInUse refuses to resume a child whose session another
// run holds, as the reviewer was held when "Ada" was resumed: nothing
// starts, so a later send_input says the child is not loaded instead of
// queuing a message that never arrives.
func TestAgents_ResumeInUse(t *testing.T) {
	e := newEnv(t, agents.Config{},
		fakellm.Reply{Calls: []fakellm.Call{call("spawn_agent", `{"message":"CHILD-U task"}`)}},
		callWith("wait_agent", `{"targets":["ID"]}`),
		fakellm.Reply{Text: "first done"},
		callWith("resume_agent", `{"id":"ID"}`),
		callWith("send_input", `{"target":"ID","message":"again"}`),
		fakellm.Reply{Text: "second done"},
	)
	e.llm.Route("CHILD-U", fakellm.Reply{Text: "answer"})
	s, ev := e.open(t, false)
	_, err := s.Submit("delegate")
	require.NoError(t, err)
	assert.Equal(t, "first done", ev.finished().Answer)
	parentID := s.ID()
	childID := ids(lastParent(e))[0]
	require.NoError(t, s.Close())
	unlock, err := harness.LockSession(e.StateDir, childID) // another run holds it
	require.NoError(t, err)
	t.Cleanup(func() { _ = unlock() })

	e.mgr = agents.New(e.cfg) // a new process
	s, ev = e.openID(t, parentID, false)
	_, err = s.Submit("again")
	require.NoError(t, err)
	assert.Equal(t, "second done", ev.finished().Answer)

	outputs := lastOutputs(e)
	assert.Contains(t, outputs, "is in use by another run, so it cannot be resumed now")
	assert.NotContains(t, outputs, "pending_init")
	assert.Contains(t, outputs, "is not loaded; resume it with resume_agent first", "send_input reports the message as not delivered")
	assert.NotContains(t, outputs, "submission_id")
}

// TestReview_MainAgentKnows tells the main agent, in a run that starts
// while a /review runs, that the review is the user's and which agent its
// reviewer is, so it does not go looking for it; the note is not the
// user's message.
func TestReview_MainAgentKnows(t *testing.T) {
	gate := make(chan struct{})
	e := newEnv(t, agents.Config{}, fakellm.Reply{Text: "it is reviewing"}, fakellm.Reply{Text: "ok"})
	e.llm.Route(uncommitted, fakellm.Reply{Gate: gate, Text: reviewAnswer})
	s, ev := e.open(t, false)

	done := make(chan error, 1)
	go func() { done <- s.Review(context.Background(), codereview.Target{Kind: codereview.Uncommitted}) }()
	ev.until("ReviewStarted", func(x core.Event) bool { _, ok := x.(session.ReviewStarted); return ok })
	_, err := s.Submit("what is the review doing?")
	require.NoError(t, err)
	assert.Equal(t, "it is reviewing", ev.finished().Answer)
	main := mainRequest(e, "what is the review doing?")
	assert.True(t, slices.ContainsFunc(main.DeveloperTexts, func(s string) bool { return strings.Contains(s, "A /review the user started is still running") }))
	assert.Equal(t, []string{"what is the review doing?"}, main.UserTexts)

	close(gate)
	require.NoError(t, <-done)
	ev.reviewFinished()
	_, err = s.Submit("thanks")
	require.NoError(t, err)
	ev.finished()
	notes := 0
	for _, d := range mainRequest(e, "thanks").DeveloperTexts {
		notes += strings.Count(d, "is still running")
	}
	assert.Equal(t, 1, notes, "once it ended, no new note")
}

// mainRequest is the main agent's last request with the message, not the
// reviewer's, which runs beside it.
func mainRequest(e *env, message string) fakellm.Request {
	var last fakellm.Request
	for _, r := range e.llm.Requests() {
		if slices.Contains(r.UserTexts, message) {
			last = r
		}
	}

	return last
}
