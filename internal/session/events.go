package session

import (
	"time"

	"github.com/viktordanov/uagent/core"
)

// Session events join the run events in one ordered stream.

// SessionOpened is always the first event of a session.
type SessionOpened struct {
	At       time.Time
	ID       string
	Resumed  bool
	Engine   string
	Settings Settings
}

// InstructionsLoaded lists the instruction files in the host prompt. It
// follows SessionOpened when instructions were loaded.
type InstructionsLoaded struct {
	At        time.Time
	Files     []string
	Bytes     int
	Truncated bool
}

// InputQueued means the session accepted a message.
type InputQueued struct {
	At    time.Time
	Input core.UserInput
	// AfterTool marks a message held for the tool call (SendAfterTool).
	AfterTool bool
}

// InputSent means the messages went to the runner, in a new run or live.
type InputSent struct {
	At  time.Time
	IDs []string
}

// InputDelivered means the runner acknowledged a message by echoing its ID.
type InputDelivered struct {
	At time.Time
	ID string
}

// InputFailed means messages did not reach the runner. Reason says why.
type InputFailed struct {
	At     time.Time
	IDs    []string
	Reason string
}

// InputWithdrawn means a queued message was taken back before it was sent.
type InputWithdrawn struct {
	At time.Time
	ID string
}

// Applied says when a settings change takes effect.
type Applied string

const (
	AppliedLive    Applied = "live"
	AppliedNextRun Applied = "next_run"
)

// SettingsChanged reports new settings and when they apply.
type SettingsChanged struct {
	At       time.Time
	Settings Settings
	Applied  Applied
}

// Idle means no run is live and nothing is queued for one.
type Idle struct {
	At time.Time
}

// Notice levels.
const (
	LevelInfo    = "info"
	LevelWarning = "warning"
	LevelError   = "error"
)

// Notice is a message for the user that is not tied to one event, such as a
// run that could not start.
type Notice struct {
	At      time.Time
	Level   string // "info", "warning", or "error"
	Message string
	// StartFailed marks the error of a run that could not start, also one
	// started for injected messages alone, which fails no input.
	StartFailed bool
}

// HookRan reports one hook run.
type HookRan struct {
	At       time.Time
	Event    string
	Command  string
	Source   string
	Outcome  string // "ok", "blocked", "error", or "skipped"
	Reason   string
	Duration time.Duration
}

func (e SessionOpened) OccurredAt() time.Time      { return e.At }
func (e HookRan) OccurredAt() time.Time            { return e.At }
func (e InstructionsLoaded) OccurredAt() time.Time { return e.At }
func (e InputQueued) OccurredAt() time.Time        { return e.At }
func (e InputSent) OccurredAt() time.Time          { return e.At }
func (e InputDelivered) OccurredAt() time.Time     { return e.At }
func (e InputFailed) OccurredAt() time.Time        { return e.At }
func (e InputWithdrawn) OccurredAt() time.Time     { return e.At }
func (e SettingsChanged) OccurredAt() time.Time    { return e.At }
func (e Idle) OccurredAt() time.Time               { return e.At }
func (e Notice) OccurredAt() time.Time             { return e.At }
