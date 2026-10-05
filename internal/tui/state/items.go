package state

import (
	"time"

	"github.com/viktordanov/uah/internal/cmdparse"
	"github.com/viktordanov/uah/internal/contextusage"
	"github.com/viktordanov/uah/internal/engine"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/gitdiff"
	"github.com/viktordanov/uah/internal/mcp"
	"github.com/viktordanov/uah/internal/patch"
)

// Kind is what a transcript item shows.
type Kind int

const (
	KindUser Kind = iota
	KindRun
	KindTurn
	KindTool
	KindAssistant
	KindReasoning
	KindNotice
	// KindAgent is a subagent: Name is its nickname, Label its role, Text
	// its ID, Detail its state, Started when its current work began, and
	// Sub its latest tool calls (KindTool items) for the detailed view.
	KindAgent
	// KindContext is a /context breakdown in Context.
	KindContext
	// KindFinish ends a run: its Status, Started, and Wall.
	KindFinish
	// KindMCP is the /mcp panel: MCP holds the servers, and Final asks for
	// the verbose form.
	KindMCP
	// KindShell is a command the user ran in shell mode (shell.go): Text
	// is the command, Detail its output, Tool its state, Exit its exit
	// code, Label why it was refused, and Input whether the agent has it.
	KindShell
	// KindDiff is /diff's changes in GitDiff (review.go).
	KindDiff
	// KindReview is a /review in Review: running, then its findings
	// (review.go).
	KindReview
)

// InputState tracks a user message from the queue to the runner.
type InputState string

const (
	InputQueued    InputState = "queued"
	InputSent      InputState = "sent"
	InputDelivered InputState = "delivered"
	InputFailed    InputState = "failed"
)

// ToolState tracks one tool call.
type ToolState string

const (
	ToolCalled  ToolState = "called"
	ToolRunning ToolState = "running"
	ToolOK      ToolState = "ok"
	ToolFailed  ToolState = "failed"
	ToolStopped ToolState = "stopped" // still running when its run ended
)

// Item is one transcript entry. Fields apply by Kind. Version grows on every
// change, so renderers can cache by (Key, Version).
type Item struct {
	Kind    Kind
	Key     string
	Version int

	// KindUser, KindAssistant, KindReasoning, KindNotice
	Text string
	// Raw is a KindUser message as sent, with its image tags.
	Raw   string
	Input InputState
	Final bool
	Level string // KindNotice: "info", "warning", "error"
	// Streaming means the model is still writing a KindAssistant or
	// KindReasoning item (stream.go).
	Streaming bool
	// Links are the words of a KindAssistant message that name files in
	// the workspace (links.go), and linked the text they were looked up in.
	Links  map[string]FileLink
	linked string

	// KindRun
	RunID  string
	Status core.Status
	Wall   time.Duration
	Tokens int64

	// KindTurn
	Turn     int
	In, Out  int64
	Pending  bool
	Started  time.Time
	Duration time.Duration

	// KindMCP
	MCP []mcp.ServerStatus

	// KindTool
	Name   string
	Label  string
	Tool   ToolState
	Detail string
	// Diff is what an applied apply_patch call changed (engine.PatchApplied).
	Diff []patch.FileDiff
	// Command is a Bash call's command, whole, as the model sent it.
	Command string
	// Verb and Parts are the call's compact line, shaped once when it
	// arrives (toolcalls.go): Verb its label (READ, LIST, SEARCH, ASK; ""
	// for the tool's own) and Parts its text in styled runs (nil: Label).
	Verb  string
	Parts []cmdparse.Part
	// ErrorLine says why the call failed; Result sums up an MCP call's
	// result; Note is the auto-reviewer's approval of it (toolcalls.go).
	ErrorLine, Result, Note string
	// Answers are what the user answered to a request_user_input call, a
	// line per question (questions.go).
	Answers []string
	// Group are the names on a call's compact line when calls after it
	// joined it (skills loaded one after another); MergedInto is the key
	// of the call whose line shows this one.
	Group      []string
	MergedInto string

	// KindContext
	Context *contextusage.Usage

	// KindShell
	Exit int

	// KindAgent
	Sub []Item
	// Agent is the latest update of a KindAgent: its spawn call's ID and
	// message, model, effort, and why it failed.
	Agent *engine.AgentUpdated

	// KindDiff
	GitDiff *gitdiff.Diff
	// KindReview
	Review *Review
}

// Live reports whether an item changes with time (spinners, elapsed times)
// and must not be served from a cache.
func (it Item) Live() bool {
	return (it.Kind == KindTurn && it.Pending) || (it.Kind == KindTool && (it.Tool == ToolRunning || it.Tool == ToolCalled)) ||
		(it.Kind == KindShell && it.Tool == ToolRunning) ||
		(it.Kind == KindRun && it.Status == core.StatusRunning) || (it.Kind == KindAgent && it.Detail == engine.AgentRunning) ||
		(it.Kind == KindReview && it.Review != nil && it.Review.Running)
}
