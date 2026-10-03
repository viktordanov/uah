package embedded_test

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/approval"
	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/engine/embedded"
	"github.com/viktordanov/uah/internal/hooks"
	"github.com/viktordanov/uah/internal/mcp"
	"github.com/viktordanov/uah/internal/review"
	"github.com/viktordanov/uah/internal/rules"
	"github.com/viktordanov/uah/internal/sandbox"
	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/testing/fakellm"
	"github.com/viktordanov/uah/testing/harnesstest"
)

// approvalEnv is a workspace-write session whose commands go through an
// approver, and a directory outside the sandbox to write to.
type approvalEnv struct {
	*env
	outside   string
	rulesFile string
	s         *session.Session
	ev        *events
}

type approvalOpts struct {
	policy      approval.Policy
	rules       string
	interactive bool
	hooks       []hooks.Hook
	autoReview  bool
	// mode, when set, is the session's permission mode.
	mode approval.Mode
	// stream asks for the model's text as it arrives.
	stream bool
	// mcp configures the test MCP server.
	mcp bool
}

// newApprovalEnv opens the session; replies gets the outside directory.
func newApprovalEnv(t *testing.T, o approvalOpts, replies func(outside string) []fakellm.Reply) *approvalEnv {
	t.Helper()
	ws := t.TempDir()
	policy := sandbox.Policy{Mode: sandbox.WorkspaceWrite, Workspace: ws}
	if _, err := policy.Wrap([]string{"/bin/sh"}); err != nil {
		t.Skipf("no sandbox here: %v", err)
	}
	outside := harnesstest.OutsideDir(t, "uah-approval-")
	e := &approvalEnv{env: newEnv(t, replies(outside)...), outside: outside, rulesFile: filepath.Join(t.TempDir(), "rules", rules.DefaultFile)}
	e.Workspace = ws
	parsed, err := rules.Parse("test.rules", []byte(o.rules))
	require.NoError(t, err)

	var runner *hooks.Runner
	if len(o.hooks) > 0 {
		runner, err = hooks.New(o.hooks, nil, ws)
		require.NoError(t, err)
	}
	var m *mcp.Manager
	if o.mcp {
		m = mcpManager(t, e.env, mcp.ServerConfig{})
	}
	eng := embedded.New(embedded.Config{
		StateDir: e.StateDir, Provider: "openai", Getenv: e.getenv,
		Sandbox: &policy, SandboxDir: filepath.Join(e.StateDir, "sandbox"),
		Approver:   approval.New(approval.Config{Policy: o.policy, Rules: parsed, RulesFile: e.rulesFile}),
		AutoReview: o.autoReview, Review: review.Config{Model: "gpt-test"}, Hooks: runner, MCP: m,
	})
	settings := e.settings()
	if o.mode != "" {
		settings = settings.WithMode(o.mode)
	}
	e.s, err = session.Open(context.Background(), eng, session.Options{Settings: settings, Interactive: o.interactive, Hooks: runner, Stream: o.stream})
	require.NoError(t, err)
	t.Cleanup(func() { _ = e.s.Close() })
	e.ev = &events{t: t, s: e.s}

	return e
}

// approve waits for the next approval request and answers it.
func (e *approvalEnv) approve(t *testing.T, a approval.Answer) session.ApprovalRequested {
	t.Helper()
	req := e.ev.until("ApprovalRequested", isA[session.ApprovalRequested]).(session.ApprovalRequested)
	require.NoError(t, e.s.Resolve(req.ID, a))
	resolved := e.ev.until("ApprovalResolved", isA[session.ApprovalResolved]).(session.ApprovalResolved)
	assert.Equal(t, a, resolved.Decision)

	return req
}

func (e *approvalEnv) run(t *testing.T) {
	t.Helper()
	_, err := e.s.Submit("write outside the workspace")
	require.NoError(t, err)
}

// lastOutputs are the tool results of the last model request.
func (e *approvalEnv) lastOutputs() string {
	reqs := e.llm.Requests()

	return strings.Join(reqs[len(reqs)-1].ToolOutputs, "\n---\n")
}

func (e *approvalEnv) count(what func(core.Event) bool) int {
	n := 0
	for _, ev := range e.ev.all {
		if what(ev) {
			n++
		}
	}

	return n
}

func isRequested(ev core.Event) bool { _, ok := ev.(session.ApprovalRequested); return ok }

// escalate is a model that asks to create x.txt outside the sandbox, then
// finishes.
func escalate(outside string) []fakellm.Reply {
	return []fakellm.Reply{{Escalated: []string{"touch " + filepath.Join(outside, "x.txt")}}, {Text: "done"}}
}

func TestEmbedded_EscalationApproved(t *testing.T) {
	e := newApprovalEnv(t, approvalOpts{interactive: true}, escalate)
	target := filepath.Join(e.outside, "x.txt")
	e.run(t)

	req := e.approve(t, approval.Approve)

	assert.Equal(t, "touch "+target, req.Command)
	assert.Equal(t, "it needs the network", req.Justification)
	assert.True(t, req.Escalation)
	assert.Equal(t, []string{"touch", target}, req.ProposedPrefix)
	assert.Equal(t, core.StatusOK, e.ev.finished().Status)
	assert.FileExists(t, target, "the approved command ran outside the sandbox")
	assert.NoFileExists(t, e.rulesFile)
}

func TestEmbedded_EscalationDeclined(t *testing.T) {
	e := newApprovalEnv(t, approvalOpts{interactive: true}, escalate)
	e.run(t)

	e.approve(t, approval.Decline)

	assert.Equal(t, core.StatusOK, e.ev.finished().Status)
	assert.NoFileExists(t, filepath.Join(e.outside, "x.txt"))
	assert.Contains(t, e.lastOutputs(), "the user declined this command")
}

func TestEmbedded_DontAskAgain(t *testing.T) {
	t.Parallel()
	e := newApprovalEnv(t, approvalOpts{interactive: true}, func(outside string) []fakellm.Reply {
		target := filepath.Join(outside, "x.txt")
		cmd := "touch " + target

		return []fakellm.Reply{{Escalated: []string{cmd}}, {Escalated: []string{cmd + " && rm " + target}}, {Escalated: []string{cmd}}, {Text: "done"}}
	})
	target := filepath.Join(e.outside, "x.txt")
	e.run(t)

	e.approve(t, approval.ApprovePrefix)
	// The new rule does not cover all of the second command, so it asks.
	e.approve(t, approval.Approve)

	assert.Equal(t, core.StatusOK, e.ev.finished().Status)
	assert.Equal(t, 2, e.count(isRequested), "the third command ran without asking")
	assert.FileExists(t, target)
	data, err := os.ReadFile(e.rulesFile)
	require.NoError(t, err)
	assert.Equal(t, `prefix_rule(pattern=["touch", "`+target+`"], decision="allow")`+"\n", string(data))
}

func TestEmbedded_ForbiddenRule(t *testing.T) {
	o := approvalOpts{interactive: true, rules: `prefix_rule(pattern=["touch"], decision="forbidden", justification="use the editor")`}
	e := newApprovalEnv(t, o, func(outside string) []fakellm.Reply {
		return []fakellm.Reply{{Commands: []string{"touch inside.txt"}, Escalated: []string{"touch " + filepath.Join(outside, "x.txt")}}, {Text: "done"}}
	})
	e.run(t)

	assert.Equal(t, core.StatusOK, e.ev.finished().Status)
	assert.Zero(t, e.count(isRequested))
	assert.NoFileExists(t, filepath.Join(e.Workspace, "inside.txt"))
	assert.Equal(t, 2, strings.Count(e.lastOutputs(), "a rule forbids this command: use the editor"), e.lastOutputs())
}

func TestEmbedded_AllowRuleRunsUnsandboxed(t *testing.T) {
	e := newApprovalEnv(t, approvalOpts{rules: `prefix_rule(pattern=["touch"])`}, func(outside string) []fakellm.Reply {
		return []fakellm.Reply{{Commands: []string{"touch " + filepath.Join(outside, "x.txt")}}, {Text: "done"}}
	})
	e.run(t)

	assert.Equal(t, core.StatusOK, e.ev.finished().Status)
	assert.FileExists(t, filepath.Join(e.outside, "x.txt"), "an allow rule runs outside the sandbox without asking, even headless")
}

func TestEmbedded_NoOneToAsk(t *testing.T) {
	for name, o := range map[string]approvalOpts{
		"headless denies":              {},
		"approval policy never denies": {policy: approval.Never, interactive: true},
	} {
		t.Run(name, func(t *testing.T) {
			e := newApprovalEnv(t, o, escalate)
			e.run(t)

			assert.Equal(t, core.StatusOK, e.ev.finished().Status)
			assert.Zero(t, e.count(isRequested))
			assert.NoFileExists(t, filepath.Join(e.outside, "x.txt"))
			assert.Contains(t, e.lastOutputs(), "not run: this command needs the user's approval")
		})
	}
}

func TestEmbedded_InterruptDeclinesApproval(t *testing.T) {
	e := newApprovalEnv(t, approvalOpts{interactive: true}, escalate)
	e.run(t)
	e.ev.until("ApprovalRequested", isA[session.ApprovalRequested])

	require.NoError(t, e.s.Interrupt())

	resolved := e.ev.until("ApprovalResolved", isA[session.ApprovalResolved]).(session.ApprovalResolved)
	assert.Equal(t, approval.Decline, resolved.Decision)
	e.ev.finished()
	assert.NoFileExists(t, filepath.Join(e.outside, "x.txt"))
}

// TestEmbedded_PermissionRequestHook answers for the user, even headless:
// allow runs the escalation, deny refuses it.
func TestEmbedded_PermissionRequestHook(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		decision string
		ran      bool
	}{{"allow", true}, {"deny", false}} {
		t.Run(tc.decision, func(t *testing.T) {
			hook := hooks.Hook{
				Event: hooks.PermissionRequest, Source: hooks.SourceUser,
				Command: `grep -q '"tool_name":"Bash"' && echo '{"hookSpecificOutput":{"permissionDecision":"` + tc.decision + `"}}'`,
			}
			e := newApprovalEnv(t, approvalOpts{hooks: []hooks.Hook{hook}}, escalate)
			e.run(t)
			assert.Equal(t, core.StatusOK, e.ev.finished().Status)
			if tc.ran {
				assert.FileExists(t, filepath.Join(e.outside, "x.txt"))
			} else {
				assert.NoFileExists(t, filepath.Join(e.outside, "x.txt"))
			}
		})
	}
}

// TestEmbedded_PreToolUseDecision: a PreToolUse "allow" approves the
// escalation without asking anyone, also headless and with the policy
// never, as Claude Code's does; "deny" and "ask" refuse the call; a forbid
// rule still refuses an allowed call.
func TestEmbedded_PreToolUseDecision(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		decision string
		policy   approval.Policy
		rules    string
		ran      bool
		output   string
	}{
		{name: "allow", decision: "allow", ran: true},
		{name: "allow with the policy never", decision: "allow", policy: approval.Never, ran: true},
		{name: "deny", decision: "deny", output: "blocked by a PreToolUse hook: no"},
		{name: "ask", decision: "ask", output: "blocked by a PreToolUse hook: no"},
		{name: "allow under a forbid rule", decision: "allow", rules: `prefix_rule(pattern=["touch"], decision="forbidden", justification="no touching")`, output: "no touching"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hook := hooks.Hook{
				Event: hooks.PreToolUse, Matcher: "Bash", Source: hooks.SourceUser,
				Command: `echo '{"hookSpecificOutput":{"permissionDecision":"` + tc.decision + `","permissionDecisionReason":"no"}}'`,
			}
			e := newApprovalEnv(t, approvalOpts{policy: tc.policy, rules: tc.rules, hooks: []hooks.Hook{hook}}, escalate)
			e.run(t)
			assert.Equal(t, core.StatusOK, e.ev.finished().Status)
			assert.Zero(t, e.count(isRequested), "no one is asked")
			if tc.ran {
				assert.FileExists(t, filepath.Join(e.outside, "x.txt"))
			} else {
				assert.NoFileExists(t, filepath.Join(e.outside, "x.txt"))
				assert.Contains(t, e.lastOutputs(), tc.output)
			}
		})
	}
}

// TestEmbedded_AutoReview puts the reviewer before the user: allow runs the
// escalation, deny refuses it with the reviewer's reason; the user is not
// asked either way.
func TestEmbedded_AutoReview(t *testing.T) {
	for _, tc := range []struct {
		verdict string
		ran     bool
		output  string
	}{
		{`{"risk_level":"low","user_authorization":"high","outcome":"allow","rationale":"The user asked for it."}`, true, ""},
		{`{"risk_level":"high","user_authorization":"unknown","outcome":"deny","rationale":"Writes outside the project."}`, false, "the auto-reviewer denied this (high risk): Writes outside the project."},
	} {
		t.Run(tc.verdict[:40], func(t *testing.T) {
			e := newApprovalEnv(t, approvalOpts{interactive: true, autoReview: true}, func(outside string) []fakellm.Reply {
				return []fakellm.Reply{
					{Escalated: []string{"touch " + filepath.Join(outside, "x.txt")}},
					{Text: tc.verdict},
					{Text: "done"},
				}
			})
			e.run(t)
			assert.Equal(t, core.StatusOK, e.ev.finished().Status)
			assert.Zero(t, countKind[session.ApprovalRequested](e.ev.all), "the user was not asked")
			assert.Equal(t, 1, countKind[engine.AutoReviewed](e.ev.all))
			if tc.ran {
				assert.FileExists(t, filepath.Join(e.outside, "x.txt"))
			} else {
				assert.NoFileExists(t, filepath.Join(e.outside, "x.txt"))
				reqs := e.llm.Requests()
				assert.Contains(t, strings.Join(reqs[len(reqs)-1].ToolOutputs, "\n"), tc.output)
			}
			review := e.llm.Requests()[1]
			assert.Equal(t, []string{"exec_command"}, review.ToolNames, "the review call offers only Codex's read-only exec_command")
			assert.Contains(t, strings.Join(review.UserTexts, "\n"), "touch ", "the reviewer sees the action")
		})
	}
}

// TestEmbedded_AutoReviewSeesResourceTools gives the reviewer Codex's MCP
// resource tools as it gives any other call: the call with its arguments,
// then its short result.
func TestEmbedded_AutoReviewSeesResourceTools(t *testing.T) {
	e := newApprovalEnv(t, approvalOpts{interactive: true, autoReview: true, mcp: true}, func(outside string) []fakellm.Reply {
		return []fakellm.Reply{
			{Calls: []fakellm.Call{call(mcp.ReadResourceTool, `{"server":"test","uri":"test://greeting"}`)}},
			{Escalated: []string{"touch " + filepath.Join(outside, "x.txt")}},
			{Text: `{"risk_level":"low","user_authorization":"high","outcome":"allow","rationale":"The user asked for it."}`},
			{Text: "done"},
		}
	})
	e.run(t)
	assert.Equal(t, core.StatusOK, e.ev.finished().Status)
	assert.FileExists(t, filepath.Join(e.outside, "x.txt"))
	reqs := e.llm.Requests()
	require.Len(t, reqs, 4)
	sent := strings.Join(reqs[2].UserTexts, "\n")
	assert.Contains(t, sent, "tool read_mcp_resource call: {\"server\":\"test\",\"uri\":\"test://greeting\"}")
	assert.Contains(t, sent, "tool read_mcp_resource result: ")
	assert.NotContains(t, sent, "hello from the resource", "the reviewer gets no output")
}

// TestEmbedded_AutoReviewCommandsAreReadOnly runs the reviewer's own
// commands in the read-only sandbox with a temporary directory of its own,
// and a session's second review continues the first's conversation with
// only what happened since.
func TestEmbedded_AutoReviewCommandsAreReadOnly(t *testing.T) {
	allow := fakellm.Reply{Text: `{"outcome":"allow"}`}
	e := newApprovalEnv(t, approvalOpts{interactive: true, autoReview: true}, func(outside string) []fakellm.Reply {
		probe := `touch probe.txt; echo tmp > "$TMPDIR/scratch" && cat "$TMPDIR/scratch"; pwd`
		return []fakellm.Reply{
			{Escalated: []string{"touch " + filepath.Join(outside, "x.txt")}},
			{Calls: []fakellm.Call{{Name: "exec_command", Args: fmt.Sprintf(`{"cmd":%q}`, probe)}}},
			allow,
			{Escalated: []string{"touch " + filepath.Join(outside, "y.txt")}},
			allow,
			{Text: "done"},
		}
	})
	ws := e.Workspace
	e.run(t)

	assert.Equal(t, core.StatusOK, e.ev.finished().Status)
	assert.FileExists(t, filepath.Join(e.outside, "y.txt"))
	assert.NoFileExists(t, filepath.Join(ws, "probe.txt"), "the reviewer cannot write the workspace")
	reqs := e.llm.Requests()
	require.Len(t, reqs, 6)
	out := strings.Join(reqs[2].ToolOutputs, "\n")
	assert.Contains(t, out, "Process exited with code 0")
	assert.Contains(t, out, "\ntmp\n", "the reviewer writes its own temporary directory")
	assert.Contains(t, out, ws)
	second := reqs[4]
	assert.Equal(t, reqs[2].Input, second.Input[:len(reqs[2].Input)], "the second review continues the first")
	last := second.UserTexts[len(second.UserTexts)-1]
	assert.Contains(t, last, ">>> TRANSCRIPT DELTA START")
	assert.Contains(t, last, "y.txt")
	delta, _, _ := strings.Cut(last, ">>> APPROVAL REQUEST START")
	assert.NotContains(t, delta, "x.txt", "the first escalation is not sent again")
}

// TestEmbedded_AutoReviewStartsAndEnds: a review reports its start, and
// its end even when the reviewer's model call fails (it then denies).
func TestEmbedded_AutoReviewStartsAndEnds(t *testing.T) {
	e := newApprovalEnv(t, approvalOpts{interactive: true, autoReview: true}, func(outside string) []fakellm.Reply {
		return []fakellm.Reply{
			{Escalated: []string{"touch " + filepath.Join(outside, "x.txt")}},
			{Fail: http.StatusBadRequest, FailCode: "invalid_prompt"},
			{Text: "done"},
		}
	})
	e.run(t)
	e.ev.finished()
	var started []engine.AutoReviewing
	var ended []engine.AutoReviewed
	for _, ev := range e.ev.all {
		switch v := ev.(type) {
		case engine.AutoReviewing:
			started = append(started, v)
		case engine.AutoReviewed:
			ended = append(ended, v)
		}
	}
	require.Len(t, started, 1)
	require.Len(t, ended, 1)
	assert.Equal(t, "deny", ended[0].Outcome)
	assert.Equal(t, started[0].Command, ended[0].Command)
	assert.NoFileExists(t, filepath.Join(e.outside, "x.txt"))
}
