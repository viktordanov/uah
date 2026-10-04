// Package hooks runs user commands at session events, with Claude Code's
// contract: the event as JSON on stdin; exit 0 continues (optionally with
// JSON on stdout), exit 2 blocks with stderr as the reason, and any other
// exit is reported and ignored.
package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/viktordanov/uah/internal/patch"
)

// Event names a point where hooks run.
type Event string

const (
	SessionStart     Event = "SessionStart"
	SessionEnd       Event = "SessionEnd"
	UserPromptSubmit Event = "UserPromptSubmit"
	PreToolUse       Event = "PreToolUse"
	PostToolUse      Event = "PostToolUse"
	Stop             Event = "Stop"
	// SubagentStart runs when a subagent starts; it only observes.
	SubagentStart Event = "SubagentStart"
	// SubagentStop runs when a subagent finishes; a block with a reason
	// sends the reason to the subagent as its next message.
	SubagentStop Event = "SubagentStop"
	PreCompact   Event = "PreCompact"
	// PermissionRequest runs before the user is asked to approve a command
	// or an MCP call; "allow" or "deny" answers for the user.
	PermissionRequest Event = "PermissionRequest"
)

// Events are the supported events.
var Events = []Event{SessionStart, SessionEnd, UserPromptSubmit, PreToolUse, PostToolUse, Stop, SubagentStart, SubagentStop, PreCompact, PermissionRequest}

// RootOnly are the events that fire for root sessions only, as in Claude
// Code: a subagent's session fires none of them.
var RootOnly = []Event{SessionStart, SessionEnd, UserPromptSubmit, Stop}

const (
	// DefaultTimeout applies when a hook sets none.
	DefaultTimeout = 60 * time.Second
	// sessionEndTimeout caps SessionEnd hooks, so quitting stays quick.
	sessionEndTimeout = time.Second
)

// Source says which file a hook came from.
type Source string

const (
	SourceUser    Source = "user"
	SourceProject Source = "project"
)

// Hook is one configured command.
type Hook struct {
	Event Event
	// Matcher is a regular expression on the tool name for tool events; empty matches all.
	Matcher string
	Command string
	Timeout time.Duration
	Source  Source
}

// Outcome is how a hook run ended.
type Outcome string

const (
	OutcomeOK      Outcome = "ok"
	OutcomeBlocked Outcome = "blocked"
	OutcomeError   Outcome = "error"
	// OutcomeSkipped means a project hook that is not trusted yet.
	OutcomeSkipped Outcome = "skipped"
	// OutcomeRunning reports a hook as it starts; its result follows.
	OutcomeRunning Outcome = "running"
)

// Result is one hook run.
type Result struct {
	Hook     Hook
	Outcome  Outcome
	ExitCode int
	// Reason is stderr for exit 2, the error for OutcomeError, or why it was skipped.
	Reason   string
	Output   Output
	Stdout   string
	Duration time.Duration
}

// Runner runs the hooks of one session.
type Runner struct {
	hooks     []Hook
	trust     *Trust
	workspace string

	mu      sync.Mutex
	report  func(Result)
	parent  func(sessionID string) string
	skipped map[string]bool // untrusted commands already reported
	// failClosed blocks a call whose gating hook failed (FailClosed).
	failClosed bool
}

// New validates the hooks and returns a runner. Project hooks run only when
// trust has approved their exact command.
func New(hooks []Hook, trust *Trust, workspace string) (*Runner, error) {
	for _, h := range hooks {
		if !slices.Contains(Events, h.Event) {
			return nil, fmt.Errorf("unknown hook event %q", h.Event)
		}
		if strings.TrimSpace(h.Command) == "" {
			return nil, fmt.Errorf("a %s hook has no command", h.Event)
		}
		if _, err := regexp.Compile(h.Matcher); err != nil {
			return nil, fmt.Errorf("invalid %s hook matcher %q: %w", h.Event, h.Matcher, err)
		}
	}

	return &Runner{hooks: hooks, trust: trust, workspace: workspace}, nil
}

// Hooks returns the configured hooks.
func (r *Runner) Hooks() []Hook {
	if r == nil {
		return nil
	}

	return slices.Clone(r.hooks)
}

// Trusted reports whether a hook may run.
func (r *Runner) Trusted(h Hook) bool {
	ok, _ := r.TrustState(h)

	return ok
}

// TrustState reports whether a hook may run, and if not, why: a project hook
// that was never trusted, or whose command or script changed since.
func (r *Runner) TrustState(h Hook) (bool, string) {
	if h.Source != SourceProject {
		return true, ""
	}
	if r.trust == nil {
		return false, ReasonUntrusted
	}

	return r.trust.Check(r.workspace, h.Command)
}

// Clone returns a runner with the same hooks and trust and its own
// OnResult, for another session of the process, such as a subagent's. A nil
// runner stays nil.
func (r *Runner) Clone() *Runner {
	if r == nil {
		return nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	return &Runner{hooks: slices.Clone(r.hooks), trust: r.trust, workspace: r.workspace, parent: r.parent, failClosed: r.failClosed}
}

// Gates are the events whose hooks decide whether a tool call runs.
var Gates = []Event{PreToolUse, PermissionRequest}

// FailClosed makes a gating hook (Gates) that fails block its call: an
// error exit other than 2, a crash, a timeout, output that is not valid
// JSON, or a permissionDecision uah does not know. Without it, such a
// failure is reported and ignored, as in Claude Code and Codex. A session
// under a tool policy sets it. Clones made after the call share it.
func (r *Runner) FailClosed() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failClosed = true
}

// SetParents tells the runner which sessions are subagents: parent returns
// a subagent's parent session ID, or "" for a root session. A subagent then
// fires no RootOnly event, and its other payloads carry agent_id and
// parent_session_id. Clones made after the call share it.
func (r *Runner) SetParents(parent func(sessionID string) string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.parent = parent
}

// inAgent fills agent_id and parent_session_id when in fired inside a
// subagent, and reports false for an event a subagent does not fire. A
// payload that already names an agent, such as SubagentStop's, is kept.
func (r *Runner) inAgent(in *Input) bool {
	r.mu.Lock()
	parentOf := r.parent
	r.mu.Unlock()
	if parentOf == nil || in.AgentID != "" || in.SessionID == "" {
		return true
	}
	parent := parentOf(in.SessionID)
	if parent == "" {
		return true
	}
	if slices.Contains(RootOnly, in.Event) {
		return false
	}
	in.AgentID, in.ParentSessionID = in.SessionID, parent

	return true
}

// OnResult sets a function that sees every result, from any goroutine.
func (r *Runner) OnResult(fn func(Result)) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.report = fn
}

// Has reports whether any hook matches the event (and tool name, for tool events).
func (r *Runner) Has(event Event, tool string) bool {
	return len(r.matching(event, tool)) > 0
}

func (r *Runner) matching(event Event, tool string) []Hook {
	if r == nil {
		return nil
	}
	var out []Hook
	for _, h := range r.hooks {
		if h.Event != event {
			continue
		}
		if h.Matcher != "" && tool != "" && !matches(h.Matcher, tool) {
			continue
		}
		out = append(out, h)
	}

	return out
}

// matches reports whether a matcher matches the tool's name or one of its
// aliases: apply_patch also answers to Edit and Write, as in Codex.
func matches(matcher, tool string) bool {
	re := regexp.MustCompile("^(?:" + matcher + ")$")
	if re.MatchString(tool) {
		return true
	}

	return tool == patch.ToolName && slices.ContainsFunc(patch.HookAliases, re.MatchString)
}

// Run runs the matching hooks in order and returns their combined decision.
// Inside a subagent (SetParents), a RootOnly event runs nothing.
func (r *Runner) Run(ctx context.Context, in Input) Decision {
	var d Decision
	if r == nil || !r.inAgent(&in) {
		return d
	}
	for _, h := range r.matching(in.Event, in.ToolName) {
		ok, why := r.TrustState(h)
		res := Result{Hook: h, Outcome: OutcomeSkipped, Reason: why}
		r.mu.Lock()
		report := r.report
		if !ok {
			if r.skipped == nil {
				r.skipped = map[string]bool{}
			}
			if r.skipped[h.Command] {
				report = nil // said once is enough
			}
			r.skipped[h.Command] = true
		}
		r.mu.Unlock()
		if ok && report != nil {
			report(Result{Hook: h, Outcome: OutcomeRunning})
		}
		if ok {
			res = r.closed(in.Event, r.exec(ctx, h, in))
		}
		if report != nil {
			report(res)
		}
		d.add(in.Event, res)
		if d.Block {
			break
		}
	}

	return d
}

// closed turns a gating hook's failure into a block when the runner fails
// closed (FailClosed).
func (r *Runner) closed(event Event, res Result) Result {
	r.mu.Lock()
	closed := r.failClosed
	r.mu.Unlock()
	if !closed || !slices.Contains(Gates, event) {
		return res
	}
	switch {
	case res.Outcome == OutcomeError:
		res.Outcome, res.Reason = OutcomeBlocked, "the hook failed ("+res.Reason+"), and this session blocks a call whose hook fails"
	case res.Outcome == OutcomeOK && res.Output.HookSpecificOutput != nil && !slices.Contains(decisions, res.Output.HookSpecificOutput.PermissionDecision):
		res.Outcome, res.Reason = OutcomeBlocked, fmt.Sprintf("the hook answered permissionDecision %q, which uah does not know, and this session blocks a call whose hook fails", res.Output.HookSpecificOutput.PermissionDecision)
	}

	return res
}

// decisions are the permissionDecision values uah knows ("" is none).
var decisions = []string{"", "allow", "deny", "ask"}

// Decision combines the results of one event's hooks.
type Decision struct {
	// Block stops the prompt or the tool call, or (for Stop) keeps the agent
	// going with Reason as the next message.
	Block  bool
	Reason string
	// Allow is a hook's "allow" permission decision (PreToolUse,
	// PermissionRequest).
	Allow bool
	// UpdatedInput replaces a PreToolUse tool call's arguments.
	UpdatedInput json.RawMessage
	// Context is text to add to a prompt (UserPromptSubmit, SessionStart).
	Context []string
	// Messages are systemMessage texts to show the user.
	Messages []string
}

func (d *Decision) add(event Event, res Result) {
	if res.Output.SystemMessage != "" {
		d.Messages = append(d.Messages, res.Output.SystemMessage)
	}
	switch res.Outcome {
	case OutcomeBlocked:
		d.Block, d.Reason = true, res.Reason

		return
	case OutcomeOK:
	case OutcomeError, OutcomeSkipped:
		return
	}
	out := res.Output
	if out.Continue != nil && !*out.Continue && event != Stop && event != SubagentStop {
		d.Block, d.Reason = true, firstNonEmpty(out.StopReason, "stopped by a hook")

		return
	}
	if out.Decision == "block" {
		d.Block, d.Reason = true, firstNonEmpty(out.Reason, "blocked by a hook")

		return
	}
	if s := out.HookSpecificOutput; s != nil && d.addSpecific(*s) {
		return
	}
	if (event == UserPromptSubmit || event == SessionStart) && out == (Output{}) && strings.TrimSpace(res.Stdout) != "" {
		d.Context = append(d.Context, strings.TrimSpace(res.Stdout)) // plain stdout is context, as in Claude Code
	}
}

// addSpecific applies hookSpecificOutput and reports whether it blocked.
func (d *Decision) addSpecific(s SpecificOutput) bool {
	switch s.PermissionDecision {
	case "deny", "ask":
		d.Block, d.Reason = true, firstNonEmpty(s.PermissionDecisionReason, "denied by a hook")

		return true
	case "allow":
		d.Allow = true
	}
	if len(s.UpdatedInput) > 0 {
		d.UpdatedInput = s.UpdatedInput
	}
	if s.AdditionalContext != "" {
		d.Context = append(d.Context, s.AdditionalContext)
	}

	return false
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}

	return ""
}
