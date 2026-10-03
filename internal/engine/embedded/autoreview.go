package embedded

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/viktordanov/uah-core/harness/llm"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/approval"
	"github.com/viktordanov/uah/internal/contextprep"
	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/goal"
	"github.com/viktordanov/uah/internal/review"
)

// How much of the session the auto-reviewer sees; the reviewer trims it to
// its own budget.
const (
	keepUserMessages = 20
	keepToolCalls    = 20
)

// transcript keeps what the auto-reviewer needs from the session's events:
// the user's messages and answers to the agent's questions (trusted), and
// the latest tool calls without their output (untrusted).
type transcript struct {
	mu     sync.Mutex
	users  []string
	calls  []recentCall
	onUser func() // a new user turn closes the reviewer's circuit breaker
}

type recentCall struct {
	id   string
	call review.ToolCall
}

func newTranscript() *transcript { return &transcript{} }

func (t *transcript) observe(e core.Event) {
	t.mu.Lock()
	defer t.mu.Unlock()
	switch v := e.(type) {
	case core.UserMessage:
		if contextprep.IsPrepared(v.Text) {
			return // uah's, not the user's, in a session from before the developer role
		}
		if kind, _ := goal.Parse(v.Text); kind != goal.KindNone && kind != goal.KindUser {
			return // the goal's continuation or steering, uah's; the user's goal change stays
		}
		t.users = keepLast(append(t.users, v.Text), keepUserMessages)
		if t.onUser != nil {
			t.onUser()
		}
	case engine.QuestionsAnswered:
		// The user's own words, as Codex keeps request_user_input answers
		// for its guardian (VerifiedAnswer).
		if text := answeredText(v); text != "" {
			t.users = keepLast(append(t.users, text), keepUserMessages)
		}
	case core.ToolCalled:
		t.addLocked(v.CallID, v.Name, v.Arguments)
	case core.ToolFinished:
		for i := range t.calls {
			if t.calls[i].id == v.CallID {
				t.calls[i].call.Status = v.Detail
			}
		}
	}
}

// note records a model response's calls before their events arrive: the
// calls' approvals start at once (prefetch.go), and each review sees all
// of the response's calls, as a review in turn after the events would.
func (t *transcript) note(calls []llm.ToolCall) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, c := range calls {
		t.addLocked(c.CallID, c.Name, c.Arguments)
	}
}

// addLocked records a call once.
func (t *transcript) addLocked(id, name, arguments string) {
	if slices.ContainsFunc(t.calls, func(c recentCall) bool { return c.id == id }) {
		return
	}
	t.calls = keepLast(append(t.calls, recentCall{id: id, call: review.ToolCall{Name: name, Arguments: arguments}}), keepToolCalls)
}

func (t *transcript) snapshot() (users []string, calls []review.ToolCall) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, c := range t.calls {
		calls = append(calls, c.call)
	}

	return append([]string(nil), t.users...), calls
}

func keepLast[T any](s []T, n int) []T { return s[max(len(s)-n, 0):] }

// answeredText is the user's answers to the agent's questions, as Codex
// words a verified answer: each question, the chosen option's description,
// and the answer; "" when nothing was answered.
func answeredText(e engine.QuestionsAnswered) string {
	var b strings.Builder
	for _, q := range e.Questions {
		answers := slices.DeleteFunc(slices.Clone(e.Answers[q.ID].Answers), func(a string) bool { return strings.TrimSpace(a) == "" })
		if len(answers) == 0 {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("Question: " + q.Question)
		for _, o := range q.Options {
			if slices.Contains(answers, o.Label) {
				b.WriteString("\n" + o.Label + ": " + o.Description)
			}
		}
		b.WriteString("\nAnswer: " + strings.Join(answers, "\n"))
	}

	return b.String()
}

// reviewedAsk puts the auto-reviewer in front of the session's ask, as
// Codex's auto_review does: allow runs the action, deny refuses it with the
// reviewer's reason, and ask_user (the circuit breaker is open) leaves it to
// PermissionRequest hooks and the user. In Auto mode the reviewer decides
// alone, also when approvals_reviewer is user: ask_user declines with its
// reason, as Codex's "Approve for me" never asks. Without auto-review and
// outside Auto mode, the session's ask answers.
func (w *wiring) reviewedAsk(sw *switcher, req core.Request) approval.Ask {
	rv := review.New(sw.direct(), w.e.cfg.Review)
	t := w.e.transcript(req.SessionID)
	t.mu.Lock()
	t.onUser = rv.Reset
	t.mu.Unlock()
	next, emit, mode, always := w.ask, w.emit, w.mode, w.e.cfg.AutoReview

	return func(ctx context.Context, p approval.Prompt) approval.Answer {
		alone := mode.get().ReviewerDecides()
		if !alone && !always {
			return askNext(ctx, next, p)
		}
		if emit != nil {
			emit(engine.AutoReviewing{At: time.Now(), Command: p.Command})
		}
		v, err := rv.Review(ctx, reviewRequest(t, req, p, mode.get()))
		if err != nil {
			v = review.Verdict{Outcome: "error", Reason: err.Error()}
		}
		if emit != nil {
			emit(engine.AutoReviewed{At: time.Now(), Command: p.Command, Outcome: string(v.Outcome), Risk: string(v.Risk), Reason: v.Reason})
		}
		if err != nil {
			return approval.Decline
		}
		switch v.Outcome {
		case review.Allow:
			return approval.Approve
		case review.Deny:
			return approval.DeclineBecause("the auto-reviewer denied this (" + string(v.Risk) + " risk): " + v.Reason + ". Do not retry it; tell the user if it is needed.")
		case review.AskUser:
		}
		if alone {
			return approval.DeclineBecause("in auto mode the auto-reviewer decides, and it did not allow this: " + v.Reason + ". Do not retry it; tell the user if it is needed.")
		}

		return askNext(ctx, next, p)
	}
}

// askNext asks the session, or declines when no user can answer.
func askNext(ctx context.Context, next approval.Ask, p approval.Prompt) approval.Answer {
	if next == nil {
		return approval.DeclineBecause("the auto-reviewer left this to the user, and no user can approve it in this headless run.")
	}

	return next(ctx, p)
}

// reviewRequest is what the reviewer judges: the action with the
// session's recent messages and tool calls.
func reviewRequest(t *transcript, req core.Request, p approval.Prompt, mode approval.Mode) review.Request {
	users, calls := t.snapshot()
	action := review.Action{Tool: "Bash", Command: p.Command, Cwd: p.Cwd, Justification: p.Justification}
	if mode != "" {
		action.SandboxMode = string(mode.Sandbox())
	}
	if name, args, _ := strings.Cut(p.Command, " "); strings.HasPrefix(name, "mcp__") {
		action.Tool, action.Command = name, args
	}
	if p.Tool != "" {
		action.Tool, action.Command = p.Tool, string(p.Input)
	}
	if p.Escalation {
		action.SandboxPermissions = permEscalated
	}

	return review.Request{Action: action, UserMessages: users, RecentCalls: calls, SessionID: req.SessionID}
}
