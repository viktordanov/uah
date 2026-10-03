package engine

import (
	"time"

	"github.com/viktordanov/uah/internal/compaction"
)

// Engine events join a run's events.

// CompactionStarted means the engine is summarizing the context.
type CompactionStarted struct {
	At      time.Time
	Trigger compaction.Trigger
	// Tokens is the context in use that the last response reported.
	Tokens int64
}

// Compacted means a compaction finished. Err is empty on success; on failure
// the request went out uncompacted. Interrupted means the user's interrupt
// or the run's end stopped it (Err says so too). Warning, on success, is
// what the user should know, such as a context still above the automatic
// limit.
type Compacted struct {
	At          time.Time
	Trigger     compaction.Trigger
	Summary     string
	Err         string
	Interrupted bool
	Warning     string
	// Stats measure a compaction that succeeded (nil for one recorded
	// before uah measured them).
	Stats *compaction.Stats
}

func (e CompactionStarted) OccurredAt() time.Time { return e.At }
func (e Compacted) OccurredAt() time.Time         { return e.At }

// Rewound means the session went back to before the message MessageID
// (Rewinder): that message and everything after it left the model's
// context, and the session file keeps them. Tokens is the context in use
// that the last response before the message reported (0: unknown).
type Rewound struct {
	At        time.Time
	MessageID string
	Tokens    int64
}

func (e Rewound) OccurredAt() time.Time { return e.At }

// EffortUpdatesOff means the backend rejected a request's effort updates
// (its configuration_update items): the engine sent the request again
// without them, at the effort they set, and the session changes its effort
// per request from then on, resumed too. Err is the backend's error.
type EffortUpdatesOff struct {
	At  time.Time
	Err string
}

func (e EffortUpdatesOff) OccurredAt() time.Time { return e.At }

// Text is the one line a front end shows for it.
func (e EffortUpdatesOff) Text() string {
	return "effort updates were rejected by the backend; switching effort per request (cache misses on switches)"
}

// AutoReviewed reports the auto-reviewer's verdict on an action that needed
// approval. Outcome is allow, deny, or ask_user (the user decides).
// Duration is how long the review took, and the tokens are its model
// calls' (input includes cached). Delta means the review continued the
// session's review conversation with what changed since the last review,
// Forked that it ran on a copy because another review held the
// conversation, and Commands counts the reviewer's read-only commands.
type AutoReviewed struct {
	At       time.Time
	Command  string
	Outcome  string
	Risk     string
	Reason   string
	Duration time.Duration
	Delta    bool
	Forked   bool
	Commands int

	InputTokens, CachedInputTokens, OutputTokens int64
}

// AutoReviewing means the auto-reviewer started judging Command; an
// AutoReviewed follows, with Outcome "error" when the reviewer failed.
type AutoReviewing struct {
	At      time.Time
	Command string
}

func (e AutoReviewed) OccurredAt() time.Time  { return e.At }
func (e AutoReviewing) OccurredAt() time.Time { return e.At }

// Reconnecting means a model request failed, for a lost connection or an
// HTTP status the client retries, and the client tries again: attempt
// Attempt of MaxAttempts starts after about Delay. Reason is the failure.
// Offline means the network is unreachable: the client waits for it
// without using up an attempt.
type Reconnecting struct {
	At          time.Time
	Attempt     int
	MaxAttempts int
	Delay       time.Duration
	Reason      string
	Offline     bool
}

// ReconnectEnded means a model request that was retried stopped retrying:
// OK when a response arrived, otherwise it failed or was canceled.
type ReconnectEnded struct {
	At time.Time
	OK bool
}

func (e Reconnecting) OccurredAt() time.Time   { return e.At }
func (e ReconnectEnded) OccurredAt() time.Time { return e.At }

// Phases of a model request, in ModelProgress.Phase.
const (
	PhaseConnecting = "connecting"
	PhaseSending    = "sending"
	PhaseWaiting    = "waiting"
	PhaseStreaming  = "streaming"
	PhaseDone       = "done"
)

// ModelProgress reports where the turn's model request is: its Phase, the
// Bytes sent (while sending) or received, and the tool call the model is
// writing (Tool, the file it names in Target, ToolBytes so far; Tool is
// empty once the call is written). At is when data last arrived. Effort is
// the effort the request went at, which adaptive effort may set below the
// session's.
type ModelProgress struct {
	At        time.Time
	Phase     string
	Attempt   int
	Bytes     int64
	Tool      string
	Target    string
	ToolBytes int64
	Effort    string
}

func (e ModelProgress) OccurredAt() time.Time { return e.At }

// TextDelta is text the model is writing into its message ItemID, as it
// arrives. Final means the message is the final answer, when the provider
// says so. The runner's AssistantMessage for the response follows its
// deltas and is authoritative. Only runs with Options.Stream report it.
type TextDelta struct {
	At     time.Time
	ItemID string
	Text   string
	Final  bool
}

// ReasoningDelta is text of part Part of reasoning item ItemID's summary,
// as it arrives; the runner's ReasoningSummary for the part follows.
type ReasoningDelta struct {
	At     time.Time
	ItemID string
	Part   int
	Text   string
}

// StreamReset means the text streamed since the last response is void: its
// request failed, was canceled, or started over in a new attempt.
type StreamReset struct {
	At time.Time
}

func (e TextDelta) OccurredAt() time.Time      { return e.At }
func (e ReasoningDelta) OccurredAt() time.Time { return e.At }
func (e StreamReset) OccurredAt() time.Time    { return e.At }

// Web search actions, as the Responses API names them.
const (
	WebSearchSearch     = "search"
	WebSearchOpenPage   = "open_page"
	WebSearchFindInPage = "find_in_page"
)

// WebSearch is the provider's hosted web search at work in the model's
// item ItemID: Done is false when it starts, and true with its Action
// (search, open_page, find_in_page, or other) and details when it ends.
// The runner keeps no record of it, so it reaches only live runs.
type WebSearch struct {
	At      time.Time
	ItemID  string
	Done    bool
	Action  string
	Query   string
	URL     string
	Pattern string
}

func (e WebSearch) OccurredAt() time.Time { return e.At }

// Text says what the search did, as Codex's history cell does:
// "searched: <query>", "opened: <url>", or "searched: '<pattern>' in <url>".
func (e WebSearch) Text() string {
	switch {
	case !e.Done:
		return "searching the web"
	case e.Action == WebSearchOpenPage && e.URL != "":
		return "opened: " + e.URL
	case e.Action == WebSearchFindInPage && e.Pattern != "" && e.URL != "":
		return "searched: '" + e.Pattern + "' in " + e.URL
	case e.Action == WebSearchFindInPage && e.Pattern != "":
		return "searched: '" + e.Pattern + "'"
	case e.Action == WebSearchFindInPage && e.URL != "":
		return "searched page: " + e.URL
	case e.Query != "":
		return "searched: " + e.Query
	}

	return "searched the web"
}
