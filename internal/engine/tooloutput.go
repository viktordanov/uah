package engine

import (
	"bytes"
	"encoding/json"
	"time"
	"unicode/utf8"
)

// MCPPlanType is the remote job plan an MCP tool call runs as. Its
// finished job keeps the call's result text, or its error, in the job's
// state.
const MCPPlanType = "uah.mcp_call"

// The runner's names in a session item: an operation's type for a remote
// job, the kind of an item with a call's status, and an operation's status
// once it completed.
const (
	remoteJob      = "remote_job"
	toolCallStatus = "tool_call_status"
	opCompleted    = "completed"
)

// outputKeep is how much of a command's output or an MCP result a
// ToolOutput keeps: enough for the line the TUI shows.
const outputKeep = 4096

// ToolOutput is what a finished call left for its line in the transcript:
// the end of a failed shell command's output (its stderr, else its
// stdout), an MCP call's result or error, or an advisor call's advice. The runner's ToolFinished
// carries only the exit code or the operation's status. The embedded
// engine adds it when the session stores the call's finished operation,
// and session.Load adds it to a loaded transcript, from the same item.
type ToolOutput struct {
	At     time.Time
	CallID string
	// Output is the last 4 KB of a failed command's stderr, or of its
	// stdout when stderr is empty, or its terminal error.
	Output string
	// Result is the first 4 KB of an MCP call's result text, or an
	// advisor call's advice whole, and Size its length in bytes.
	Result string
	Size   int
	// Error is a failed MCP call's error.
	Error string
}

func (e ToolOutput) OccurredAt() time.Time { return e.At }

// toolStatusItem is the part of a tool_call_status item ToolOutputFromItem
// reads.
type toolStatusItem struct {
	RecordedAt time.Time
	Kind       string
	Data       struct {
		CallID     string
		Operations []struct {
			Type, Status string
			State        opState
		}
	}
}

// opState is the part of an operation's state ToolOutputFromItem reads.
type opState struct {
	Plan   struct{ Type string }
	Result *struct {
		Out, Err string
		ExitCode int
	}
	TerminalError  string
	TerminalResult string
}

// ToolOutputFromItem reads a session item, one line of the session file as
// the runner writes it, and returns the output of a shell command that
// failed, or of an MCP or advisor call that finished.
func ToolOutputFromItem(line []byte) (ToolOutput, bool) {
	if !bytes.Contains(line, []byte(`"tool_call_status"`)) || !mayHaveOutput(line) {
		return ToolOutput{}, false
	}
	var item toolStatusItem
	if json.Unmarshal(line, &item) != nil || item.Kind != toolCallStatus {
		return ToolOutput{}, false
	}
	out := ToolOutput{At: item.RecordedAt, CallID: item.Data.CallID}
	for _, op := range item.Data.Operations {
		st := op.State
		if op.Type == remoteJob {
			if jobOutput(op.Status, st, &out) {
				return out, true
			}

			continue
		}
		switch {
		case op.Type == "shell" && st.TerminalError != "":
			out.Output = st.TerminalError

			return out, true
		case op.Type == "shell" && st.Result != nil && st.Result.ExitCode != 0:
			out.Output = tail(st.Result.Err)
			if out.Output == "" {
				out.Output = tail(st.Result.Out)
			}

			return out, true
		}
	}

	return ToolOutput{}, false
}

// jobOutput puts what a remote job left in out: a finished MCP call's
// result or error, or an advisor call's advice, whole, for the detailed
// view, or why it got none, with what the agent read then. It is false
// for any other job.
func jobOutput(status string, st opState, out *ToolOutput) bool {
	switch {
	case st.Plan.Type == MCPPlanType && status == opCompleted:
		out.Result, out.Size = head(st.TerminalResult), len(st.TerminalResult)
	case st.Plan.Type == AdvisorPlanType && status == opCompleted:
		out.Result, out.Size = st.TerminalResult, len(st.TerminalResult)
	case st.Plan.Type == MCPPlanType && st.TerminalError != "":
		out.Error = st.TerminalError
	case st.Plan.Type == AdvisorPlanType && st.TerminalError != "":
		// a call that got no advice: why, and the command's stderr after it
		out.Error, out.Result, out.Size = st.TerminalError, st.TerminalResult, len(st.TerminalResult)
	default:
		return false
	}

	return true
}

// mayHaveOutput tells, without parsing, whether an item as encoding/json
// writes it can have a ToolOutput: an MCP or advisor call's plan type, a terminal
// error, or a nonzero exit code (a JSON number never starts 0 unless it is
// 0). Most status items are of commands running or done well, with their
// whole output, which a parse would copy for nothing.
func mayHaveOutput(line []byte) bool {
	return bytes.Contains(line, []byte(MCPPlanType)) || bytes.Contains(line, []byte(AdvisorPlanType)) ||
		followedByOther(line, `"TerminalError":"`, '"') || followedByOther(line, `"ExitCode":`, '0')
}

// followedByOther reports whether a byte other than b follows any key in line.
func followedByOther(line []byte, key string, b byte) bool {
	for {
		i := bytes.Index(line, []byte(key))
		if i < 0 {
			return false
		}
		line = line[i+len(key):]
		if len(line) > 0 && line[0] != b {
			return true
		}
	}
}

// head is the first outputKeep bytes of s, cut at a rune.
func head(s string) string {
	if len(s) <= outputKeep {
		return s
	}
	i := outputKeep
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}

	return s[:i]
}

// tail is the last outputKeep bytes of s, cut at a rune.
func tail(s string) string {
	if len(s) <= outputKeep {
		return s
	}
	i := len(s) - outputKeep
	for i < len(s) && !utf8.RuneStart(s[i]) {
		i++
	}

	return s[i:]
}
