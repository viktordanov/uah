package embedded

import (
	"cmp"
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
	"github.com/viktordanov/uah/internal/mcp"
	"github.com/viktordanov/uah/internal/review"
)

// keepEntries is how many of the session's latest transcript entries the
// auto-reviewer may see, besides the first user message; the reviewer
// trims them to its own budget.
const keepEntries = 200

// transcript keeps what the auto-reviewer needs from the session's events,
// in order: the user's messages and answers to the agent's questions
// (trusted), and the tool calls and their short results without output
// (untrusted). It also holds the session's review conversation, which
// continues across the session's runs.
type transcript struct {
	mu      sync.Mutex
	entries []review.Entry
	// start is the number of entries[0] in the session.
	start int
	// first is the session's first user message, entry firstAt.
	first   *review.Entry
	firstAt int
	// open are the calls recorded and not yet finished, by call ID.
	open   map[string]bool
	onUser func() // a new user turn closes the reviewer's circuit breaker

	conv review.Conversation
}

func newTranscript() *transcript { return &transcript{open: map[string]bool{}} }

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
		// The user's words without the MCP resources the message names,
		// which a server wrote.
		t.addLocked(review.Entry{Kind: review.EntryUser, Text: mcp.WithoutResources(v.Text)})
		if t.onUser != nil {
			t.onUser()
		}
	case engine.QuestionsAnswered:
		// The user's own words, as Codex keeps request_user_input answers
		// for its guardian (VerifiedAnswer).
		if text := answeredText(v); text != "" {
			t.addLocked(review.Entry{Kind: review.EntryUser, Text: text})
		}
	case core.ToolCalled:
		t.callLocked(v.CallID, v.Name, v.Arguments)
	case core.ToolFinished:
		delete(t.open, v.CallID)
		t.addLocked(review.Entry{Kind: review.EntryResult, Tool: v.Name, Text: cmp.Or(v.Detail, "done")})
	}
}

// note records a model response's calls before their events arrive: the
// calls' approvals start at once (prefetch.go), and each review sees all
// of the response's calls, as a review in turn after the events would.
func (t *transcript) note(calls []llm.ToolCall) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, c := range calls {
		t.callLocked(c.CallID, c.Name, c.Arguments)
	}
}

// callLocked records a call once.
func (t *transcript) callLocked(id, name, arguments string) {
	if t.open[id] {
		return
	}
	t.open[id] = true
	t.addLocked(review.Entry{Kind: review.EntryCall, Tool: name, Text: arguments})
}

// addLocked appends an entry, keeping the first user message and the
// latest keepEntries.
func (t *transcript) addLocked(e review.Entry) {
	if t.first == nil && e.Kind == review.EntryUser {
		t.first, t.firstAt = &e, t.start+len(t.entries)
	}
	t.entries = append(t.entries, e)
	if drop := len(t.entries) - keepEntries; drop > 0 {
		t.entries = slices.Delete(t.entries, 0, drop)
		t.start += drop
	}
}

func (t *transcript) snapshot() review.Transcript {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := review.Transcript{Entries: slices.Clone(t.entries), Start: t.start}
	if t.first != nil && t.firstAt < t.start {
		first := *t.first
		out.First, out.FirstAt = &first, t.firstAt
	}

	return out
}

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
	// The reviewer's commands are set up at its first review.
	run := sync.OnceValue(func() review.Runner { return w.reviewCommands(req) })

	return func(ctx context.Context, p approval.Prompt) approval.Answer {
		alone := mode.get().ReviewerDecides()
		if !alone && !always {
			return askNext(ctx, next, p)
		}
		start := time.Now()
		if emit != nil {
			emit(engine.AutoReviewing{At: start, Command: p.Command})
		}
		v, err := rv.Review(ctx, reviewRequest(t, req, p, mode.get(), run()))
		if err != nil {
			v = review.Verdict{Outcome: "error", Reason: err.Error()}
		}
		if emit != nil {
			emit(engine.AutoReviewed{
				At: time.Now(), Command: p.Command, Outcome: string(v.Outcome), Risk: string(v.Risk), Reason: v.Reason,
				Duration: time.Since(start), Delta: v.Delta, Forked: v.Forked, Commands: v.Commands,
				InputTokens: v.Usage.InputTokens, CachedInputTokens: v.Usage.CachedInputTokens, OutputTokens: v.Usage.OutputTokens,
			})
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
// session's transcript, in the session's review conversation.
func reviewRequest(t *transcript, req core.Request, p approval.Prompt, mode approval.Mode, run review.Runner) review.Request {
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

	return review.Request{Action: action, Transcript: t.snapshot(), SessionID: req.SessionID, Conversation: &t.conv, Run: run}
}
