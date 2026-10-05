package session

import (
	"errors"
	"path/filepath"

	"github.com/viktordanov/uagent/harness"
)

// InUse reports whether a run holds the session's lock now, in this
// process or another: a second run of it would fail to start with
// harness.ErrSessionBusy. It takes the lock and lets it go at once.
func InUse(sessionsDir, id string) bool {
	unlock, err := harness.LockSession(filepath.Dir(sessionsDir), id)
	if err != nil {
		return errors.Is(err, harness.ErrSessionBusy)
	}
	_ = unlock()

	return false
}
