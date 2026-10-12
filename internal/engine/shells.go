package engine

import (
	"encoding/json"
	"strings"
	"time"
)

// Background shells: a Bash call with background set starts its command
// detached from the run, as one of the session's shells. The session hears
// of each one through ShellUpdated, and the agent of its end through a
// <background_shell_notification>, which goes the way a subagent's
// notification goes (Options.Inject).

// Shell states.
const (
	ShellRunning = "running"
	ShellExited  = "exited"
	ShellKilled  = "killed"
)

// ShellUpdated reports a background shell in its session's stream. Every
// update carries the whole picture, so the latest one is enough to draw it.
type ShellUpdated struct {
	At      time.Time
	ID      string
	Command string
	State   string
	// Group is its process group, Log the file its output goes to, and
	// ExitCode how it ended (128 plus the signal for a signal).
	Group    int
	Log      string
	ExitCode int
	Started  time.Time
	Ended    time.Time
	// CallID is the Bash call that started it.
	CallID string
}

func (e ShellUpdated) OccurredAt() time.Time { return e.At }

// ShellStopper is an engine that runs background shells; the session
// stops one through it (Session.StopShell).
type ShellStopper interface {
	StopShell(sessionID, id string) error
}

// ShellEnd is how a background shell ended, as its notification and
// wait_shell give it to the agent.
type ShellEnd struct {
	ID       string `json:"shell_id"`
	Command  string `json:"command"`
	State    string `json:"state"`
	ExitCode *int   `json:"exit_code,omitempty"`
	Ran      string `json:"ran"`
	Log      string `json:"log"`
	// Tail is the end of its output.
	Tail string `json:"output_tail,omitempty"`
}

const (
	shellNotificationOpen  = "<background_shell_notification>"
	shellNotificationClose = "</background_shell_notification>"
)

// ShellNotification is the message that tells the agent a background shell
// ended.
func ShellNotification(end ShellEnd) (string, error) {
	body, err := json.Marshal(end)
	if err != nil {
		return "", err // a plain encoding
	}

	return shellNotificationOpen + "\n" + string(body) + "\n" + shellNotificationClose, nil
}

// ParseShellNotification reads such a message; ok is false for any other
// text.
func ParseShellNotification(text string) (ShellEnd, bool) {
	body, found := strings.CutPrefix(strings.TrimSpace(text), shellNotificationOpen)
	if !found {
		return ShellEnd{}, false
	}
	body, _ = strings.CutSuffix(body, shellNotificationClose)
	var end ShellEnd
	if json.Unmarshal([]byte(strings.TrimSpace(body)), &end) != nil {
		return ShellEnd{}, false
	}

	return end, true
}
