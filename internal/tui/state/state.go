// Package state is the TUI's model and reducer. It has no terminal or
// framework code: Reduce folds events and user intents into State and returns
// effects for the shell to run, so any renderer can drive it and plain tests
// can check it.
package state

import (
	"strings"
	"time"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/compaction"
	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/images"
	"github.com/viktordanov/uah/internal/session"
)

// Mode is the screen being shown.
type Mode int

const (
	ModeChat Mode = iota
	ModePicker
)

// Queued is a message waiting for the agent.
type Queued struct {
	ID   string
	Text string
	// AfterTool is a message held until the agent's tool call (enter).
	AfterTool bool
}

// Live is the run in progress.
type Live struct {
	RunID   string
	Started time.Time
	// What the run waits on (wait.go): the model request since Turn, its
	// latest Progress and Retry, an auto-review, hook, or compaction
	// aside from it, and since when the user stops the run.
	Turn, Stopping time.Time
	Progress       engine.ModelProgress
	Retry          *engine.Reconnecting
	Aside          *Wait
}

// Totals add up the session's finished runs.
type Totals struct {
	Runs        int
	Turns       int
	ToolCalls   int
	MaxParallel int
	Tokens      core.Tokens
	Overlap     time.Duration
	ToolBusy    time.Duration
}

// Picker is the session list. Like Codex, it shows the sessions of the
// current directory unless All is set.
type Picker struct {
	Local    []session.Info // sessions whose workspace is the current directory
	Sessions []session.Info // every session
	All      bool
	Filter   string
	Selected int
}

// State is everything the TUI shows.
type State struct {
	Mode      Mode
	SessionID string
	Resumed   bool
	Engine    string
	// Priority is whether the session's provider offers priority
	// processing, which /fast turns on (session.Session.Priority).
	Priority bool
	// Yolo is whether the session started with --yolo, which puts yolo
	// mode in the shift+tab cycle (session.Session.Yolo).
	Yolo     bool
	Settings session.Settings
	Files    []string // instruction files in the prompt
	// Home is the user's home directory, which tool lines show as ~. The
	// shell sets it.
	Home string
	// loadedWorkspace is the workspace of the run a loaded transcript is
	// at, for tool lines drawn before the session opens (toolcalls.go).
	loadedWorkspace string

	Items []Item
	index map[string]int
	// streaming are the items the model is still writing, oldest first
	// (stream.go).
	streaming []streamed

	Queue []Queued
	// Approvals are commands waiting for the user, in order.
	Approvals []Approval
	// Questions are the agent's questions waiting for the user's answers,
	// in order (questions.go).
	Questions []Questions
	Live      *Live
	Busy      bool // from a message sent until the session is idle
	Totals    Totals
	// ContextUsed is the tokens the last response used (0: unknown).
	ContextUsed int64
	// Usage is the subscription's usage, for /status and the footer (usage.go).
	Usage Usage

	ShowReasoning bool
	// Details shows turns, run dividers, and token totals; the default is a
	// compact, Codex-like view.
	Details bool
	Scroll  int // lines scrolled up from the bottom
	// Anchor, while the window is pinned (scrolled up, or a selection
	// being dragged), is the transcript line on its bottom row; the
	// renderer keeps that line there as the transcript grows below it or
	// changes above it, and the shell reports the window back (Anchored;
	// scroll.go).
	Anchor TextPos
	// NewBelow says output arrived below the pinned window, which the
	// renderer shows as a pill over its last row until the window follows
	// the bottom again.
	NewBelow bool
	// anchorLayout is the layout Anchor was reported in, so a resize or a
	// change of view does not count as new output.
	anchorLayout layout
	Picker       Picker
	Menu         Menu
	Now          time.Time
	// Windows finds a model's context window in the session's model
	// catalog, for the footer's "N% context left" (nil: the default
	// window). The shell sets it; the reducer only calls it.
	Windows compaction.WindowLookup
	// agentIDs are the subagents' IDs in the order they started, so lookups
	// of the agents do not walk the whole transcript (Agents).
	agentIDs []string
	// viewGen numbers the agent views opened (AgentView.Gen).
	viewGen int
	// View, when set, shows a subagent's transcript instead of the
	// session's (see agentview.go).
	View *AgentView
	// Config, when set, is the /config panel (see config.go).
	Config *ConfigPanel
	// ModelPicker, when set, is the /model panel (see modelpicker.go).
	ModelPicker *ModelPicker
	// Mouse reports the mouse to the TUI, so the wheel scrolls and a drag
	// selects transcript text.
	Mouse bool
	// Title shows the session's state in the terminal's title (title.go).
	Title bool
	// Shell is shell mode: enter runs the composer's line as a command
	// (shell.go).
	Shell bool
	// Keys is what enter, tab, and ctrl+enter do in this terminal
	// (sendkeys.go).
	Keys Keys
	// Reviewing is the running /review's ID ("": none; review.go).
	Reviewing string
	// Goal is the session's goal, nil without one (goal.go).
	Goal *GoalView
	// Attached are the images pasted into the composer, in order; each
	// placeholder in the draft names one (see images.go).
	Attached []images.Image
	// Backtrack, when set, is the earlier message selected to go back to
	// (backtrack.go).
	Backtrack *Backtrack
	// Find, when set, is an open search of the transcript
	// (transcriptsearch.go).
	Find *TranscriptSearch
	// History is the prompt history ↑ and ctrl+r recall (history.go).
	History PromptHistory
	// Selection, when set, is transcript text selected with the mouse
	// (selection.go); click counts double and triple clicks.
	Selection *Selection
	click     clicks
	// FileLinks is what a click on a file path does: LinksPeek,
	// LinksEditor, LinksOpen, or LinksOff, which draws no links ("" too).
	// The shell sets it from [tui] file_links (links.go).
	FileLinks string
	// Host is this machine's name, for the links' file:// URLs. The shell
	// sets it.
	Host string
	// Peek, when set, is a file shown in the overlay (peek.go).
	Peek *Peek
	// pressed is the link under the mouse's press, opened by a release
	// that selected nothing; unlinked are the agent's messages whose words
	// are not looked up yet (links.go).
	pressed  *FileLink
	unlinked []string
	// waiting is a clicked link that opens unless a second click comes
	// (LinkTimer with linkSeq).
	waiting *FileLink
	linkSeq int

	// Toast is a short note drawn over the transcript's corner for a
	// moment, such as the copy's (toast.go).
	Toast *Toast

	// Status is a transient hint in the footer, such as a pending confirmation.
	Status      string
	escArmed    time.Time
	quitArmed   time.Time
	Quitting    bool
	nextNoticeN int
}

// New returns an empty state.
func New(now time.Time) State {
	return State{index: map[string]int{}, Now: now}
}

// steerWhen is when a Steer's message reaches the working agent, by key:
// enter after its tool call, ctrl+enter and alt+enter now.
var steerWhen = map[string]session.When{KeyEnter: session.SendAfterTool, KeyCtrlEnter: session.SendNow, KeyAltEnter: session.SendNow}

// Intents are what the user asks for, translated from keys by the shell.
type (
	// Submit is Enter with the composer text; text starting with "/" and a command-like word is a command.
	Submit struct{ Text string }
	// Steer is a send key with the composer text while the agent works.
	Steer struct {
		Text string
		When session.When
	}
	// Esc is the Escape key; twice while busy interrupts, and twice on an
	// empty composer while idle goes back to an earlier message
	// (backtrack.go). Empty says the composer is empty.
	Esc struct{ Empty bool }
	// Quit is Ctrl+C with an empty composer; twice while busy quits.
	Quit struct{}
	// EditLastQueued is Up on an empty composer.
	EditLastQueued struct{}
	// ToggleDetails switches between the compact and the detailed view.
	ToggleDetails struct{}
	// ScrollBy scrolls the transcript; positive is up.
	ScrollBy struct{ Lines int }
	// ScrollToBottom follows new output again.
	ScrollToBottom struct{}
	// StepEffort lowers (-1) or raises (+1) the effort.
	StepEffort struct{ Delta int }
	// CycleAdaptive is alt+e: adaptive effort's next value, off, 1 step,
	// then 2 steps, for this session.
	CycleAdaptive struct{}
	// OpenPicker loads the session list.
	OpenPicker struct{}
	// Tick advances the clock for timers and spinners.
	Tick struct{ Now time.Time }
	// HistoryLoaded fills the transcript of a resumed session before it opens.
	HistoryLoaded struct {
		SessionID string
		Runs      []session.LoadedRun
	}
	// SessionsLoaded fills the picker. Local holds the current directory's
	// sessions; All shows every session from the start.
	SessionsLoaded struct {
		Sessions []session.Info
		Local    []session.Info
		All      bool
	}
	// ActivityLoaded carries runs per day (YYYY-MM-DD) for /status.
	ActivityLoaded struct {
		Counts map[string]int
	}
	// PickerToggleAll switches between this directory and all sessions.
	PickerToggleAll struct{}
	// PickerMove moves the picker selection.
	PickerMove struct{ Delta int }
	// PickerType edits the picker filter; Backspace is Text "\b".
	PickerType struct{ Text string }
	// PickerChoose opens the selected session.
	PickerChoose struct{}
	// PickerCancel closes the picker.
	PickerCancel struct{}
	// Failed reports an effect that failed.
	Failed struct{ Err error }
)

// Filtered returns the picker sessions in scope that match the filter.
func (p Picker) Filtered() []session.Info {
	list := p.Local
	if p.All {
		list = p.Sessions
	}
	if p.Filter == "" {
		return list
	}
	var out []session.Info
	for _, s := range list {
		if containsFold(s.ID, p.Filter) || containsFold(s.FirstPrompt, p.Filter) || containsFold(s.Model, p.Filter) {
			out = append(out, s)
		}
	}

	return out
}

// Item returns the item with key, if any.
func (s State) Item(key string) (Item, bool) {
	i, ok := s.index[key]
	if !ok {
		return Item{}, false
	}

	return s.Items[i], true
}

func containsFold(s, sub string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(sub))
}
