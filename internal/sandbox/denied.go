// Adapted from openai/codex rust-v0.156.1 (Apache-2.0):
// codex-rs/sandboxing/src/denial.rs.

package sandbox

import "strings"

// deniedKeywords are Codex's SANDBOX_DENIED_KEYWORDS, matched in lower case,
// without its bare "sandbox", "seccomp", and "landlock": those name Codex's
// own filters, which uah does not use (Seatbelt and bubblewrap make a
// denied call fail with the errors below), and as plain words they flagged
// any failed command whose output mentioned them, such as a search for
// "sandbox" that ended with exit 1.
var deniedKeywords = []string{
	"operation not permitted",
	"permission denied",
	"read-only file system",
	"failed to write file",
}

// networkKeywords are uah's addition. Codex's seccomp filter makes connect
// fail with "operation not permitted", which the keywords above catch; bwrap's
// --unshare-net and Seatbelt's network deny instead leave no DNS and no route,
// so resolvers and clients print these.
var networkKeywords = []string{
	"could not resolve host",
	"temporary failure in name resolution",
	"name or service not known",
	"network is unreachable",
}

// Denied reports whether a failed sandboxed command was probably stopped by
// the sandbox, as Codex guesses: a non-zero exit code and an error that
// mentions a denial. Errors go to stderr, so only stderr is read when the
// command wrote any; stdout is read when stderr is empty, as for a command
// run with 2>&1. There is no certain way to tell; a command can print
// "permission denied" for its own reasons. Codex checks the keywords before
// its quick-reject exit codes (2, 126, 127), so those codes never override a
// keyword; its 128+SIGSYS rule is for its seccomp filter, which uah has not.
func Denied(exitCode int, stdout, stderr string) bool {
	if exitCode == 0 {
		return false
	}
	output := stderr
	if strings.TrimSpace(output) == "" {
		output = stdout
	}
	lower := strings.ToLower(output)
	for _, k := range deniedKeywords {
		if strings.Contains(lower, k) {
			return true
		}
	}
	for _, k := range networkKeywords {
		if strings.Contains(lower, k) {
			return true
		}
	}

	return false
}
