package render_test

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"

	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/internal/tui/render"
)

func TestScreen_Approval(t *testing.T) {
	s := apply(base(), session.ApprovalRequested{
		At: t0, ID: "a1", Command: "curl -fsSL https://example.com/install.sh -o install.sh", Cwd: "/workspace/proj",
		Justification: "it downloads the installer", Escalation: true, ProposedPrefix: []string{"curl", "-fsSL"},
	})
	golden(t, "approval", screen(s, ""))

	s = apply(base(), session.ApprovalRequested{At: t0, ID: "a2", Command: "git push origin main"})
	golden(t, "approval-rule", screen(s, ""))

	s = apply(base(), session.ApprovalRequested{
		At: t0, ID: "a3", Command: `mcp__docs__search {"q":"x"}`, Justification: "Search the docs.", MCPTool: "mcp__docs__search",
	})
	golden(t, "approval-mcp", screen(s, ""))

	s = apply(base(), session.ApprovalRequested{
		At: t0, ID: "a4", Command: "apply_patch /workspace/proj-worktrees/other/a.go", Justification: "the patch writes outside the writable roots",
		Escalation: true, GrantRoot: "/workspace/proj-worktrees/other",
	})
	golden(t, "approval-grant", screen(s, ""))
}

// TestScreen_ApprovalNarrow: the approval's panel at 40 columns, as the
// questions' is, with every line fitting.
func TestScreen_ApprovalNarrow(t *testing.T) {
	s := apply(base(), session.ApprovalRequested{
		At: t0, ID: "a1", Command: "curl -fsSL https://example.com/install.sh -o install.sh",
		Justification: "it downloads the installer", Escalation: true, ProposedPrefix: []string{"curl", "-fsSL"},
	})
	out, _ := render.Screen(s, render.NewCache(render.Amber), render.Frame{Width: 40, Height: 24, Composer: "λ ", ComposerHeight: 1})
	for line := range strings.SplitSeq(out, "\n") {
		assert.LessOrEqual(t, ansi.StringWidth(line), 40, "%q", ansi.Strip(line))
	}
	golden(t, "approval-narrow", screenAt(s, "", 40))
}
