package engine

import (
	"context"
	"encoding/json"
	"time"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/approval"
	"github.com/viktordanov/uah/internal/sandbox"
)

// Subagents runs the child agents a session's agent starts; internal/agents
// implements it (see its README). The embedded engine offers the tools
// Attach returns and runs each call in the background as a remote job; it
// knows nothing of what the tools do.
type Subagents interface {
	// Attach records the parent's current run and returns the tools to
	// offer it: none when the session may not start children.
	Attach(parent AgentParent) []AgentTool
	// ToolNames are every tool name Call accepts, offered or not, so a
	// session with past calls resumes where the tools are not offered.
	ToolNames() []string
	// Call runs one of the parent's tool calls and returns the result for
	// the model. It blocks as long as the tool needs (a wait); ctx ends
	// when the call is canceled or the run stops.
	Call(ctx context.Context, call AgentCall) (string, error)
	// Interrupt stops the live runs of the parent's children and their
	// own children, because the user interrupted the parent.
	Interrupt(parentID string)
}

// AgentCall is one tool call of a parent session.
type AgentCall struct {
	ParentID string
	// CallID is the model's ID for the call; spawn_agent's fork_context
	// finds the request that made it by this ID. Empty for calls from before
	// uah recorded it.
	CallID string
	Tool   string
	Args   json.RawMessage
}

// Forker is an engine whose sessions can start from a copy of another
// session's history, and share its prompt cache. The embedded engine
// implements it; internal/agents uses it for spawn_agent's fork_context.
type Forker interface {
	// Fork creates the session childID from the parent's history as it was
	// when the model made the call callID: every item before that model
	// request, and the compaction that applied to it. The child's first
	// model request then starts with the parent's.
	Fork(ctx context.Context, parentID, childID, callID string) error
	// SetCacheKey makes the session's model requests use key as their
	// prompt cache key (the provider's cache affinity) instead of the
	// session's ID. Codex keys every agent of a tree by the root session.
	SetCacheKey(sessionID, key string)
}

// Scoper is an engine that can narrow one session's tools and let it run
// some actions without asking. The embedded engine implements it;
// internal/agents sets a child's scope from its agent definition.
type Scoper interface {
	// SetScope applies s to the session's runs from its next run; the zero
	// Scope removes it.
	SetScope(sessionID string, s Scope)
}

// Scope narrows a session: the tools it is offered and the actions it may
// run without asking. It never widens what the session's permission mode
// allows.
type Scope struct {
	// Tools are the tools the session is offered, by name; mcp__<server>
	// and mcp__<server>__* stand for every tool of a server. Nil offers
	// every tool, and an empty, non-nil list none.
	Tools []string
	// Approve are actions approved in advance: command prefixes, such as
	// "git status" or "apply_patch", and MCP tool names or server
	// patterns. They answer what would otherwise ask, so a forbid rule, the
	// approval policy never, and a read-only sandbox still hold.
	Approve []string
	// NeverAsk declines every action that would ask for approval, before
	// the auto-reviewer, as Codex's approval_policy = never does for its
	// /review thread.
	NeverAsk bool
	// CommandTimeout, when set, stops each command that runs longer, as
	// an interrupt stops it: its call ends with what it printed.
	CommandTimeout time.Duration
	// NoTools offers no tool and refuses every call, while the tools of
	// past calls still resolve, so the session's history still loads: a
	// /review's last turn, which can only answer.
	NoTools bool
}

// AgentParent is a parent session's live run.
type AgentParent struct {
	SessionID string
	// Request is the parent run's request as it started: its provider,
	// workspace, and system prompt hold for the run.
	Request core.Request
	// Settings are the parent's model, effort, service tier, adaptive
	// effort, and permission mode now, with the changes made during the
	// run (/model, /effort, /fast, a stricter mode): a child starts with
	// them, read when it is spawned or resumed (nil: the request's model and
	// effort).
	Settings func() LiveSettings
	// Ask asks the parent's user, also after the parent's run ends (nil:
	// no one can, so children are declined).
	Ask approval.Ask
	// Emit adds an event to the parent session's stream, also after the
	// run ends, such as AgentUpdated.
	Emit func(core.Event)
	// Inject gives the parent's agent a message without a turn of its own,
	// as a child's <subagent_notification> (nil: none); withdraw takes it back unsent.
	Inject func(text string) (withdraw func())
	// Grants are the parent session's grants, which its children share
	// (Options.Grants).
	Grants *sandbox.Grants
}

// AgentTool is a tool a run is offered, with its JSON Schema parameters.
type AgentTool struct {
	Name        string
	Description string
	Parameters  map[string]any
}

// Agent states, as Codex reports them.
const (
	AgentPendingInit = "pending_init"
	AgentRunning     = "running"
	AgentInterrupted = "interrupted"
	AgentCompleted   = "completed"
	AgentErrored     = "errored"
	AgentShutdown    = "shutdown"
	AgentNotFound    = "not_found"
)

// AgentUpdated reports a child's progress in its parent's stream. Every
// update carries the whole picture, so the latest one is enough to draw
// the child.
type AgentUpdated struct {
	At       time.Time
	ID       string
	Nickname string
	Role     string
	State    string
	// Message says why an errored child failed, in one line: the
	// provider's message when it gave one.
	Message string
	// Started is when the child's current work began.
	Started time.Time
	// CallID is the parent's spawn_agent call that started the child
	// ("" for a resumed child), Task that call's message, and Model and
	// Effort the child's settings as it runs.
	CallID string
	Task   string
	Model  string
	Effort string
	// Forked is a child started with fork_context.
	Forked bool
}

// AgentActivity is one of a child's tool events (core.ToolCalled,
// core.ToolStarted, or core.ToolFinished) in its parent's stream, for the
// detailed view.
type AgentActivity struct {
	At    time.Time
	ID    string
	Event core.Event
}

func (e AgentUpdated) OccurredAt() time.Time  { return e.At }
func (e AgentActivity) OccurredAt() time.Time { return e.At }
