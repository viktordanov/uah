package embedded

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/viktordanov/uah/internal/sandbox"
)

// TestCommandPaths pins the paths an escalated command names: words with a
// slash or a dot directory, in arguments, redirections, flag values, and
// substitutions, a glob's directory, resolved against the working
// directory; not plain words.
func TestCommandPaths(t *testing.T) {
	cwd := sandbox.ResolvePath(t.TempDir())
	got := commandPaths(`cd ../wt && gofmt -w ./pkg/*.go > "/abs/out log" --file=rel/x $(cat /etc/conf) ~/y plain`, cwd)
	assert.Equal(t, []string{
		filepath.Join(filepath.Dir(cwd), "wt"),
		filepath.Join(cwd, "pkg"),
		filepath.Join(cwd, "rel", "x"),
		sandbox.ResolvePath("/etc/conf"),
		filepath.Join(cwd, "~", "y"),
		"/abs/out log",
	}, got)
	assert.Equal(t, []string{cwd}, commandPaths("ls . .", cwd), "each once")
	assert.Empty(t, commandPaths("echo 'unclosed", cwd), "a command that does not parse names none")
}
