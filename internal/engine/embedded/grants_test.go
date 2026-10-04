package embedded_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/approval"
	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/rules"
	"github.com/viktordanov/uah/internal/sandbox"
	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/testing/fakellm"
	"github.com/viktordanov/uah/testing/harnesstest"
)

// sibling makes the workspace a repository and adds a linked worktree of
// it outside the sandbox, at <outside>/bar, and returns the worktree's
// real path.
func sibling(t *testing.T, ws, outside string) string {
	t.Helper()
	gitRepo(t, ws)
	bar := filepath.Join(outside, "bar")
	harnesstest.Git(t, ws, "worktree", "add", "-q", "-b", "bar", bar)
	real, err := filepath.EvalSymlinks(bar)
	require.NoError(t, err)

	return real
}

// gitRepo makes dir a repository with a.txt committed.
func gitRepo(t *testing.T, dir string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	harnesstest.Git(t, dir, "init", "-q")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\ntwo\n"), 0o644))
	harnesstest.Git(t, dir, "add", "a.txt")
	harnesstest.Git(t, dir, "commit", "-q", "-m", "first")
}

func noticeWith(text string) func(core.Event) bool {
	return func(e core.Event) bool {
		n, ok := e.(session.Notice)

		return ok && strings.Contains(n.Message, text)
	}
}

// TestGrant_SiblingWorktreeInAutoMode is the case the grants exist for: in
// Auto mode, the first patch into a linked worktree of the workspace's
// repository applies without a review, the worktree becomes writable for
// the session with one notice, and a later command writes there in the
// sandbox, with the sandbox note naming it.
func TestGrant_SiblingWorktreeInAutoMode(t *testing.T) {
	skipWithoutSandbox(t)
	var bar string
	e := newPatchEnv(t, patchOpts{mode: sandbox.WorkspaceWrite, permission: approval.ModeAuto, autoReview: true}, func(ws, outside string) []fakellm.Reply {
		bar = sibling(t, ws, outside)

		return []fakellm.Reply{
			{Calls: []fakellm.Call{{Name: "apply_patch", Custom: true, Args: "*** Begin Patch\n*** Update File: " + filepath.Join(bar, "a.txt") + "\n@@\n one\n-two\n+TWO\n*** End Patch\n"}}},
			{Commands: []string{"echo cmd > " + filepath.Join(bar, "cmd.txt")}},
			{Text: "done"},
		}
	})
	assert.Equal(t, core.StatusOK, e.ev.finished().Status)

	assert.Equal(t, "one\nTWO\n", readFile(t, filepath.Join(bar, "a.txt")))
	assert.Equal(t, "cmd\n", readFile(t, filepath.Join(bar, "cmd.txt")), "a later command writes the worktree in the sandbox")
	assert.Zero(t, countKind[engine.AutoReviewing](e.ev.all), "no review")
	assert.Zero(t, countKind[session.ApprovalRequested](e.ev.all))
	assert.Equal(t, 1, count(e.ev.all, noticeWith("writable for this session: "+bar+" (a git worktree of this repository)")))
	reqs := e.llm.Requests()
	require.Len(t, reqs, 3)
	assert.Contains(t, reqs[1].ToolOutputs[0], "uah: "+bar+" is now writable for the rest of this session")
	assert.NotContains(t, toolDefs(reqs[0]), bar)
	assert.Contains(t, toolDefs(reqs[1]), bar, "the sandbox note names the grant")
}

// TestGrant_EscalatedCommandIntoSiblingWorktree: an escalated command that
// names a path in a linked worktree grants the worktree and runs in the
// sandbox, without a review.
func TestGrant_EscalatedCommandIntoSiblingWorktree(t *testing.T) {
	skipWithoutSandbox(t)
	var bar string
	e := newPatchEnv(t, patchOpts{mode: sandbox.WorkspaceWrite, permission: approval.ModeAuto, autoReview: true}, func(ws, outside string) []fakellm.Reply {
		bar = sibling(t, ws, outside)

		return []fakellm.Reply{
			{Escalated: []string{"cd " + bar + " && echo fmt > fmt.txt"}},
			{Text: "done"},
		}
	})
	assert.Equal(t, core.StatusOK, e.ev.finished().Status)

	assert.Equal(t, "fmt\n", readFile(t, filepath.Join(bar, "fmt.txt")))
	assert.Zero(t, countKind[engine.AutoReviewing](e.ev.all), "no review")
	assert.Equal(t, 1, count(e.ev.all, noticeWith("writable for this session: "+bar)))
}

// TestGrant_NotForOtherDirectories pins the negative side in a headless
// workspace session, where a patch outside the writable roots is refused:
// an unrelated repository, a plain directory, and a worktree of the same
// repository that is the home directory are not granted, and the patch
// is refused as before.
func TestGrant_NotForOtherDirectories(t *testing.T) {
	skipWithoutSandbox(t)
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, ws, outside string) string
	}{
		{"unrelated repository", func(t *testing.T, _, outside string) string {
			other := filepath.Join(outside, "other")
			gitRepo(t, other)

			return other
		}},
		{"plain directory", func(t *testing.T, _, outside string) string {
			plain := filepath.Join(outside, "plain")
			require.NoError(t, os.MkdirAll(plain, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(plain, "a.txt"), []byte("one\ntwo\n"), 0o644))

			return plain
		}},
		{"the home directory", func(t *testing.T, ws, outside string) string {
			bar := sibling(t, ws, outside)
			t.Setenv("HOME", bar)

			return bar
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var dir string
			e := newPatchEnv(t, patchOpts{mode: sandbox.WorkspaceWrite}, func(ws, outside string) []fakellm.Reply {
				dir = tc.setup(t, ws, outside)

				return applyPatch("*** Update File: " + filepath.Join(dir, "a.txt") + "\n@@\n one\n-two\n+TWO")
			})
			assert.Equal(t, core.StatusOK, e.ev.finished().Status)

			assert.Equal(t, "one\ntwo\n", readFile(t, filepath.Join(dir, "a.txt")))
			assert.Contains(t, e.lastOutput(), "no user can approve it")
			assert.Zero(t, count(e.ev.all, noticeWith("writable for this session")))
		})
	}
}

// TestGrant_WorkspaceModeOffersTheDirectory: in workspace mode the user is
// asked about a patch outside the writable roots, with the choice to allow
// writes to its directory for the session; chosen, the patch applies, a
// later command writes there in the sandbox, and a later patch there asks
// no one.
func TestGrant_WorkspaceModeOffersTheDirectory(t *testing.T) {
	skipWithoutSandbox(t)
	var plain string
	e := newPatchEnv(t, patchOpts{mode: sandbox.WorkspaceWrite, interactive: true}, func(_, outside string) []fakellm.Reply {
		plain = filepath.Join(outside, "plain")
		require.NoError(t, os.MkdirAll(plain, 0o755))
		real, err := filepath.EvalSymlinks(plain)
		require.NoError(t, err)
		plain = real

		return []fakellm.Reply{
			{Calls: []fakellm.Call{{Name: "apply_patch", Custom: true, Args: "*** Begin Patch\n*** Add File: " + filepath.Join(plain, "one.txt") + "\n+one\n*** End Patch\n"}}},
			{Commands: []string{"echo two > " + filepath.Join(plain, "two.txt")}},
			{Calls: []fakellm.Call{{Name: "apply_patch", Custom: true, Args: "*** Begin Patch\n*** Add File: " + filepath.Join(plain, "three.txt") + "\n+three\n*** End Patch\n"}}},
			{Text: "done"},
		}
	})
	req := e.answer(t, approval.ApproveGrant)
	assert.Equal(t, plain, req.GrantRoot)
	assert.Equal(t, core.StatusOK, e.ev.finished().Status)

	assert.Equal(t, "one\n", readFile(t, filepath.Join(plain, "one.txt")))
	assert.Equal(t, "two\n", readFile(t, filepath.Join(plain, "two.txt")))
	assert.Equal(t, "three\n", readFile(t, filepath.Join(plain, "three.txt")))
	assert.Equal(t, 1, countKind[session.ApprovalRequested](e.ev.all), "asked once")
	assert.Equal(t, 1, count(e.ev.all, noticeWith("writable for this session: "+plain+" (your approval)")))
}

// TestGrant_AutoReviewerNeverGrants: the auto-reviewer's approval applies
// the one patch and grants nothing, so a later command still cannot write
// there.
func TestGrant_AutoReviewerNeverGrants(t *testing.T) {
	skipWithoutSandbox(t)
	var plain string
	e := newPatchEnv(t, patchOpts{mode: sandbox.WorkspaceWrite, permission: approval.ModeAuto, autoReview: true}, func(_, outside string) []fakellm.Reply {
		plain = filepath.Join(outside, "plain")
		require.NoError(t, os.MkdirAll(plain, 0o755))

		return []fakellm.Reply{
			{Calls: []fakellm.Call{{Name: "apply_patch", Custom: true, Args: "*** Begin Patch\n*** Add File: " + filepath.Join(plain, "one.txt") + "\n+one\n*** End Patch\n"}}},
			{Text: `{"risk_level":"low","user_authorization":"high","outcome":"allow","rationale":"Asked for."}`},
			{Commands: []string{"echo two > " + filepath.Join(plain, "two.txt")}},
			{Text: "done"},
		}
	})
	assert.Equal(t, core.StatusOK, e.ev.finished().Status)

	assert.Equal(t, "one\n", readFile(t, filepath.Join(plain, "one.txt")))
	assert.NoFileExists(t, filepath.Join(plain, "two.txt"))
	assert.Equal(t, 1, countKind[engine.AutoReviewed](e.ev.all))
	assert.Zero(t, count(e.ev.all, noticeWith("writable for this session")))
}

// TestGrant_ForbidRuleWins: a forbid rule on a path refuses a patch there
// before any grant, also in a worktree of the same repository, and that
// patch grants nothing; a later patch elsewhere in the worktree grants it,
// and the rule still refuses its path.
func TestGrant_ForbidRuleWins(t *testing.T) {
	skipWithoutSandbox(t)
	var bar string
	e := newPatchEnv(t, patchOpts{mode: sandbox.WorkspaceWrite, permission: approval.ModeAuto, rules: func(string) []rules.Rule {
		return []rules.Rule{{Pattern: [][]string{{"apply_patch"}, {filepath.Join(bar, "secret.txt")}}, Decision: rules.Forbidden}}
	}}, func(ws, outside string) []fakellm.Reply {
		bar = sibling(t, ws, outside)
		add := func(name string) fakellm.Reply {
			return fakellm.Reply{Calls: []fakellm.Call{{Name: "apply_patch", Custom: true, Args: "*** Begin Patch\n*** Add File: " + filepath.Join(bar, name) + "\n+x\n*** End Patch\n"}}}
		}

		return []fakellm.Reply{add("secret.txt"), add("open.txt"), add("secret.txt"), {Text: "done"}}
	})
	assert.Equal(t, core.StatusOK, e.ev.finished().Status)

	assert.NoFileExists(t, filepath.Join(bar, "secret.txt"))
	assert.FileExists(t, filepath.Join(bar, "open.txt"))
	reqs := e.llm.Requests()
	assert.Contains(t, reqs[1].ToolOutputs[0], "not run: a rule forbids this command.")
	assert.Contains(t, reqs[3].ToolOutputs[2], "not run: a rule forbids this command.", "also once granted")
	assert.Equal(t, 1, count(e.ev.all, noticeWith("writable for this session: "+bar)), "granted by the second patch")
}

// TestGrant_NotForForbiddenOrProtected, in a headless workspace session:
// an escalated command a forbid rule refuses grants nothing, and a
// worktree inside a protected path of the workspace (.agents) is never
// granted, so its escalation stays one and a later command cannot write
// either.
func TestGrant_NotForForbiddenOrProtected(t *testing.T) {
	skipWithoutSandbox(t)
	var bar, nested string
	e := newPatchEnv(t, patchOpts{mode: sandbox.WorkspaceWrite, rules: func(string) []rules.Rule {
		return []rules.Rule{{Pattern: [][]string{{"touch"}}, Decision: rules.Forbidden}}
	}}, func(ws, outside string) []fakellm.Reply {
		bar = sibling(t, ws, outside)
		nested = filepath.Join(ws, ".agents", "wt")
		harnesstest.Git(t, ws, "worktree", "add", "-q", "-b", "nested", nested)

		return []fakellm.Reply{
			{Escalated: []string{"touch " + filepath.Join(bar, "x.txt")}},
			{Escalated: []string{"echo n > " + filepath.Join(nested, "n.txt")}},
			{Commands: []string{"echo y > " + filepath.Join(bar, "y.txt")}},
			{Text: "done"},
		}
	})
	assert.Equal(t, core.StatusOK, e.ev.finished().Status)

	assert.NoFileExists(t, filepath.Join(bar, "x.txt"))
	assert.NoFileExists(t, filepath.Join(bar, "y.txt"), "the forbidden command granted nothing")
	assert.NoFileExists(t, filepath.Join(nested, "n.txt"))
	assert.Zero(t, count(e.ev.all, noticeWith("writable for this session")))
	outputs := e.llm.Requests()[3].ToolOutputs
	require.Len(t, outputs, 3)
	assert.Contains(t, outputs[0], "a rule forbids this command")
	assert.Contains(t, outputs[1], "no user can approve it", "still an escalation")
	assert.Contains(t, outputs[2], "sandbox likely blocked this")
}

func skipWithoutSandbox(t *testing.T) {
	t.Helper()
	if _, err := (sandbox.Policy{Mode: sandbox.WorkspaceWrite, Workspace: t.TempDir()}).Wrap([]string{"/bin/sh"}); err != nil {
		t.Skipf("no sandbox here: %v", err)
	}
}

// TestPatch_ForbidRuleInAnotherSpelling: a forbid rule that names a path
// through a symlink, or in another case, refuses a patch that names the
// same file by its own path.
func TestPatch_ForbidRuleInAnotherSpelling(t *testing.T) {
	skipWithoutSandbox(t)
	e := newPatchEnv(t, patchOpts{mode: sandbox.WorkspaceWrite, rules: func(ws string) []rules.Rule {
		return []rules.Rule{{Pattern: [][]string{{"apply_patch"}, {filepath.Join(ws, "link", "secret.txt")}}, Decision: rules.Forbidden}}
	}}, func(ws, _ string) []fakellm.Reply {
		require.NoError(t, os.MkdirAll(filepath.Join(ws, "Real"), 0o755))
		require.NoError(t, os.Symlink(filepath.Join(ws, "Real"), filepath.Join(ws, "link")))
		require.NoError(t, os.WriteFile(filepath.Join(ws, "Real", "secret.txt"), []byte("s\n"), 0o644))

		return applyPatch("*** Update File: Real/secret.txt\n@@\n-s\n+leak")
	})
	e.ev.finished()

	assert.Contains(t, e.lastOutput(), "not run: a rule forbids this command.")
	assert.Equal(t, "s\n", readFile(t, filepath.Join(e.Workspace, "Real", "secret.txt")))
}
