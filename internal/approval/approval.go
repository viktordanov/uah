// Package approval decides how a command runs: in the sandbox, outside it,
// or not at all. It applies the command rules and the approval policy and,
// when a command needs approval, asks the user through the session.
package approval

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/viktordanov/uah/internal/rules"
)

// Policy is when the user is asked, as Codex's approval_policy.
type Policy string

const (
	// OnRequest asks the user when the model asks for escalation or a rule
	// says prompt. It is the default.
	OnRequest Policy = "on-request"
	// Never asks no one: what needs approval is denied.
	Never Policy = "never"
)

// Policies are the names --ask and approval_policy accept.
var Policies = []string{string(OnRequest), string(Never)}

// ParsePolicy reads a policy name; empty is OnRequest. Codex's alias
// on-failure is OnRequest too.
func ParsePolicy(s string) (Policy, error) {
	switch s {
	case "", string(OnRequest), "on-failure":
		return OnRequest, nil
	case string(Never):
		return Never, nil
	}

	return "", fmt.Errorf("invalid approval policy %q (want on-request or never)", s)
}

// Request is a command the model wants to run.
type Request struct {
	Command string
	Cwd     string
	// Escalated means the model asked to run outside the sandbox.
	Escalated     bool
	Justification string
	// PrefixRule is the model's suggested prefix for "don't ask again".
	PrefixRule []string
	// NoSandbox means no sandbox is available: every command that no rule
	// allows needs approval to run.
	NoSandbox bool
	// Bypass is yolo mode: what would need approval runs unsandboxed
	// without asking anyone; a forbid rule still refuses, also a command
	// that may match it (Decide).
	Bypass bool
	// Approved means a PreToolUse hook allowed the call: what would need
	// approval runs as if the user approved it, without asking anyone, also
	// with the policy never or headless. Rules still apply first, so a
	// forbid rule refuses.
	Approved bool
	// Tool and Input name a tool call other than Bash that needs approval,
	// such as apply_patch, and its input for PermissionRequest hooks and
	// the auto-reviewer; Command then describes it for the rules and the
	// user.
	Tool  string
	Input json.RawMessage
}

// Run is how a decided command runs.
type Run int

const (
	// Deny does not run the command; Decision.Reason says why.
	Deny Run = iota
	// Sandboxed runs the command in the sandbox.
	Sandboxed
	// Unsandboxed runs the command outside the sandbox.
	Unsandboxed
)

// Decision is how to run a command.
type Decision struct {
	Run Run
	// Reason is what the model hears when the command is denied. When it
	// runs, Reason is a warning for the user, such as a rule that could not
	// be saved.
	Reason string
}

// Prompt is what the user is asked.
type Prompt struct {
	Command       string
	Cwd           string
	Justification string
	// Escalation is true when the model asked to run outside the sandbox;
	// false when a prompt rule matched.
	Escalation bool
	// ProposedPrefix, when set, offers "don't ask again" for it.
	ProposedPrefix []string
	// Tool and Input are Request.Tool and Request.Input: empty for Bash.
	Tool  string
	Input json.RawMessage
	// MCPTool, when set, is the qualified name (mcp__<server>__<tool>) of
	// the MCP tool asked about, and offers ApproveTool for it.
	MCPTool string
}

// Answer is the user's choice.
type Answer string

const (
	Approve Answer = "approve"
	// ApprovePrefix approves and allows the proposed prefix from now on.
	ApprovePrefix Answer = "approve_prefix"
	// ApproveTool approves an MCP tool call and sets the tool's
	// approval_mode to approve from now on, as Codex's "Allow and don't ask
	// me again".
	ApproveTool Answer = "approve_tool"
	Decline     Answer = "decline"
)

// declinePrefix starts a decline that carries its own reason.
const declinePrefix = "decline: "

// DeclineBecause declines with a reason the model hears, such as an
// auto-reviewer's, instead of "the user declined".
func DeclineBecause(reason string) Answer { return Answer(declinePrefix + reason) }

// Approved reports whether the answer lets the command run.
func (a Answer) Approved() bool { return a == Approve || a == ApprovePrefix || a == ApproveTool }

// DeclineReason is the reason a DeclineBecause answer carries.
func (a Answer) DeclineReason() (string, bool) {
	reason, ok := strings.CutPrefix(string(a), declinePrefix)

	return reason, ok
}

// Ask shows the prompt to the user and waits for the answer. It returns
// Decline when the context ends first.
type Ask func(ctx context.Context, p Prompt) Answer

// Config configures an Approver.
type Config struct {
	Policy Policy
	// Rules are the loaded command rules.
	Rules []rules.Rule
	// RulesFile is where "don't ask again" appends its rule.
	RulesFile string
}

// Approver decides commands. It is safe for concurrent use.
type Approver struct {
	cfg Config

	mu    sync.Mutex
	rules *rules.Policy
	// changes wakes the prompts open while "don't ask again" adds a rule.
	changes Changes
}

// New returns an approver.
func New(cfg Config) *Approver {
	if cfg.Policy == "" {
		cfg.Policy = OnRequest
	}

	return &Approver{cfg: cfg, rules: rules.New(cfg.Rules...)}
}

// Policy is the approval policy.
func (a *Approver) Policy() Policy { return a.cfg.Policy }

// Decide applies the rules and the policy, asking the user through ask when
// the command needs approval. A nil ask means no one can answer, as in a
// headless run, and such commands are denied.
func (a *Approver) Decide(ctx context.Context, req Request, ask Ask) Decision {
	commands, ok := rules.Split(req.Command)
	if d, denied := a.forbidsUnsplit(req.Command, ok); denied {
		return d
	}
	rule, matched := a.policy().Check(commands)
	if d, done := byRule(req, rule, matched); done {
		return d
	}
	if req.Bypass {
		return Decision{Run: Unsandboxed}
	}
	p := prompt(req, rule, matched, commands)
	run := Sandboxed
	if p.Escalation {
		run = Unsandboxed
	}
	if req.Approved {
		return Decision{Run: run}
	}
	if reason := a.cannotAsk(ask); reason != "" {
		return Decision{Run: Deny, Reason: reason}
	}
	var ruled Decision
	answer, settled := AskUnless(ctx, ask, p, &a.changes, func() bool {
		rule, matched := a.policy().Check(commands)
		var done bool
		ruled, done = byRule(req, rule, matched)

		return done
	})
	if settled {
		return ruled
	}

	return a.answer(answer, p, run)
}

// Forbidden reports whether a forbid rule refuses the command, or may
// refuse it (as Decide does for a command that does not split), and why.
// It applies no other rule and asks no one: a caller that runs some
// actions without Decide, such as a patch inside the writable roots or
// any patch in yolo mode, checks it first, so forbid rules refuse in every
// mode.
func (a *Approver) Forbidden(command string) (reason string, forbidden bool) {
	commands, ok := rules.Split(command)
	if d, denied := a.forbidsUnsplit(command, ok); denied {
		return d.Reason, true
	}
	if rule, matched := a.policy().Check(commands); matched && rule.Decision == rules.Forbidden {
		return forbiddenReason(rule), true
	}

	return "", false
}

// byRule decides without asking when a rule settles it, or when nothing
// needs approval; done is false when the user must be asked.
func byRule(req Request, rule rules.Rule, matched bool) (d Decision, done bool) {
	switch {
	case matched && rule.Decision == rules.Forbidden:
		return Decision{Run: Deny, Reason: forbiddenReason(rule)}, true
	case matched && rule.Decision == rules.Allow:
		return Decision{Run: Unsandboxed}, true
	case matched && rule.Decision == rules.Prompt:
		return Decision{}, false
	case !req.Escalated && !req.NoSandbox:
		return Decision{Run: Sandboxed}, true
	}

	return Decision{}, false
}

// prompt is what the user is asked, with a prefix for "don't ask again"
// when no rule matched.
func prompt(req Request, rule rules.Rule, matched bool, commands [][]string) Prompt {
	p := Prompt{
		Command: req.Command, Cwd: req.Cwd, Justification: req.Justification, Escalation: req.Escalated || req.NoSandbox,
		Tool: req.Tool, Input: req.Input,
	}
	if !matched {
		p.ProposedPrefix = proposePrefix(req.PrefixRule, commands)
	}
	if matched && p.Justification == "" {
		p.Justification = rule.Justification
	}

	return p
}

// answer turns the user's answer into the decision.
func (a *Approver) answer(answer Answer, p Prompt, run Run) Decision {
	switch answer {
	case Approve, ApproveTool:
		return Decision{Run: run}
	case ApprovePrefix:
		if len(p.ProposedPrefix) > 0 {
			if err := a.allow(p.ProposedPrefix); err != nil {
				return Decision{Run: run, Reason: err.Error()}
			}
		}

		return Decision{Run: run}
	case Decline:
	}
	if reason, ok := answer.DeclineReason(); ok {
		return Decision{Run: Deny, Reason: "not run: " + reason}
	}

	return Decision{Run: Deny, Reason: "not run: the user declined this command. Do not run it again; ask the user what to do instead."}
}

// cannotAsk says why no one can approve, or "" when the user can be asked.
func (a *Approver) cannotAsk(ask Ask) string {
	switch {
	case a.cfg.Policy == Never:
		return "not run: this command needs the user's approval, and the approval policy is never. " +
			"Work within the sandbox, or tell the user the command and why it is needed."
	case ask == nil:
		return "not run: this command needs the user's approval, and no user can approve it in this headless run. " +
			"Work within the sandbox, or report the command and why it is needed."
	}

	return ""
}

// forbidsUnsplit denies a command that does not split (split is false)
// when a forbidden rule matches, or may match, a simple command inside it.
// Check sees no words for such a command, so without this a redirect or a
// subshell would hide a forbidden command, and yolo would run it.
func (a *Approver) forbidsUnsplit(command string, split bool) (Decision, bool) {
	if split {
		return Decision{}, false
	}
	r, maybe, found := a.policy().Forbids(command)
	if !found {
		return Decision{}, false
	}
	if !maybe {
		return Decision{Run: Deny, Reason: forbiddenReason(r)}, true
	}
	reason := "not run: the command may run a command a rule forbids, and its words are not all plain text (a variable, glob, or substitution)"
	if r.Justification != "" {
		reason += "; the rule says: " + r.Justification
	}

	return Decision{Run: Deny, Reason: reason + ". Write the command out in plain words."}, true
}

func forbiddenReason(r rules.Rule) string {
	reason := "not run: a rule forbids this command"
	if r.Justification != "" {
		reason += ": " + r.Justification
	}

	return reason + "."
}

func (a *Approver) policy() *rules.Policy {
	a.mu.Lock()
	defer a.mu.Unlock()

	return a.rules
}

// allow appends an allow rule for the prefix to the rules file and applies
// it at once.
func (a *Approver) allow(prefix []string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	pattern := make([][]string, 0, len(prefix))
	for _, w := range prefix {
		pattern = append(pattern, []string{w})
	}
	r := rules.Rule{Pattern: pattern, Decision: rules.Allow}
	a.rules = a.rules.With(r) // it applies to this session even when the file cannot be written
	a.changes.Notify()
	if a.cfg.RulesFile == "" {
		return nil
	}
	_, err := rules.AppendAllow(a.cfg.RulesFile, prefix)

	return err // AppendAllow's errors name the file
}
