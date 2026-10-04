package state

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/viktordanov/uah/internal/engine"

	"github.com/viktordanov/uah/internal/approval"
	"github.com/viktordanov/uah/internal/session"
)

// Approval is a command waiting for the user's approval. The first pending
// one shows as the approval overlay.
type Approval struct {
	ID            string
	Command       string
	Justification string
	// Escalation is true when the command would run outside the sandbox.
	Escalation bool
	// Prefix, when set, offers "don't ask again" for it.
	Prefix []string
	// MCPTool, when set, offers "don't ask again" for this MCP tool.
	MCPTool string
	// GrantRoot, when set, offers to allow writes to it for the session.
	GrantRoot string
	// Answered hides the choices once the answer is on its way.
	Answered bool
	Since    time.Time
}

// Answer is the user's choice in the approval overlay.
type Answer struct{ Answer approval.Answer }

// EffResolve answers a pending approval.
type EffResolve struct {
	ID     string
	Answer approval.Answer
}

func (EffResolve) effect() {}

// PendingApproval is the approval the overlay shows, if any.
func (s State) PendingApproval() (Approval, bool) {
	if len(s.Approvals) == 0 {
		return Approval{}, false
	}

	return s.Approvals[0], true
}

func (s *State) requestApproval(e session.ApprovalRequested) {
	s.Approvals = append(s.Approvals, Approval{
		ID: e.ID, Command: e.Command, Justification: e.Justification, Escalation: e.Escalation, Prefix: e.ProposedPrefix,
		MCPTool: e.MCPTool, GrantRoot: e.GrantRoot, Since: e.At,
	})
	s.Scroll = 0
}

// resolveApproval closes the approval and records the answer in the
// transcript, as Codex does.
func (s *State) resolveApproval(e session.ApprovalResolved) {
	i := slices.IndexFunc(s.Approvals, func(a Approval) bool { return a.ID == e.ID })
	if i < 0 {
		return
	}
	a := s.Approvals[i]
	s.Approvals = slices.Delete(s.Approvals, i, i+1)
	command := oneLine(a.Command)
	switch e.Decision {
	case approval.Approve:
		s.notice(session.LevelInfo, "✔ approved: "+command)
	case approval.ApprovePrefix:
		s.notice(session.LevelInfo, "✔ approved, and from now on commands that start with `"+strings.Join(a.Prefix, " ")+"`: "+command)
	case approval.ApproveTool:
		s.notice(session.LevelInfo, "✔ approved, and from now on the tool "+a.MCPTool+": "+command)
	case approval.ApproveGrant:
		s.notice(session.LevelInfo, "✔ approved, and writes to "+a.GrantRoot+" for this session: "+command)
	case approval.Decline:
		s.notice(session.LevelWarning, "✗ declined: "+command)
	}
}

// answer sends the user's choice for the pending approval once.
func (s *State) answer(e Answer) (State, []Effect) {
	if len(s.Approvals) == 0 || s.Approvals[0].Answered {
		return *s, nil
	}
	a := &s.Approvals[0]
	if (e.Answer == approval.ApprovePrefix && len(a.Prefix) == 0) || (e.Answer == approval.ApproveTool && a.MCPTool == "") ||
		(e.Answer == approval.ApproveGrant && a.GrantRoot == "") {
		return *s, nil
	}
	a.Answered = true

	return *s, []Effect{EffResolve{ID: a.ID, Answer: e.Answer}}
}

func oneLine(text string) string { return strings.Join(strings.Fields(text), " ") }

// onAutoReviewed shows the auto-reviewer's verdict: an approval under the
// call it approved (a line of its own when the call is not in the
// transcript), a line for what it denied, and nothing extra when it left
// the choice to the user.
func (s *State) onAutoReviewed(e engine.AutoReviewed) {
	switch e.Outcome {
	case "allow":
		if s.attachApproval(e.Command, fmt.Sprintf("auto-approved · %s risk · %s", e.Risk, oneLine(e.Reason))) {
			return
		}
		s.notice(session.LevelInfo, fmt.Sprintf("auto-approved (%s risk): %s — %s", e.Risk, oneLine(e.Command), e.Reason))
	case "deny":
		s.notice(session.LevelWarning, fmt.Sprintf("auto-review denied (%s risk): %s — %s", e.Risk, oneLine(e.Command), e.Reason))
	}
}
