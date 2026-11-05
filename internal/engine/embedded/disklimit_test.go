package embedded_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/engine/embedded"
	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/testing/fakellm"
)

// TestEmbedded_DiskLimit: max_disk counts the tool output a session keeps,
// not its scratch directories. A session whose $TMPDIR and reviewer's
// directory hold more than the limit runs on, as one that builds Go code
// does, and one whose command output passes it is stopped. uagent measures
// every 5 s, so each command outlasts a measurement.
func TestEmbedded_DiskLimit(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for uagent's 5 s disk measurement")
	}
	const limit = 1 << 20
	tests := map[string]struct {
		command string
		scratch bool
		want    core.Status
	}{
		"scratch directories do not count": {command: "sleep 6", scratch: true, want: core.StatusOK},
		"tool output counts":               {command: "yes | head -c 2097152; sleep 6", want: core.StatusDiskLimit},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t, fakellm.Reply{Commands: []string{tc.command}}, fakellm.Reply{Text: "done"})
			eng := embedded.New(embedded.Config{StateDir: e.StateDir, Provider: "openai", Getenv: e.getenv, MaxDisk: limit})
			s, ev := e.open(t, eng, "")
			if tc.scratch {
				sessions := filepath.Join(e.StateDir, "sessions")
				for _, dir := range []string{session.TempDir(sessions, s.ID()), session.ReviewTempDir(sessions, s.ID())} {
					require.NoError(t, os.MkdirAll(filepath.Join(dir, "go-build"), 0o700))
					require.NoError(t, os.WriteFile(filepath.Join(dir, "go-build", "cache-d"), make([]byte, 2*limit), 0o600))
				}
			}

			_, err := s.Submit("run it")
			require.NoError(t, err)

			assert.Equal(t, tc.want, ev.finished().Status)
		})
	}
}
