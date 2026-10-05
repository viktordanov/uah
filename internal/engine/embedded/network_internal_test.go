package embedded

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/viktordanov/uagent/core"
	"github.com/viktordanov/uagent/harness"

	"github.com/viktordanov/uah/internal/sandbox"
)

// TestPolicy_ReadOnlyHasNoNetwork: network_access is the workspace-write
// sandbox's, as in Codex, so a /review's read-only reviewer never has it.
func TestPolicy_ReadOnlyHasNoNetwork(t *testing.T) {
	policy := sandbox.Policy{Network: true}
	ws := t.TempDir()
	w := &wiring{e: &Engine{cfg: Config{Sandbox: &policy}}, l: harness.Launch{SessionsDir: t.TempDir()}, grants: sandbox.NewGrants(ws, nil)}
	req := core.Request{Workspace: ws, SessionID: "s"}

	assert.True(t, w.policy(req, sandbox.WorkspaceWrite).Network)
	assert.False(t, w.policy(req, sandbox.ReadOnly).Network)
}
