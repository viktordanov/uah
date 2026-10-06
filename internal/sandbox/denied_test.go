package sandbox_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/viktordanov/uah/internal/sandbox"
)

func TestDenied(t *testing.T) {
	cases := []struct {
		name   string
		code   int
		output string
		want   bool
	}{
		{"git index.lock", 128, "fatal: Unable to create '/home/u/ws/.git/index.lock': Read-only file system\n", true},
		{"git on macOS", 128, "error: could not lock config file .git/config: Operation not permitted\n", true},
		{"touch outside workspace", 1, "touch: cannot touch '/home/u/x': Read-only file system\n", true},
		{"touch on macOS", 1, "touch: /Users/u/x: Operation not permitted\n", true},
		{"permission denied", 1, "bash: /etc/hosts: Permission denied\n", true},
		{"ls exit 2", 2, "ls: cannot open directory '/root': Permission denied\n", true},
		{"sandbox-exec", 71, "sandbox-exec: sandbox_apply: Operation not permitted\n", true},
		{"python write", 1, "PermissionError: [Errno 1] Operation not permitted: '/usr/x'\n", true},
		{"failed to write file", 1, "error: failed to write file `target/debug/.fingerprint`\n", true},
		{"curl no network", 6, "curl: (6) Could not resolve host: example.com\n", true},
		{"git fetch no network", 128, "fatal: unable to access 'https://github.com/x/y/': Could not resolve host: github.com\n", true},
		{"pip no network", 1, "WARNING: Retrying ... NewConnectionError: [Errno -3] Temporary failure in name resolution\n", true},
		{"connect no route", 7, "curl: (7) Failed to connect to 1.1.1.1 port 443: Network is unreachable\n", true},
		{"upper case", 1, "OPERATION NOT PERMITTED\n", true},
		{"success with keyword", 0, "chmod: Operation not permitted (ignored)\n", false},
		{"go test failure", 1, "--- FAIL: TestX (0.00s)\n    x_test.go:12: got 1, want 2\nFAIL\n", false},
		{"command not found", 127, "bash: line 1: frobnicate: command not found\n", false},
		{"grep no match", 1, "", false},
		{"compile error", 2, "./main.go:3:1: syntax error: non-declaration statement outside function body\n", false},
		{"make failure", 2, "make: *** [Makefile:3: all] Error 1\n", false},
		{"a search that mentions the sandbox", 1, "internal/sandbox/denied.go\nsandbox: see README\n", false},
		{"seccomp in a word", 1, "docs/seccomp.md: no match\n", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, sandbox.Denied(c.code, c.output, ""), "as stdout alone, as with 2>&1")
			assert.Equal(t, c.want, sandbox.Denied(c.code, "", c.output), "as stderr")
		})
	}
}

// TestDenied_StderrFirst: when a command wrote to stderr, only stderr
// tells a denial, so a denial phrase in its stdout, such as a matched log
// line, does not.
func TestDenied_StderrFirst(t *testing.T) {
	assert.False(t, sandbox.Denied(1, "log: Permission denied for user x\n", "grep: warning: something\n"))
	assert.True(t, sandbox.Denied(1, "partial output\n", "touch: /Users/u/x: Operation not permitted\n"))
}
