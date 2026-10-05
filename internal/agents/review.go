package agents

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/approval"
	"github.com/viktordanov/uah/internal/codereview"
	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/instructions"
	"github.com/viktordanov/uah/internal/session"
)

var _ session.Reviewer = (*Manager)(nil)

// reviewTools are the reviewer's tools: commands, read-only, to read the
// changes and the code, and images. No apply_patch, no MCP, and no agent
// tools (a child is never offered them).
var reviewTools = []string{"Bash", "ViewImage"}

// ReviewSettings are a reviewer's settings, as Codex sets up its review
// thread: the parent's in read only mode, with Codex's rubric in place of
// the base instructions and the project's, then the parent's environment
// context, and the configured review model (the parent's by default).
func (m *Manager) ReviewSettings(parent session.Settings) session.Settings {
	s := parent.WithMode(approval.ModeReadOnly)
	s.SystemPrompt = instructions.HostPrompt(codereview.Instructions(), "", environment(parent.SystemPrompt))
	s.Model = first(m.cfg.ReviewModel, s.Model)

	return s
}

// Review runs one /review as Codex runs its review thread: a fresh
// session beside the parent with no history and the reviewer's settings
// (ReviewSettings), whose approvals are never asked. It sends the prompt,
// waits for the answer, and closes the session. The session is a child of
// the parent (its sidecar says so, and `uah sessions` lists it), but not
// one of the parent's agents: the agent tools and /agents never see it.
func (m *Manager) Review(ctx context.Context, req session.ReviewRequest) (session.ReviewAnswer, error) {
	m.mu.Lock()
	eng, opts := m.eng, m.tmpl
	id := session.NewSubagentID()
	m.parentIDs[id] = req.ParentID // no spawn tools, no root-only hooks
	m.mu.Unlock()
	if eng == nil {
		return session.ReviewAnswer{}, errors.New("subagents are not available in this session")
	}
	opts.ID, opts.Resumed, opts.Source, opts.Parent = id, false, session.SourceSubagent, req.ParentID
	opts.Hooks, opts.Stream, opts.Interactive, opts.Shell = opts.Hooks.Clone(), false, false, nil
	opts.Ask = func(context.Context, approval.Prompt) approval.Answer { return approval.Decline }
	opts.Instructions, opts.Notices, opts.Settings = nil, nil, req.Settings
	if ce, ok := eng.(childEngine); ok {
		if sc, ok := ce.Engine.(engine.Scoper); ok {
			sc.SetScope(id, engine.Scope{Tools: reviewTools, NeverAsk: true})
		}
	}

	// The session outlives ctx long enough to stop gracefully and close.
	rs, err := session.Open(context.WithoutCancel(ctx), eng, opts)
	if err != nil {
		return session.ReviewAnswer{}, fmt.Errorf("failed to start the reviewer: %w", err)
	}
	defer func() {
		go func() {
			// Drained, so the session's loop never blocks while it closes.
			for e := range rs.Events() {
				_ = e
			}
		}()
		_ = rs.Close()
	}()
	if _, err := rs.Submit(req.Prompt); err != nil {
		return session.ReviewAnswer{}, fmt.Errorf("failed to start the review: %w", err)
	}

	return awaitReview(ctx, rs, req.Activity)
}

// environment is the <environment_context> block that ends the parent's
// system prompt, or "": Codex's review thread gets the environment context
// and the prompt, and none of the parent's instructions.
func environment(prompt string) string {
	if i := strings.Index(prompt, instructions.EnvironmentOpen); i >= 0 {
		return prompt[i:]
	}

	return ""
}

// awaitReview follows the reviewer's session until its run ends and
// returns its answer. When ctx ends first, it interrupts the run and
// waits for it to stop.
func awaitReview(ctx context.Context, rs *session.Session, activity func(core.Event)) (session.ReviewAnswer, error) {
	w := reviewWatch{activity: activity}
	done := ctx.Done()
	for {
		select {
		case <-done:
			done = nil
			go func() { _ = rs.Interrupt() }()
		case e, ok := <-rs.Events():
			if !ok {
				return session.ReviewAnswer{}, errors.New("the reviewer's session closed")
			}
			if w.observe(e) {
				return w.answer(ctx)
			}
		}
	}
}

// reviewWatch folds the reviewer's events: its tool events, a failed
// command's output, and its model responses (their tokens) go to
// activity, and its run's result and failure are kept.
type reviewWatch struct {
	activity func(core.Event)
	result   *core.Result
	cause    string
}

// observe takes one event and reports whether the review ended.
func (w *reviewWatch) observe(e core.Event) bool {
	switch e := e.(type) {
	case core.ToolCalled, core.ToolStarted, core.ToolFinished, core.ModelResponded, engine.ToolOutput:
		if w.activity != nil {
			w.activity(e)
		}
	case core.RunnerError:
		w.cause = e.Message
	case session.InputFailed:
		w.cause = e.Reason
	case session.Notice:
		if e.Level == session.LevelError {
			w.cause = e.Message
		}
	case core.RunFinished:
		r := e.Result
		w.result = &r
	case session.Idle:
		return w.result != nil || w.cause != ""
	}

	return false
}

// answer is the reviewer's last message, or why there is none, with the
// tokens its run used either way.
func (w *reviewWatch) answer(ctx context.Context) (session.ReviewAnswer, error) {
	r := w.result
	var a session.ReviewAnswer
	if r != nil {
		a.Tokens = r.Stats.Tokens
	}
	switch {
	case ctx.Err() != nil:
		return a, fmt.Errorf("the review stopped: %w", ctx.Err())
	case r != nil && r.Status == core.StatusOK:
		a.Text = r.Answer

		return a, nil
	case w.cause != "":
		return a, errors.New(readable(w.cause))
	case r != nil:
		return a, errors.New(strings.TrimSpace(fmt.Sprintf("the review ended with status %s. %s", r.Status, r.Answer)))
	}

	return a, errors.New("the reviewer did not answer")
}
