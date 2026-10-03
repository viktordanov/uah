package approval_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/approval"
	"github.com/viktordanov/uah/internal/rules"
)

const ruleSrc = `
prefix_rule(pattern=["git", "push"], decision="prompt", justification="pushing changes the remote")
prefix_rule(pattern=["rm", "-rf"], decision="forbidden", justification="use trash instead")
prefix_rule(pattern=["go", "test"])
`

// answer is a user who always answers the same, recording the prompts.
type answer struct {
	with    approval.Answer
	prompts []approval.Prompt
}

func (a *answer) ask(_ context.Context, p approval.Prompt) approval.Answer {
	a.prompts = append(a.prompts, p)

	return a.with
}

func TestDecide(t *testing.T) {
	parsed, err := rules.Parse("test.rules", []byte(ruleSrc))
	require.NoError(t, err)
	for _, tc := range []struct {
		name      string
		policy    approval.Policy
		req       approval.Request
		user      approval.Answer // "" is a headless run
		want      approval.Run
		reason    string
		asked     bool
		proposed  []string
		escalated bool
	}{
		{name: "a plain command runs sandboxed", req: approval.Request{Command: "ls"}, user: approval.Approve, want: approval.Sandboxed},
		{name: "an allow rule runs unsandboxed", req: approval.Request{Command: "go test ./..."}, want: approval.Unsandboxed},
		{name: "a forbidden rule denies", req: approval.Request{Command: "ls && rm -rf /", Escalated: true}, user: approval.Approve, want: approval.Deny, reason: "use trash instead"},
		{
			name: "an escalation asks and runs unsandboxed", req: approval.Request{Command: "curl example.com", Escalated: true, Justification: "fetch"},
			user: approval.Approve, want: approval.Unsandboxed, asked: true, proposed: []string{"curl", "example.com"}, escalated: true,
		},
		{
			name: "the model's prefix is proposed when it covers the command", req: approval.Request{Command: "npm install x", Escalated: true, PrefixRule: []string{"npm", "install"}},
			user: approval.Approve, want: approval.Unsandboxed, asked: true, proposed: []string{"npm", "install"}, escalated: true,
		},
		{
			name: "a banned prefix is not proposed", req: approval.Request{Command: "git", Escalated: true, PrefixRule: []string{"git"}},
			user: approval.Approve, want: approval.Unsandboxed, asked: true, escalated: true,
		},
		{name: "a declined escalation denies", req: approval.Request{Command: "curl x", Escalated: true}, user: approval.Decline, want: approval.Deny, reason: "the user declined", asked: true, proposed: []string{"curl", "x"}, escalated: true},
		{name: "a prompt rule asks and runs sandboxed", req: approval.Request{Command: "git push"}, user: approval.Approve, want: approval.Sandboxed, asked: true},
		{name: "headless denies an escalation", req: approval.Request{Command: "curl x", Escalated: true}, want: approval.Deny, reason: "headless"},
		{name: "never denies an escalation", policy: approval.Never, req: approval.Request{Command: "curl x", Escalated: true}, user: approval.Approve, want: approval.Deny, reason: "approval policy is never"},
		{name: "never denies a prompt rule", policy: approval.Never, req: approval.Request{Command: "git push"}, user: approval.Approve, want: approval.Deny, reason: "never"},
		{name: "yolo runs a prompt rule unasked", req: approval.Request{Command: "git push", Bypass: true}, user: approval.Decline, want: approval.Unsandboxed},
		{name: "yolo runs an escalation unasked, also headless", req: approval.Request{Command: "curl x", Escalated: true, Bypass: true}, want: approval.Unsandboxed},
		{name: "yolo runs past the never policy", policy: approval.Never, req: approval.Request{Command: "curl x", Escalated: true, Bypass: true}, want: approval.Unsandboxed},
		{name: "a forbid rule refuses in yolo too", req: approval.Request{Command: "rm -rf /", Bypass: true}, user: approval.Approve, want: approval.Deny, reason: "use trash instead"},
		{name: "a forbid rule refuses a command with a redirect in yolo", req: approval.Request{Command: "rm -rf x 2>/dev/null", Bypass: true}, want: approval.Deny, reason: "use trash instead"},
		{name: "a forbid rule refuses inside a subshell", req: approval.Request{Command: "(cd x && rm -rf y)", Bypass: true}, want: approval.Deny, reason: "use trash instead"},
		{name: "a hook's approval runs an escalation unasked, also headless", req: approval.Request{Command: "curl x", Escalated: true, Approved: true}, want: approval.Unsandboxed},
		{name: "a hook's approval runs a prompt rule sandboxed", policy: approval.Never, req: approval.Request{Command: "git push", Approved: true}, want: approval.Sandboxed},
		{name: "a forbid rule refuses past a hook's approval", req: approval.Request{Command: "rm -rf /", Approved: true}, want: approval.Deny, reason: "use trash instead"},
		{name: "a forbid rule refuses with an assignment prefix", req: approval.Request{Command: "X=1 rm -rf y"}, user: approval.Approve, want: approval.Deny, reason: "use trash instead"},
		{name: "a forbid rule refuses inside a substitution", req: approval.Request{Command: "echo $(rm -rf y)", Bypass: true}, want: approval.Deny, reason: "use trash instead"},
		{name: "a forbid rule refuses a command that may match it", req: approval.Request{Command: "rm $FLAGS y", Bypass: true}, want: approval.Deny, reason: "may run a command a rule forbids"},
		{name: "a forbid rule refuses an unknown command name", req: approval.Request{Command: "$CMD -rf y", Bypass: true}, want: approval.Deny, reason: "may run a command a rule forbids"},
		{name: "yolo runs an unsplittable command no forbid rule can match", req: approval.Request{Command: "echo $HOME > out.txt", Escalated: true, Bypass: true}, want: approval.Unsandboxed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := approval.New(approval.Config{Policy: tc.policy, Rules: parsed})
			user := &answer{with: tc.user}
			var ask approval.Ask
			if tc.user != "" {
				ask = user.ask
			}

			got := a.Decide(context.Background(), tc.req, ask)

			assert.Equal(t, tc.want, got.Run)
			if tc.reason != "" {
				assert.Contains(t, got.Reason, tc.reason)
			}
			require.Equal(t, tc.asked, len(user.prompts) == 1)
			if tc.asked {
				assert.Equal(t, tc.proposed, user.prompts[0].ProposedPrefix)
				assert.Equal(t, tc.escalated, user.prompts[0].Escalation)
			}
		})
	}
}

func TestDecide_DontAskAgain(t *testing.T) {
	file := filepath.Join(t.TempDir(), "rules", rules.DefaultFile)
	a := approval.New(approval.Config{RulesFile: file})
	user := &answer{with: approval.ApprovePrefix}
	req := approval.Request{Command: "cargo build --release", Escalated: true, PrefixRule: []string{"cargo", "build"}}

	assert.Equal(t, approval.Unsandboxed, a.Decide(context.Background(), req, user.ask).Run)
	req.Command = "cargo build"
	assert.Equal(t, approval.Unsandboxed, a.Decide(context.Background(), req, user.ask).Run)

	assert.Len(t, user.prompts, 1, "the second command matched the new rule")
	data, err := os.ReadFile(file)
	require.NoError(t, err)
	assert.Equal(t, `prefix_rule(pattern=["cargo", "build"], decision="allow")`+"\n", string(data))
}

func TestDecide_NoSandbox(t *testing.T) {
	a := approval.New(approval.Config{})
	user := &answer{with: approval.Approve}

	got := a.Decide(context.Background(), approval.Request{Command: "ls", NoSandbox: true}, user.ask)

	assert.Equal(t, approval.Unsandboxed, got.Run)
	require.Len(t, user.prompts, 1, "without a sandbox every command asks")
	assert.True(t, user.prompts[0].Escalation)
}

func TestParsePolicy(t *testing.T) {
	for in, want := range map[string]approval.Policy{"": approval.OnRequest, "on-request": approval.OnRequest, "on-failure": approval.OnRequest, "never": approval.Never} {
		got, err := approval.ParsePolicy(in)
		require.NoError(t, err)
		assert.Equal(t, want, got)
	}
	_, err := approval.ParsePolicy("untrusted")
	require.Error(t, err)
}

// TestForbidden checks that Forbidden applies only the forbidden rules,
// also to a command that does not split, and leaves allow and prompt
// rules alone.
func TestForbidden(t *testing.T) {
	parsed, err := rules.Parse("test.rules", []byte(ruleSrc))
	require.NoError(t, err)
	a := approval.New(approval.Config{Rules: parsed})
	for command, want := range map[string]bool{
		"rm -rf build":            true,
		"ls && rm -rf build":      true,
		"rm -rf $DIR > /dev/null": true,
		"git push":                false,
		"go test ./...":           false,
		"ls":                      false,
	} {
		reason, forbidden := a.Forbidden(command)
		assert.Equal(t, want, forbidden, command)
		if want {
			assert.Contains(t, reason, "not run:", command)
		} else {
			assert.Empty(t, reason, command)
		}
	}
}
