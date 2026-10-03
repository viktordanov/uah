package contextprep_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/viktordanov/uah/internal/contextprep"
)

func TestAgentFiles(t *testing.T) {
	t.Parallel()
	got := contextprep.AgentFiles{}.Prepare(t.Context(), contextprep.Facts{InstructionFiles: []string{"/home/u/.codex/AGENTS.md", "/repo/AGENTS.md"}})
	assert.Equal(t, "Instruction files in the system prompt, in order (their @ lines are expanded in place):\n"+
		"- /home/u/.codex/AGENTS.md\n- /repo/AGENTS.md\n"+
		"These are all of the session's instruction files: there is no need to search for more AGENTS.md or CLAUDE.md files.", got)

	none := contextprep.AgentFiles{}.Prepare(t.Context(), contextprep.Facts{})
	assert.Contains(t, none, "No instruction files (AGENTS.md) were loaded")

	omitted := contextprep.AgentFiles{}.Prepare(t.Context(), contextprep.Facts{OmittedInstructionFiles: []string{"/repo/AGENTS.md"}})
	assert.Equal(t, "This session's system prompt replaces uah's and leaves out the workspace's instruction files on purpose, so none of them is in it:\n"+
		"- /repo/AGENTS.md\nRead the ones the task needs.", omitted, "not said to be in the system prompt, nor to be none")

	off := contextprep.AgentFiles{}.Prepare(t.Context(), contextprep.Facts{InstructionsOff: true})
	assert.Equal(t, "Loading instruction files (AGENTS.md) is turned off for this session, so none of them is in the system prompt.", off,
		"--no-instructions is not taken for a workspace without instruction files")
	assert.NotContains(t, off, "none to search for")
}
