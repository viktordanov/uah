package engine

import "time"

// ToolBridgeCalled reports a call a command made through the session's
// tool bridge (`uah tool call`, internal/toolbridge): which tool, with
// what (cut), how it ended, and how long it took.
type ToolBridgeCalled struct {
	At        time.Time
	Name      string
	Arguments string
	OK        bool
	Error     string
	Duration  time.Duration
}

func (e ToolBridgeCalled) OccurredAt() time.Time { return e.At }
