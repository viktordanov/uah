// Package review is the automatic approval reviewer, Codex's "auto-review"
// (guardian): a model judges an action that needs approval, from the
// session's transcript (the user's messages, trusted; the tool calls and
// their results without output, untrusted) and the action itself, and may
// run read-only commands first. Each session keeps one review conversation,
// to which each review appends only what changed since the last. It fails
// closed, and a circuit breaker hands the decision back to the user after
// repeated denials.
//
// The prompts in prompts/ are Codex's (rust-v0.156.1,
// codex-rs/prompts/templates/guardian), Apache License 2.0, Copyright 2025
// OpenAI, trimmed of what uah's reviewer cannot do; see
// prompts/LICENSE-codex.
package review

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/viktordanov/uah-core/harness/llm"

	"github.com/viktordanov/uah/internal/llmcall"
)

// DefaultTimeout bounds one review, as Codex's REVIEW_TIMEOUT does.
const DefaultTimeout = 90 * time.Second

// maxAttempts is how often a review runs when the model's answer does not
// parse, as Codex's MAX_REVIEW_ATTEMPTS. Transport errors are retried by
// the client itself.
const maxAttempts = 3

// Outcome is the reviewer's decision.
type Outcome string

const (
	// Allow runs the action.
	Allow Outcome = "allow"
	// Deny refuses the action; the reason goes back to the model.
	Deny Outcome = "deny"
	// AskUser means the circuit breaker is open: the user decides (headless
	// runs deny).
	AskUser Outcome = "ask_user"
)

// Risk is the reviewer's risk level.
type Risk string

// The risk levels, as in Codex.
const (
	RiskLow      Risk = "low"
	RiskMedium   Risk = "medium"
	RiskHigh     Risk = "high"
	RiskCritical Risk = "critical"
)

// Action is what the agent wants to do.
type Action struct {
	// Tool is the tool name, such as Bash.
	Tool    string
	Command string
	Cwd     string
	// SandboxMode is the session's sandbox mode, such as workspace-write.
	SandboxMode string
	// SandboxPermissions is what the call asked for, such as
	// require_escalated.
	SandboxPermissions string
	// Justification is the agent's reason for asking.
	Justification string
	// Denied is what the sandbox denied on the first run, if it ran.
	Denied string
	// Rule is the rule context, such as the rule that asked for approval.
	Rule string
}

// Request is one review.
type Request struct {
	Action Action
	// Transcript is the session so far. Its user entries are the only
	// trusted input.
	Transcript Transcript
	// Policy replaces the default security policy when set, as Codex's
	// [auto_review] policy does.
	Policy string
	// SessionID keys the provider's prompt cache.
	SessionID string
	// Conversation, when set, is the session's review conversation, which
	// the review continues; without one, each review starts anew.
	Conversation *Conversation
	// Run, when set, runs the reviewer's read-only commands; without it,
	// the reviewer has no tools.
	Run Runner
}

// Verdict is the result of a review.
type Verdict struct {
	Outcome Outcome
	Risk    Risk
	// Authorization is the reviewer's user_authorization: unknown, low,
	// medium, or high.
	Authorization string
	Reason        string
	// Failed means the review did not finish (an error, a timeout, or an
	// answer that did not parse), so the verdict is a fail-closed deny.
	Failed bool
	Usage  llm.Usage
	// Delta means the review continued its session's conversation with
	// what changed since the last review; else it sent the whole
	// transcript. Forked means another review held the conversation, so
	// this one continued a copy of it and was dropped. Commands counts
	// the reviewer's commands.
	Delta    bool
	Forked   bool
	Commands int
}

// Config is the reviewer's model, budget, and policy.
type Config struct {
	GuardianMarkers bool
	Model           string
	Effort          llm.ReasoningEffort
	// Policy replaces the default security policy for every request that
	// sets none; PolicyFile is the file it was read from ([review]
	// policy_file), for display.
	Policy     string
	PolicyFile string
	// Timeout bounds a review (DefaultTimeout when zero).
	Timeout time.Duration
	Limits  Limits
}

// Reviewer reviews actions over one model adapter. It is safe for
// concurrent use.
type Reviewer struct {
	adapter llm.Adapter
	cfg     Config

	mu      sync.Mutex
	breaker Breaker
}

// New returns a reviewer that calls adapter.
func New(adapter llm.Adapter, cfg Config) *Reviewer {
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.Limits == (Limits{}) {
		cfg.Limits = DefaultLimits
	}

	return &Reviewer{adapter: adapter, cfg: cfg}
}

// Review judges the request. A failed review is a deny with Failed set;
// the error is only for a cancelled ctx. When the circuit breaker is open,
// it returns AskUser without calling the model.
func (r *Reviewer) Review(ctx context.Context, req Request) (Verdict, error) {
	r.mu.Lock()
	open := r.breaker.Open()
	r.mu.Unlock()
	if open {
		return Verdict{Outcome: AskUser, Reason: "auto-review denied too many actions in a row; the user decides"}, nil
	}
	v := r.review(ctx, req)
	if ctx.Err() != nil {
		return Verdict{}, fmt.Errorf("failed to review: %w", ctx.Err())
	}
	r.mu.Lock()
	r.breaker.Record(v.Outcome == Deny)
	r.mu.Unlock()

	return v, nil
}

// Reset closes the circuit breaker, as Codex does at each new turn.
func (r *Reviewer) Reset() {
	r.mu.Lock()
	r.breaker = Breaker{}
	r.mu.Unlock()
}

func (r *Reviewer) review(ctx context.Context, req Request) Verdict {
	instructions := Instructions(cmp.Or(req.Policy, r.cfg.Policy), req.Run != nil)
	conv := req.Conversation
	if conv == nil {
		conv = &Conversation{}
	}
	cp, trunk := conv.begin(settingsKey(r.cfg, instructions))
	var next *checkpoint
	if trunk {
		defer func() { conv.end(next) }()
	}
	base := cp.history
	delta := len(base) > 0
	if delta && !cp.reminded {
		base = append(base, llmcall.Message(llm.RoleDeveloper, followupReminder))
	}
	msg := Render(req, r.cfg.Limits)
	if delta {
		msg = RenderDelta(req, cp.cursor, r.cfg.Limits)
	}
	base = append(base, llmcall.Message(llm.RoleUser, msg))
	call := llmcall.Request{Model: r.cfg.Model, Effort: r.cfg.Effort, Instructions: instructions}
	if req.Run != nil {
		call.Tools = []llm.Tool{execTool}
	}
	if req.SessionID != "" {
		call.CacheKey = "uah-review-" + req.SessionID
	}
	deadline := time.Now().Add(r.cfg.Timeout)
	var usage llm.Usage
	var err error
	commands := 0
	for range maxAttempts {
		var t turn
		t, err = r.turn(ctx, call, base, req.Run, deadline)
		usage = addUsage(usage, t.usage)
		commands += t.commands
		if err != nil && !errors.Is(err, llmcall.ErrNoText) {
			break
		}
		if err == nil {
			var v Verdict
			if v, err = Parse(t.text); err == nil {
				v.Usage, v.Delta, v.Forked, v.Commands = usage, delta, !trunk, commands
				next = &checkpoint{
					key: cp.key, history: append(base, t.items...), cursor: req.Transcript.End(),
					reviews: cp.reviews + 1, reminded: delta || cp.reminded, tokens: t.lastInput,
				}

				return v
			}
		}
		if time.Until(deadline) <= 0 {
			break
		}
	}
	v := failed(err, usage)
	v.Delta, v.Forked, v.Commands = delta, !trunk, commands

	return v
}

// turn is one attempt's model calls and commands.
type turn struct {
	// items are the attempt's output: the model's items and the commands'
	// results.
	items    []llm.Item
	text     string
	usage    llm.Usage
	commands int
	// lastInput is the last call's input tokens.
	lastInput int64
}

// turn calls the model on base until it answers in text, running the
// commands it asks for on the way.
func (r *Reviewer) turn(ctx context.Context, call llmcall.Request, base []llm.Item, run Runner, deadline time.Time) (turn, error) {
	var t turn
	for round := 0; ; round++ {
		call.Input = slices.Concat(base, t.items)
		call.Timeout = time.Until(deadline)
		res, err := llmcall.Call(ctx, r.adapter, call)
		t.usage = addUsage(t.usage, res.Usage)
		t.lastInput = res.Usage.InputTokens
		if err != nil {
			return t, err
		}
		t.items = append(t.items, res.Output...)
		if len(res.Calls) == 0 || run == nil {
			t.text = res.Text

			return t, nil
		}
		if round+1 == maxRounds {
			return t, fmt.Errorf("the reviewer ran commands for %d rounds without answering", maxRounds)
		}
		for _, c := range res.Calls {
			t.items = append(t.items, runCall(ctx, run, c, r.cfg.Limits))
			t.commands++
		}
	}
}

// settingsKey identifies what a conversation was made with: a review with
// other settings starts a new one.
func settingsKey(cfg Config, instructions string) string {
	sum := sha256.Sum256([]byte(instructions))

	return cfg.Model + "\x00" + string(cfg.Effort) + "\x00" + hex.EncodeToString(sum[:8])
}

// failed is the fail-closed verdict for a review that did not finish.
func failed(err error, usage llm.Usage) Verdict {
	reason := "auto-review failed, so the action is denied: " + err.Error()
	if errors.Is(err, context.DeadlineExceeded) {
		reason = "auto-review did not finish before its deadline, so the action is denied. " +
			"Do not assume the action is unsafe based on the timeout alone; retry once, or ask the user."
	}

	return Verdict{Outcome: Deny, Risk: RiskHigh, Reason: reason, Failed: true, Usage: usage}
}

func addUsage(a, b llm.Usage) llm.Usage {
	a.InputTokens += b.InputTokens
	a.CachedInputTokens += b.CachedInputTokens
	a.OutputTokens += b.OutputTokens
	a.ReasoningTokens += b.ReasoningTokens

	return a
}
