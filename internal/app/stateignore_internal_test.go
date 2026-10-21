package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The state directory's .ignore is uah's while it starts with the marker:
// written when missing, rewritten when it differs, and a user's own kept.
func TestWriteStateIgnore(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	path := filepath.Join(dir, ".ignore")

	require.NoError(t, writeStateIgnore(dir))
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, stateIgnore, string(got))
	assert.Contains(t, string(got), "\n/sessions/\n")
	assert.NotContains(t, string(got), "config", "the configuration stays searchable")

	require.NoError(t, os.WriteFile(path, []byte(stateIgnoreMarker+" an older version\n/sessions/\n"), 0o600))
	require.NoError(t, writeStateIgnore(dir))
	got, _ = os.ReadFile(path)
	assert.Equal(t, stateIgnore, string(got), "uah's older version is rewritten")

	require.NoError(t, os.WriteFile(path, []byte("/runs/\n"), 0o600))
	require.NoError(t, writeStateIgnore(dir))
	got, _ = os.ReadFile(path)
	assert.Equal(t, "/runs/\n", string(got), "the user's own stays")
}
