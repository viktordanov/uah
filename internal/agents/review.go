package agents

import (
	"context"
	"errors"
	"fmt"
	"strings"

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
// one of the parent's agents: its sidecar marks it as a review, and the
// agent tools and /agents never see it. The review ends at the
// reviewer's first answer, and its limits (ReviewLimits) bound it.
func (m *Manager) Review(ctx context.Context, req session.ReviewRequest) (session.ReviewAnswer, error) {
	m.mu.Lock()
	eng, opts, limits := m.eng, m.tmpl, m.cfg.ReviewLimits
	id := session.NewSubagentID()
	m.parentIDs[id] = req.ParentID // no spawn tools, no root-only hooks
	m.reviews[id] = true
	m.mu.Unlock()
	if eng == nil {
		return session.ReviewAnswer{}, errors.New("subagents are not available in this session")
	}
	opts.ID, opts.Resumed, opts.Source, opts.Parent, opts.Review = id, false, session.SourceSubagent, req.ParentID, true
	opts.Hooks, opts.Stream, opts.Interactive, opts.Shell = opts.Hooks.Clone(), false, false, nil
	opts.Ask = func(context.Context, approval.Prompt) approval.Answer { return approval.Decline }
	opts.Instructions, opts.Notices, opts.Settings, opts.FirstPrompt = nil, nil, req.Settings, ""
	setScope := func(engine.Scope) {}
	if ce, ok := eng.(childEngine); ok {
		if sc, ok := ce.Engine.(engine.Scoper); ok {
			setScope = func(s engine.Scope) { sc.SetScope(id, s) }
		}
	}
	setScope(engine.Scope{Tools: reviewTools, NeverAsk: true, CommandTimeout: limits.Command})
	defer setScope(engine.Scope{})

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
	// The last turn under a limit offers no tool, so it can only answer.
	noTools := func() {
		setScope(engine.Scope{Tools: reviewTools, NeverAsk: true, CommandTimeout: limits.Command, NoTools: true})
	}

	return awaitReview(ctx, rs, req.Activity, limits, noTools)
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
