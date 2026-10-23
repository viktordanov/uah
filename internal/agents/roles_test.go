package agents_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/agents"
)

func writeRole(t *testing.T, dir, name, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600))
}

func TestLoadRoles(t *testing.T) {
	user, project := t.TempDir(), t.TempDir()
	writeRole(t, user, "reviewer.toml", `
name = "reviewer"
description = "Reviews diffs."
nickname_candidates = ["Rex", "Rita"]
model = "gpt-small"
model_reasoning_effort = "low"
developer_instructions = "Review only."
sandbox_mode = "read-only"
`)
	writeRole(t, user, "worker.toml", `
name = "worker"
description = "User worker."
developer_instructions = "Work."
`)
	writeRole(t, project, "worker.toml", `
name = "worker"
description = "Project worker."
developer_instructions = "Work here."
`)
	writeRole(t, project, "fast-reviewer.toml", `
name = "fast-reviewer"
description = "Reviews fast."
service_tier = "fast"
developer_instructions = "Review quickly."
`)
	writeRole(t, project, "flex.toml", `
name = "flex"
description = "Flex."
service_tier = "flex"
developer_instructions = "Wait."
`)
	writeRole(t, project, "bad.toml", `name = "bad"`)
	writeRole(t, project, "notes.txt", `ignored`)

	roles, warnings := agents.LoadRoles(user, project, filepath.Join(user, "missing"))

	require.Len(t, roles, 4)
	assert.Equal(t, "fast-reviewer", roles[0].Name)
	assert.Equal(t, agents.TierPriority, roles[0].ServiceTier, "fast is Codex's legacy name for priority")
	assert.Equal(t, "flex", roles[1].Name)
	assert.Empty(t, roles[1].ServiceTier, "no provider of uah's serves flex")
	assert.Equal(t, "reviewer", roles[2].Name)
	assert.Equal(t, []string{"Rex", "Rita"}, roles[2].NicknameCandidates)
	assert.Equal(t, "gpt-small", roles[2].Model)
	assert.Equal(t, "low", roles[2].Effort)
	assert.Equal(t, "Project worker.", roles[3].Description, "the project role replaces the user role")
	require.Len(t, warnings, 3)
	assert.Contains(t, warnings[0], "ignoring keys uah does not support: sandbox_mode")
	assert.Contains(t, warnings[1], "must define a non-empty description")
	assert.Contains(t, warnings[2], `service_tier = "flex" (want priority, fast, or default)`)
}

// TestLoadRoles_TOMLValues reads tools and service_tier in a TOML role
// file in the forms a Markdown file takes: tools as a comma-separated
// string or a list, service_tier as a string or a bool.
func TestLoadRoles_TOMLValues(t *testing.T) {
	dir := t.TempDir()
	writeRole(t, dir, "a.toml", "name = \"a\"\ndescription = \"A.\"\ndeveloper_instructions = \"A.\"\ntools = \"Bash, apply_patch\"\nservice_tier = true\n")
	writeRole(t, dir, "b.toml", "name = \"b\"\ndescription = \"B.\"\ndeveloper_instructions = \"B.\"\ntools = [\"Bash\"]\nservice_tier = false\n")
	writeRole(t, dir, "c.md", "---\nname: c\ndescription: C.\nservice_tier: true\n---\nC.\n")
	writeRole(t, dir, "d.toml", "name = \"d\"\ndescription = \"D.\"\ndeveloper_instructions = \"D.\"\ntools = 3\n")

	roles, warnings := agents.LoadRoles(dir)

	require.Len(t, roles, 3, warnings)
	assert.Equal(t, []string{"Bash", "apply_patch"}, roles[0].Tools)
	assert.Equal(t, agents.TierPriority, roles[0].ServiceTier)
	assert.Equal(t, []string{"Bash"}, roles[1].Tools)
	assert.Equal(t, agents.TierDefault, roles[1].ServiceTier)
	assert.Equal(t, agents.TierPriority, roles[2].ServiceTier, "a Markdown file's service_tier may be a bool too")
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "d.toml")
}
