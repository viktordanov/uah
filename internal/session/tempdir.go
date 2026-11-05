package session

import "path/filepath"

// The scratch directories a session keeps in its operation directory,
// sessions/operations/<id>/, so removing the session removes them too. The
// runner names the operation directories beside them by operation ID, never
// one of these names.
const (
	// TempDirName is the session's $TMPDIR (TempDir).
	TempDirName = "tmp"
	// ReviewTempDirName is the auto-reviewer's $TMPDIR (ReviewTempDir).
	ReviewTempDirName = "review-tmp"
)

// ScratchDirNames are the scratch directories in a session's operation
// directory. They hold temporary files and build caches, not tool output,
// so uagent's disk limit does not count them (harness.Config.DiskExclude).
func ScratchDirNames() []string { return []string{TempDirName, ReviewTempDirName} }

// TempDir is the session id's private temporary directory, $TMPDIR for its
// commands (sandbox.Policy.TempDir): tmp in its operation directory.
func TempDir(sessionsDir, id string) string {
	return filepath.Join(sessionsDir, "operations", id, TempDirName)
}

// ReviewTempDir is the temporary directory of the auto-reviewer's commands
// in session id: review-tmp in its operation directory.
func ReviewTempDir(sessionsDir, id string) string {
	return filepath.Join(sessionsDir, "operations", id, ReviewTempDirName)
}
