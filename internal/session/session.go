// Package session is the long-lived object the TUI and `uah exec` talk to. A
// session owns its settings, a queue of messages, and at most one live run,
// and it merges run events and its own events into one ordered stream.
package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/approval"
	"github.com/viktordanov/uah/internal/contextusage"
	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/goal"
	"github.com/viktordanov/uah/internal/hooks"
	"github.com/viktordanov/uah/internal/mcp"
	"github.com/viktordanov/uah/internal/usershell"
)

// ErrClosed means the session has been closed.
var ErrClosed = errors.New("session is closed")

const eventBuffer = 4096

// State is where the session is in its run lifecycle.
type State string

const (
	StateIdle     State = "idle"
	StateStarting State = "starting"
	StateRunning  State = "running"
	StateStopping State = "stopping"
	StateClosed   State = "closed"
)

// Options configure a new session.
type Options struct {
	// ID resumes that runner session; empty starts a new one.
	ID string
	// Resumed marks an ID that already has runs, for SessionOpened.
	Resumed  bool
	Settings Settings
	// Instructions, when set, is emitted after SessionOpened.
	Instructions *InstructionsLoaded
	// Hooks run at session events (nil runs none).
	Hooks *hooks.Runner
	// SessionsDir holds the runner session files; hooks get the file's path.
	SessionsDir string
	// Notices are shown after SessionOpened, such as configuration warnings.
	Notices []string
	// Source (SourceTUI or SourceRun) is recorded in a new session's sidecar
	// in SessionsDir when set.
	Source string
	// Interactive means a user answers approvals (ApprovalRequested and
	// Resolve). Otherwise commands that need approval are denied. An
	// interactive session also connects the engine's MCP servers as it
	// opens (MCPStarted).
	Interactive bool
	// Parent is the spawning session of a subagent, recorded in the sidecar.
	Parent string
	// Ask, when set, answers this session's approvals instead of its own
	// prompts: a subagent asks through its parent.
	Ask approval.Ask
	// Shell runs the commands the user types (RunShell); nil: none.
	Shell *usershell.Runner
	// Stream asks the engine for the model's text as it arrives
	// (engine.TextDelta), for a TUI or `uah exec --json`.
	Stream bool
	// FirstPrompt is a resumed session's first message, from its runs, for
	// a sidecar from before uah kept it.
	FirstPrompt string
	// Yolo allows yolo mode (--yolo): without it, SetSettings refuses the
	// mode.
	Yolo bool
	// Goals configures /goal ([goals]). A subagent's session (Parent set)
	// never has a goal.
	Goals goal.Settings
}

// Session is safe to use from any goroutine. All state lives on one internal
// goroutine, which is also the only writer of Events.
type Session struct {
	id  string
	eng engine.Engine
	// priority is Engine.Priority, read once when the session opens.
	priority bool
	// yolo is Options.Yolo.
	yolo bool
	in   chan any
	out  chan core.Event
	ctx  context.Context
	stop context.CancelFunc
	done chan struct{}

	// Owned by the loop goroutine.
	settings Settings
	state    State
	queue    []core.UserInput
	sent     map[string]bool
	// live are messages sent into a running run, in order; the ones it had
	// not read when it ended go out again with the next run.
	live []core.UserInput
	// startSteers are steers made while a run was starting.
	startSteers          []core.UserInput
	run                  engine.Run
	restartAfterStop     bool
	interruptWhenStarted bool
	closeReply           chan error
	// compactPending is a /compact the engine has not started yet.
	compactPending bool
	// compactFocus is what the pending /compact asked the summary to focus on.
	compactFocus string
	// clearPending is a /clear the engine has not started yet.
	clearPending bool
	// held are injected messages waiting for the next run (Inject).
	held        []core.UserInput
	hooks       hookState
	interactive bool
	// stream is Options.Stream, for each run.
	stream bool
	// approvals are the pending approvals' reply channels by ID.
	approvals map[string]pending
	// questions are the pending questions' reply channels by ID.
	questions map[string]chan engine.Answers
	// askOverride is Options.Ask.
	askOverride approval.Ask
	// sessionsDir holds the sidecar the settings are saved in ("": none).
	sessionsDir string
	// shell runs the user's commands; shells stops each running one by ID.
	shell  *usershell.Runner
	shells map[string]context.CancelFunc
	// reviewStop stops the running /review (Review), nil when none runs.
	reviewStop context.CancelFunc
	// firstPromptPending is a new session whose sidecar has no first
	// message yet; the first run records it.
	firstPromptPending bool
	// afterTool marks the queued SendAfterTool messages; modelBusy, a model request under way.
	afterTool  map[string]bool
	modelBusy  bool
	savedQueue []string // what the sidecar keeps as Queued
	// goal is the session's goal and its accounting (goal.go).
	goal goalState
}

// Open starts a session. Its first event is SessionOpened.
func Open(ctx context.Context, eng engine.Engine, opts Options) (*Session, error) {
	if err := opts.Settings.Validate(); err != nil {
		return nil, fmt.Errorf("failed to open session: %w", err)
	}
	if opts.Settings.Mode.AsksNoOne() && !opts.Yolo {
		return nil, errors.New("failed to open session: yolo mode needs --yolo")
	}
	id := opts.ID
	if id == "" {
		id = uuid.NewString()
	}
	// The kept queue is read first: Open reports it before it returns, so
	// Events must hold all of it, however long.
	var sc Sidecar
	var scErr error
	if opts.SessionsDir != "" {
		sc, _, scErr = ReadSidecar(opts.SessionsDir, id)
	}
	runCtx, stop := context.WithCancel(ctx)
	s := &Session{
		id: id, eng: eng, priority: eng.Priority(), yolo: opts.Yolo,
		in: make(chan any, eventBuffer), out: make(chan core.Event, eventBuffer+queuedRoom(sc)),
		ctx: runCtx, stop: stop, done: make(chan struct{}),
		settings: opts.Settings, state: StateIdle, sent: map[string]bool{}, afterTool: map[string]bool{},
		hooks:       hookState{runner: opts.Hooks, resumed: opts.Resumed, tools: map[string]core.ToolCalled{}},
		interactive: opts.Interactive, stream: opts.Stream, approvals: map[string]pending{}, questions: map[string]chan engine.Answers{}, askOverride: opts.Ask,
		sessionsDir: opts.SessionsDir, shell: opts.Shell, shells: map[string]context.CancelFunc{},
		// A resumed session that never ran has no first message yet.
		firstPromptPending: !opts.Resumed || opts.FirstPrompt == "",
	}
	s.out <- SessionOpened{At: time.Now(), ID: id, Resumed: opts.Resumed, Engine: eng.Name(), Settings: opts.Settings}
	if opts.Instructions != nil {
		loaded := *opts.Instructions
		loaded.At = time.Now()
		s.out <- loaded
	}
	if opts.SessionsDir != "" {
		s.hooks.transcriptPath = filepath.Join(opts.SessionsDir, id+".session.jsonl")
		if opts.Source != "" && !opts.Resumed {
			if err := writeSidecar(opts.SessionsDir, id, Sidecar{Source: opts.Source, Created: time.Now().UTC(), Parent: opts.Parent}); err != nil {
				s.out <- Notice{At: time.Now(), Level: LevelWarning, Message: err.Error()}
			}
		}
		s.saveSettings(opts.Settings)
		s.noteOpened(opts.FirstPrompt)
		s.restoreQueue(sc, scErr)
	}
	s.openGoal(opts.Goals, opts.Parent == "" && opts.Source != SourceSubagent)
	for _, n := range opts.Notices {
		s.out <- Notice{At: time.Now(), Level: LevelWarning, Message: n}
	}
	s.startHooks(opts.Resumed)
	if opts.Interactive {
		s.startMCP()
	}
	go s.loop()

	return s, nil
}

// ID is the runner session ID; resume the session with it.
func (s *Session) ID() string { return s.id }

// Priority reports whether the session's provider accepts priority
// processing, so /fast can turn it on (engine.Engine.Priority).
func (s *Session) Priority() bool { return s.priority }

// Yolo reports whether the session may run in yolo mode: it started with
// --yolo.
func (s *Session) Yolo() bool { return s.yolo }

// Events is the ordered stream of run and session events. It is closed after Close.
func (s *Session) Events() <-chan core.Event { return s.out }

// Submit sends a message: it starts a run when idle and queues it while a run
// is live. Queued messages go out together when the run ends.
func (s *Session) Submit(text string) (core.UserInput, error) {
	return s.Send(text, SendAfterRun)
}

// SteerNow sends a message to the agent now. With live input it reaches the
// running agent, which drops the model response under way for it; otherwise
// the run is interrupted and a new run starts with the queue and this message.
func (s *Session) SteerNow(text string) (core.UserInput, error) {
	return s.Send(text, SendNow)
}

// When is when a message sent while a run is live reaches the agent: now
// (SteerNow), after the next tool call (sendAfterTool), or after the run.
type When int

const (
	SendNow When = iota
	SendAfterTool
	SendAfterRun
)

// SteerQueued sends every queued message now, in order, as SteerNow sends
// one (ctrl+enter on an empty composer). It reports how many there were.
func (s *Session) SteerQueued() (int, error) {
	return call[int](s, cmdSteerQueued{})
}

// Interrupt stops the live run gracefully. Queued messages stay queued.
func (s *Session) Interrupt() error {
	_, err := call[struct{}](s, cmdInterrupt{})

	return err
}

// Withdraw takes a queued message back before it is sent. It reports whether
// the message was still queued.
func (s *Session) Withdraw(id string) (bool, error) {
	return call[bool](s, cmdWithdraw{id: id})
}

// SetSettings changes the settings and reports when they apply.
func (s *Session) SetSettings(settings Settings) (Applied, error) {
	if err := settings.Validate(); err != nil {
		return "", err
	}
	if settings.Mode.AsksNoOne() && !s.yolo {
		return "", errors.New("yolo mode needs --yolo when the session starts")
	}

	return call[Applied](s, cmdSettings{settings: settings})
}

// MCPServers reports the engine's MCP servers; ok is false when the engine
// does not run MCP servers.
func (s *Session) MCPServers() (servers []mcp.ServerStatus, ok bool) {
	l, ok := s.eng.(engine.MCPLister)
	if !ok {
		return nil, false
	}

	return l.MCPServers(), true
}

// MCP is the engine's MCP servers, for their prompts and resources; nil
// when the engine runs none.
func (s *Session) MCP() *mcp.Manager {
	if c, ok := s.eng.(engine.MCPClient); ok {
		return c.MCP()
	}

	return nil
}

// ContextUsage breaks down the context of the last model request; ok is
// false when the engine cannot or no request was sent yet.
func (s *Session) ContextUsage() (contextusage.Usage, bool) {
	r, ok := s.eng.(engine.ContextReporter)
	if !ok {
		return contextusage.Usage{}, false
	}

	return r.ContextUsage(s.id)
}

// Close interrupts a live run, waits for it to end, and closes Events. It
// then closes the engine when it holds resources between runs, such as MCP
// servers.
func (s *Session) Close() error {
	err := s.close()
	if f, ok := s.eng.(engine.Forgetter); ok {
		f.Forget(s.id)
	}
	if c, ok := s.eng.(io.Closer); ok {
		if cerr := c.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("failed to close the engine: %w", cerr)
		}
	}

	return err
}

func (s *Session) close() error {
	reply := make(chan error, 1)
	select {
	case s.in <- cmdClose{reply: reply}:
	case <-s.done:
		return nil
	}
	select {
	case err := <-reply:
		<-s.done

		return err
	case <-s.done:
		return nil
	}
}

// Send sends a message: it starts a run when idle, and while a run is live
// it reaches the agent when says.
func (s *Session) Send(text string, when When) (core.UserInput, error) {
	if strings.TrimSpace(text) == "" {
		return core.UserInput{}, errors.New("the message is empty")
	}

	return call[core.UserInput](s, cmdSubmit{text: text, when: when})
}
