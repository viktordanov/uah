package harnesstest

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// Git runs git in dir and returns its trimmed output, failing the test when
// git fails and skipping it when git is not installed. It reads no user or
// system configuration, commits as a fixed identity, and lets a submodule
// come from a local path.
func Git(tb testing.TB, dir string, args ...string) string {
	tb.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		tb.Skip("no git")
	}
	argv := append([]string{
		"-C", dir, "-c", "user.name=uah test", "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false",
		"-c", "init.defaultBranch=main", "-c", "protocol.file.allow=always",
	}, args...)
	cmd := exec.CommandContext(tb.Context(), "git", argv...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	out, err := cmd.CombinedOutput()
	if err != nil {
		tb.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}

	return strings.TrimSpace(string(out))
}
