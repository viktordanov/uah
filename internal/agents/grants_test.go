package agents_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/agents"
	"github.com/viktordanov/uah/internal/approval"
	"github.com/viktordanov/uah/internal/engine/embedded"
	"github.com/viktordanov/uah/testing/fakellm"
	"github.com/viktordanov/uah/testing/harnesstest"
)

// TestAgents_ChildSharesTheParentsGrants pins that a child shares its
// parent's grants: the parent's patch makes a linked worktree of the
// workspace's repository writable, and the child's sandboxed command then
// writes there.
func TestAgents_ChildSharesTheParentsGrants(t *testing.T) {
	outside := harnesstest.OutsideDir(t, "uah-agents-grant-")
	bar := filepath.Join(outside, "bar")
	e := newEnv(t, agents.Config{},
		fakellm.Reply{Calls: []fakellm.Call{{Name: "apply_patch", Custom: true, Args: "*** Begin Patch\n*** Add File: " + filepath.Join(bar, "parent.txt") + "\n+parent\n*** End Patch\n"}}},
		fakellm.Reply{Calls: []fakellm.Call{call("spawn_agent", `{"message":"CHILD-WT write there"}`)}},
		fakellm.Reply{From: func(req fakellm.Request) fakellm.Reply {
			return fakellm.Reply{Calls: []fakellm.Call{call("wait_agent", `{"targets":["`+strings.Join(ids(req), `","`)+`"],"timeout_ms":60000}`)}}
		}},
		fakellm.Reply{Text: "done"},
	)
	e.llm.Route("CHILD-WT", fakellm.Reply{Commands: []string{"echo child > " + filepath.Join(bar, "child.txt")}}, fakellm.Reply{Text: "wrote"})
	require.NoError(t, os.MkdirAll(e.Workspace, 0o755))
	harnesstest.Git(t, e.Workspace, "init", "-q")
	harnesstest.Git(t, e.Workspace, "commit", "-q", "--allow-empty", "-m", "first")
	harnesstest.Git(t, e.Workspace, "worktree", "add", "-q", "-b", "bar", bar)
	sandboxed := e.sandboxed(t)
	s, ev := e.open(t, false, sandboxed, func(c *embedded.Config) { c.Approver = approval.New(approval.Config{}) })

	_, err := s.Submit("delegate")
	require.NoError(t, err)
	ev.finished()

	assert.FileExists(t, filepath.Join(bar, "parent.txt"))
	assert.FileExists(t, filepath.Join(bar, "child.txt"), "the child writes the parent's grant in the sandbox")
}
