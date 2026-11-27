package config_test

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/config"
)

// TestReferenceListsEveryKey keeps docs/configuration.md complete: every
// TOML key of Config, including nested tables and MCP servers, must appear
// there in backticks, alone or as a table header.
func TestReferenceListsEveryKey(t *testing.T) {
	doc, err := os.ReadFile("../../docs/configuration.md")
	require.NoError(t, err)

	keys := config.Keys()
	require.Greater(t, len(keys), 50, "the walk found the nested keys")
	text := string(doc)
	for _, key := range keys {
		leaf := key[strings.LastIndex(key, ".")+1:]
		// A table may appear as its header: `[tui]`, `[[hooks.<Event>]]`.
		mentioned := strings.Contains(text, "`"+leaf+"`") || strings.Contains(text, "`["+leaf) ||
			strings.Contains(text, "`[["+leaf) || strings.Contains(text, "."+leaf+"`")
		assert.True(t, mentioned, "docs/configuration.md does not mention %s in backticks", key)
	}
}
