package engine

import (
	"time"
)

// AdvisorToolName is the advisor tool: a call sends the conversation so far
// to a stronger model, which answers with advice (docs/design/advisor.md).
const AdvisorToolName = "advisor"

// AdvisorPlanType is the remote job plan an advisor call runs as. Its
// finished job keeps the advice, or why there is none, as its result.
const AdvisorPlanType = "uah.advisor"

// The advisor's defaults: how long a call waits for the advice, how many
// calls a run makes, and how many tokens (estimated) of the transcript a
// command backend reads.
const (
	DefaultAdvisorTimeout        = 10 * time.Minute
	DefaultAdvisorMaxUses        = 5
	DefaultAdvisorMaxInputTokens = 150000
)

// The command backend's input formats ([advisor] input).
const (
	AdvisorInputText = "text"
	AdvisorInputJSON = "json"
)

// EnvAdvisor is on or off: off turns the advisor off. The command backend
// sets it to off for its command, so a command that runs uah does not
// advise itself.
const EnvAdvisor = "UAH_ADVISOR"

// AdvisorLogSuffix ends the name of a session's advisor log,
// sessions/<id>.advisor.jsonl: one AdvisorRecord per call that reached
// the advisor's model or ran its command.
const AdvisorLogSuffix = ".advisor.jsonl"

// AdvisorCommandPrefix starts the Model of a command backend's
// AdvisorRecord, before the command's program: "command:claude". Its
// usage is zero, as a command's tokens are not known.
const AdvisorCommandPrefix = "command:"

// AdvisorRecord is one advisor call in a session's advisor log: when it
// ran, the model (or AdvisorCommandPrefix and the command's program) and
// effort, how long it took, what it used, and its error.
type AdvisorRecord struct {
	At         time.Time    `json:"at"`
	CallID     string       `json:"call_id"`
	Model      string       `json:"model"`
	Effort     string       `json:"effort,omitempty"`
	DurationMS int64        `json:"duration_ms"`
	Usage      AdvisorUsage `json:"usage"`
	Error      string       `json:"error,omitempty"`
}

// AdvisorUsage is an advisor call's tokens.
type AdvisorUsage struct {
	Input     int64 `json:"input"`
	Cached    int64 `json:"cached,omitempty"`
	Output    int64 `json:"output"`
	Reasoning int64 `json:"reasoning,omitempty"`
}

// Advisor is an engine whose sessions can have an advisor, which /advisor
// shows and changes.
type Advisor interface {
	// Advisor applies /advisor's argument to the session, whose provider
	// is provider now, and its subagents, and says what the advisor is
	// then: "" shows it, "off" turns it off, "on" turns it back on,
	// "command" goes back to the configured command, and a model, with an
	// optional effort after it, picks the model backend's model.
	Advisor(sessionID, provider, arg string) (string, error)
}
