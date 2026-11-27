package tomledit_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/config/tomledit"
)

const file = `# My settings
model = "gpt-6-sol" # the default
effort = "high"

# The TUI
[tui]
details = true

# Servers
[mcp_servers.docs]
url = "https://docs.example.com/mcp"
`

func TestSet(t *testing.T) {
	tests := []struct {
		name  string
		data  string
		key   []string
		value any
		want  string
	}{
		{
			name: "replaces a value in place, keeping its comment",
			data: file, key: []string{"model"}, value: "gpt-6-luna",
			want: `# My settings
model = "gpt-6-luna" # the default
effort = "high"

# The TUI
[tui]
details = true

# Servers
[mcp_servers.docs]
url = "https://docs.example.com/mcp"
`,
		},
		{
			name: "adds a root key after the last root key",
			data: file, key: []string{"auto_compact_percent"}, value: 80,
			want: `# My settings
model = "gpt-6-sol" # the default
effort = "high"
auto_compact_percent = 80

# The TUI
[tui]
details = true

# Servers
[mcp_servers.docs]
url = "https://docs.example.com/mcp"
`,
		},
		{
			name: "adds a key to its table",
			data: file, key: []string{"tui", "mouse"}, value: true,
			want: `# My settings
model = "gpt-6-sol" # the default
effort = "high"

# The TUI
[tui]
details = true
mouse = true

# Servers
[mcp_servers.docs]
url = "https://docs.example.com/mcp"
`,
		},
		{
			name: "a root key goes above the first table's comments",
			data: "# The TUI\n[tui]\ndetails = true\n", key: []string{"fast"}, value: true,
			want: "fast = true\n# The TUI\n[tui]\ndetails = true\n",
		},
		{
			name: "appends a missing table",
			data: "model = \"x\"", key: []string{"tui", "mouse"}, value: false,
			want: "model = \"x\"\n\n[tui]\nmouse = false\n",
		},
		{
			name: "keeps the dotted form of a table made by dotted keys",
			data: "tui.details = true\n", key: []string{"tui", "mouse"}, value: true,
			want: "tui.details = true\ntui.mouse = true\n",
		},
		{
			name: "an empty file",
			data: "", key: []string{"compact_model"}, value: "gpt-6-luna",
			want: "compact_model = \"gpt-6-luna\"\n",
		},
		{
			name: "a file without a final newline",
			data: "model = \"x\"", key: []string{"fast"}, value: true,
			want: "model = \"x\"\nfast = true\n",
		},
		{
			name: "replaces a multi-line string",
			data: "compact_prompt = \"\"\"\nline one\nline two\"\"\"\nfast = true\n", key: []string{"compact_prompt"}, value: "short",
			want: "compact_prompt = \"short\"\nfast = true\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tomledit.Set([]byte(tt.data), tt.key, tt.value)
			require.NoError(t, err)
			assert.Equal(t, tt.want, string(got))
		})
	}
}

func TestSetRefusesKeysItCannotEdit(t *testing.T) {
	_, err := tomledit.Set([]byte("tui = { details = true }\n"), []string{"tui", "mouse"}, true)
	require.ErrorIs(t, err, tomledit.ErrNotEditable)
	_, err = tomledit.Set([]byte("[tui]\n"), []string{"tui"}, true)
	require.ErrorIs(t, err, tomledit.ErrNotEditable)
	_, err = tomledit.Set([]byte("model = \n"), []string{"model"}, "x")
	require.ErrorContains(t, err, "failed to parse")
}

func TestUnset(t *testing.T) {
	got, ok, err := tomledit.Unset([]byte(file), []string{"tui", "details"})
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "# My settings\nmodel = \"gpt-6-sol\" # the default\neffort = \"high\"\n\n# The TUI\n[tui]\n\n# Servers\n[mcp_servers.docs]\nurl = \"https://docs.example.com/mcp\"\n", string(got))

	got, ok, err = tomledit.Unset([]byte(file), []string{"compact_model"})
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Equal(t, file, string(got))
}

func TestCutTables(t *testing.T) {
	data := file + "\n[mcp_servers.docs.env]\nA = \"1\"\n\n# Next\n[agents]\nenabled = true\n"
	got, cut, err := tomledit.CutTables([]byte(data), "mcp_servers", "docs")
	require.NoError(t, err)
	assert.True(t, cut)
	assert.Equal(t, "# My settings\nmodel = \"gpt-6-sol\" # the default\neffort = \"high\"\n\n# The TUI\n[tui]\ndetails = true\n\n# Servers\n# Next\n[agents]\nenabled = true\n", string(got))
}

func TestWriteKeepsThePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "config.toml")
	data, mode, err := tomledit.Read(path)
	require.NoError(t, err)
	assert.Empty(t, data)
	require.NoError(t, tomledit.Write(path, []byte("fast = true\n"), mode))
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestSetQuotesStrings(t *testing.T) {
	got, err := tomledit.Set(nil, []string{"compact_prompt"}, "say \"hi\"\\\nnow\x01")
	require.NoError(t, err)
	assert.Equal(t, "compact_prompt = \"say \\\"hi\\\"\\\\\\nnow\\u0001\"\n", string(got))
	got, err = tomledit.Set(nil, []string{"projects", "/my ws", "trusted"}, true)
	require.NoError(t, err)
	assert.Equal(t, "[projects.\"/my ws\"]\ntrusted = true\n", string(got))
}

// TestSetBeforeUpdateBlock: in a file with only comments, a new root key
// goes before the first block uah config update added, not after it, where
// uncommenting the block's table would take it.
func TestSetBeforeUpdateBlock(t *testing.T) {
	data := []byte("# mine\n\n# uah config update: [advisor], added commented out\n# [advisor]\n# model = \"m\"\n")
	out, err := tomledit.Set(data, []string{"effort"}, "high")
	require.NoError(t, err)
	assert.Equal(t, "# mine\n\neffort = \"high\"\n# uah config update: [advisor], added commented out\n# [advisor]\n# model = \"m\"\n", string(out))

	below := []byte("# uah config update: [advisor], added commented out\n# [advisor]\nmodel = \"m\"\n")
	out, err = tomledit.Set(below, []string{"effort"}, "high")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(out), "effort = \"high\"\n# uah config update"), "above the block, also when a root key is below it: %q", out)
}

// TestCutTablesKeepsUpdateBlock: cutting the last table stops at a block
// uah config update added after it.
func TestCutTablesKeepsUpdateBlock(t *testing.T) {
	block := "# uah config update: [advisor], added commented out\n# [advisor]\n# uah config update: [advisor] ends\n"
	data := []byte("[mcp_servers.demo]\ncommand = \"x\"\n\n" + block)
	out, cut, err := tomledit.CutTables(data, "mcp_servers", "demo")
	require.NoError(t, err)
	assert.True(t, cut)
	assert.Equal(t, block, string(out))

	below := []byte("[mcp_servers.demo]\ncommand = \"x\"\n\n" + block + "args = [\"-y\"]\n")
	out, _, err = tomledit.CutTables(below, "mcp_servers", "demo")
	require.NoError(t, err)
	assert.Equal(t, block, string(out), "a key of the table below the block goes too")

	inString := []byte("[mcp_servers.demo]\nargs = [\"\"\"\n# uah config update: [advisor] ends\n\"\"\"]\n")
	out, _, err = tomledit.CutTables(inString, "mcp_servers", "demo")
	require.NoError(t, err)
	assert.Empty(t, string(out), "a marker inside a value is the value's")

	indented := []byte("[mcp_servers.demo]\ncommand = \"x\"\n  " + block)
	out, _, err = tomledit.CutTables(indented, "mcp_servers", "demo")
	require.NoError(t, err)
	assert.Equal(t, "  "+block, string(out), "an indented block stays too")
}

// TestCommentLines finds a comment line, not the same text in a value.
func TestCommentLines(t *testing.T) {
	data := []byte("\uFEFF# mark\nprompt = \"\"\"\n# mark\n\"\"\"\n  # mark\n")
	at, err := tomledit.CommentLines(data, "# mark")
	require.NoError(t, err)
	assert.Len(t, at, 2, "after a BOM and indented, not the one in the string")
}
