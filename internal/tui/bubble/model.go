// Package bubble is the shell around the TUI state and renderer, on term:
// it turns keys into intents, runs effects against the session, batches
// session events, and draws frames. Everything else lives in state and render.
package bubble

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/compaction"
	"github.com/viktordanov/uah/internal/history"
	"github.com/viktordanov/uah/internal/images"
	"github.com/viktordanov/uah/internal/images/clipboard"
	"github.com/viktordanov/uah/internal/models"
	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/internal/tui/composer"
	"github.com/viktordanov/uah/internal/tui/render"
	"github.com/viktordanov/uah/internal/tui/state"
	"github.com/viktordanov/uah/internal/tui/term"
	"github.com/viktordanov/uah/internal/usage"
	"github.com/viktordanov/uah/internal/usage/cachestats"
)

const (
	batchWindow  = 16 * time.Millisecond
	tickInterval = 100 * time.Millisecond
)

// Deps are what the TUI needs from the command that starts it.
type Deps struct {
	// Open opens a session: "" starts a new one, otherwise it resumes that ID.
	// It returns the saved runs of a resumed session.
	Open func(ctx context.Context, id string) (*session.Session, []session.LoadedRun, error)
	// Sessions lists sessions for the picker.
	Sessions func() ([]session.Info, error)
	// Models lists the provider's models for /model (optional). It may call
	// the provider, so it runs off the update loop.
	Models func(ctx context.Context, provider string) models.Catalog
	// Activity counts recent runs per day for /status (optional).
	Activity func() (map[string]int, error)
	// SessionID is the session to open first ("" for a new one).
	SessionID string
	// Prompt, when set, is sent once the first session is open.
	Prompt string
	// Cwd scopes the picker to sessions of this directory, as Codex does.
	Cwd string
	// Picker opens the session picker first instead of a session.
	Picker bool
	// AllSessions starts the picker showing every directory.
	AllSessions bool
	// Details starts in the detailed view: turns, run dividers, and tokens.
	Details bool
	// Mouse reports the mouse, so the wheel scrolls and a drag selects
	// transcript text; off, the terminal selects text and its wheel sends
	// ↑ and ↓.
	Mouse bool
	// Title shows idle, working, or waiting for an approval in the
	// terminal's title; off, uah leaves the title alone.
	Title bool
	// CopyText writes text to the system clipboard with its own tool, next
	// to OSC 52 (optional; internal/images/clipboard.WriteText).
	CopyText func(ctx context.Context, text string) error
	// Config loads the effective configuration for /config: the user file
	// and each key's value and source (optional). SaveConfig writes one key
	// to the user file, keeping its comments; a nil value removes it.
	Config     func(ctx context.Context) state.ConfigLoaded
	SaveConfig func(key string, value any) error
	// Version is uah's version, for the banner.
	Version string
	// Windows finds a model's context window in the model catalog, for the
	// footer's context meter (nil: the default window).
	Windows compaction.WindowLookup
	// Usage reads the subscription's usage for /status, the footer, and the
	// warnings (nil: none, as for a provider without usage).
	Usage usage.Reader
	// Cache reads a session's prompt cache accounting from its recorded
	// runs, for /usage and /status (optional; session.CacheStats).
	Cache func(id string) ([]cachestats.Attributed, error)
	// Now is the clock (default time.Now).
	Now func() time.Time
	// Images stores images pasted into the composer, and Clipboard reads
	// the clipboard's image for ctrl+v (optional; without them pasting an
	// image says it cannot).
	Images    *images.Store
	Clipboard clipboard.Reader
	// PasteText reads the clipboard's text, which ctrl+v pastes when the
	// clipboard holds no image (optional; clipboard.TextReader.ReadText).
	PasteText func(ctx context.Context) (string, error)
	// WritableRoots are the sandbox's extra writable roots, absolute; ctrl+g
	// checks that sandboxed commands cannot write its draft file.
	WritableRoots []string
	// Exec runs the editor for ctrl+g with the terminal released (default
	// term.Exec); tests run it directly.
	Exec func(term.ExecCommand, term.ExecCallback) term.Cmd
	// History is the prompt history file for ↑ and ctrl+r (nil: this
	// process's prompts only).
	History *history.File
	// FileLinks is what a click on a file path does, [tui] file_links:
	// "peek", "editor", "open", or "off" ("" too: no links).
	FileLinks string
	// Launch runs a program of its own for a file link, the system's
	// opener or a windowed editor, and waits for it (default: in a process
	// group of its own); tests catch the command.
	Launch func(ctx context.Context, args []string) error
}

// Model is the TUI's term.Model.
type Model struct {
	ctx   context.Context
	deps  Deps
	st    state.State
	cache *render.Cache
	// theme is the cache's theme, for what uah prints after the TUI.
	theme    render.Theme
	composer composer.Composer
	w, h     int

	sess     *session.Session
	gen      int // increases with each session; stale events are dropped
	ticking  bool
	prompted bool
	// held are effects that need a session, made before the first one opened.
	held []state.Effect
	// watch follows the agent the view shows; watchGen drops its events
	// once it ends.
	watch    *session.AgentWatch
	watchGen int
	// prompts appends to the history file in order (nil without one).
	prompts *history.Recorder
	// calls keeps the session calls in the order Update made them.
	calls *calls
	// pointer is where the mouse is while a drag selects text, and
	// edgeTicking says the drag's edge scroll waits for its tick (mouse.go).
	pointer     struct{ x, y int }
	edgeTicking bool
}

// Messages from goroutines and commands.
type (
	eventsMsg struct {
		gen     int
		events  []core.Event
		batches <-chan []core.Event
	}
	sessionClosedMsg struct{ gen int }
	openedMsg        struct {
		sess    *session.Session
		history []session.LoadedRun
	}
	withdrawnMsg struct{ text string }
	quitMsg      struct{}
	tickMsg      time.Time
)

// New returns the model; attach it to a program with Run.
func New(ctx context.Context, deps Deps) Model {
	if deps.Now == nil {
		deps.Now = time.Now
	}

	st := state.New(deps.Now())
	st.Details, st.Mouse, st.Title, st.Windows = deps.Details, deps.Mouse, deps.Title, deps.Windows
	st.Home, _ = os.UserHomeDir()
	st.Host, _ = os.Hostname()
	st.FileLinks = deps.FileLinks

	m := Model{
		ctx: ctx, deps: deps, st: st,
		cache: render.NewCache(render.Amber), theme: render.Amber, composer: newComposer(render.NewStyles(render.Amber)),
		calls: &calls{},
	}
	if deps.History != nil {
		m.prompts = history.NewRecorder(*deps.History)
	}

	return m
}

// onBackground picks the theme for the terminal's background.
func (m Model) onBackground(msg term.BackgroundColorMsg) Model {
	m.theme = render.ThemeFor(msg.Color)
	m.cache = render.NewCache(m.theme)
	m.composer.SetStyles(composerStyles(m.cache.Styles()))

	return m
}

// Run runs the TUI on the terminal and blocks until it exits.
func Run(ctx context.Context, deps Deps) (Exit, error) {
	final, err := term.Run(ctx, New(ctx, deps), term.Options{})
	fm, ok := final.(Model)
	if !ok {
		return Exit{}, err // the caller wraps it
	}
	if fm.sess != nil {
		_ = fm.sess.Close() // the program ended without /quit, for example on SIGTERM
	}

	return fm.Exit(), err // the caller wraps it
}

func (m Model) Init() term.Cmd {
	// The theme follows the terminal's background once it answers.
	if m.deps.Picker {
		return term.Batch(m.run(state.EffLoadPrompts{}), m.run(state.EffLoadSessions{}))
	}

	return term.Batch(m.run(state.EffLoadPrompts{}), m.open(m.deps.SessionID))
}

// onTerminalReport takes the terminal's answers to term's startup
// queries: its background color, and whether it tells shift+enter from
// enter, which picks the new-line hint (no answer at all, as from tmux:
// ctrl+j).
func (m Model) onTerminalReport(msg term.Msg) (term.Model, term.Cmd) {
	switch msg := msg.(type) {
	case term.BackgroundColorMsg:
		return m.onBackground(msg), nil
	case term.KeyboardEnhancementsMsg:
		return m.dispatch(state.KeyboardReported{Disambiguates: msg.SupportsKeyDisambiguation()})
	}

	return m, nil
}

func (m Model) Update(msg term.Msg) (term.Model, term.Cmd) {
	next, cmd := m.update(msg)
	if nm, ok := next.(Model); ok {
		next = nm.anchor()
	}

	return next, cmd
}

func (m Model) update(msg term.Msg) (term.Model, term.Cmd) {
	switch msg := msg.(type) {
	case term.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.resizeComposer()

		return m, nil
	case term.BackgroundColorMsg, term.KeyboardEnhancementsMsg:
		return m.onTerminalReport(msg)
	case term.MouseWheelMsg, term.MouseClickMsg, term.MouseMotionMsg, term.MouseReleaseMsg, edgeMsg:
		return m.onPointer(msg)
	case term.KeyPressMsg:
		return m.now().onKey(msg)
	case term.PasteMsg:
		return m.now().onPaste(msg)
	case eventsMsg:
		if msg.gen != m.gen {
			return m, next(msg.gen, msg.batches) // drain a closed session's last events
		}
		cmds := []term.Cmd{next(m.gen, msg.batches)}
		for _, e := range msg.events {
			var effects []state.Effect
			m.st, effects = state.Reduce(m.st, e)
			for _, eff := range effects { // such as reading the usage after a run
				cmds = append(cmds, m.run(eff))
			}
		}
		// The agent's questions change the composer's hint.
		m.composer.Placeholder = render.ShellPlaceholder(m.st)
		// After the events: a batch that starts a run starts the clock.
		cmds = append(cmds, m.afterChange())

		return m, term.Batch(cmds...)
	case sessionClosedMsg:
		if msg.gen == m.gen {
			m.sess = nil
		}

		return m, nil
	case openedMsg:
		return m.onOpened(msg)
	case agentOpenedMsg, agentEventsMsg, agentWatchEndedMsg:
		return m.onAgentMsg(msg)
	case withdrawnMsg:
		return m.dispatch(state.DraftRestored{Text: msg.text})
	case tickMsg:
		m = m.now()
		m.ticking = false

		return m, m.afterChange()
	case quitMsg:
		m.sess = nil

		return m, term.Quit
	case state.Failed, state.SessionsLoaded, state.ActivityLoaded, state.FilesLoaded, state.MCPListed, state.ContextShown,
		state.ModelsLoaded, state.ConfigLoaded, state.ConfigSaved, state.ImageAttached, state.ImageFailed, state.DraftEdited:
		return m.dispatch(msg)
	case state.UsageLoaded, state.CacheLoaded, state.Copied, state.DiffShown, state.ReviewTargetsLoaded, state.PromptsLoaded:
		return m.dispatch(msg)
	case state.LinksResolved, state.PeekLoaded, state.FileOpened, state.LinkTimer:
		return m.now().dispatch(msg) // a toast starts now, not at the last tick
	}

	return m, nil
}

func (m Model) onOpened(msg openedMsg) (term.Model, term.Cmd) {
	m.gen++
	m.stopWatch()
	m.sess = msg.sess
	m.st.Priority = msg.sess.Priority()
	m.st.Yolo = msg.sess.Yolo()
	var cmds []term.Cmd
	if len(msg.history) > 0 {
		var effects []state.Effect
		m.st, effects = state.Reduce(m.st, state.HistoryLoaded{SessionID: msg.sess.ID(), Runs: msg.history})
		for _, e := range effects {
			if _, ok := e.(state.EffResolveLinks); ok { // the loaded messages' file links
				cmds = append(cmds, m.run(e))
			}
		}
	}
	batches := make(chan []core.Event)
	go batch(msg.sess.Events(), batches)
	cmds = append(cmds, next(m.gen, batches))
	for _, e := range m.held {
		cmds = append(cmds, m.run(e))
	}
	m.held = nil
	if !m.prompted && m.deps.Prompt != "" {
		m.prompted = true
		updated, cmd := m.dispatch(state.Submit{Text: m.deps.Prompt})

		return updated, term.Batch(append(cmds, cmd)...)
	}

	return m, term.Batch(cmds...)
}

// dispatch reduces an intent and runs the effects it returns.
func (m Model) dispatch(intent any) (term.Model, term.Cmd) {
	var effects []state.Effect
	shell := m.st.Shell
	m.st, effects = state.Reduce(m.st, intent)
	m.syncShell(shell)
	cmds := []term.Cmd{m.afterChange()}
	for _, e := range effects {
		if d, ok := e.(state.EffSetDraft); ok {
			m.composer.SetValue(d.Text)
			m.composer.CursorEnd()

			continue
		}
		if ins, ok := e.(state.EffInsertText); ok {
			m.composer.InsertString(ins.Text)

			continue
		}
		if _, ok := e.(state.EffCloseAgentView); ok {
			m.stopWatch()

			continue
		}
		if m.sess == nil && needsSession(e) {
			m.held = append(m.held, e) // sent once the session opens

			continue
		}
		cmds = append(cmds, m.run(e))
	}

	return m, term.Batch(cmds...)
}

// now sets the state's clock to deps.Now. The clock ticks only while
// something moves on screen, so a key sets it first: a first esc after the
// session sat idle is timed from the key, not from the last tick.
func (m Model) now() Model {
	m.st, _ = state.Reduce(m.st, state.Tick{Now: m.deps.Now()})

	return m
}

// afterChange keeps the clock ticking while anything moves on screen.
func (m *Model) afterChange() term.Cmd {
	moving := m.st.Busy || m.st.Live != nil || m.st.Status != "" || m.st.Toast != nil || m.st.AgentsRunning() || m.st.ShellRunning() || m.st.ReviewRunning()
	if v := m.st.View; v != nil {
		moving = moving || v.St.Busy || v.St.Live != nil // the viewed agent's spinner
	}
	if m.ticking || !moving {
		return nil
	}
	m.ticking = true

	return term.Tick(tickInterval, func(t time.Time) term.Msg { return tickMsg(t) })
}

// frame is what render.Screen draws around the state.
func (m Model) frame() render.Frame {
	return render.Frame{
		Width: m.w, Height: m.h, Composer: m.composer.View(), ComposerHeight: m.composer.Height(), Draft: m.composer.Value(), Version: m.deps.Version,
	}
}

func (m Model) View() term.View {
	content, composerRow := render.Screen(m.st, m.cache, m.frame())
	v := term.NewView(content)
	// With the mouse reported, which is the default, wheel events scroll
	// the transcript and a drag selects its text (mouse.go); the terminal's
	// own selection needs its modifier (Option in iTerm2 and Terminal,
	// Shift in most others). Without it, the terminal selects text and
	// turns the wheel into ↑ and ↓ (keys.go).
	v.Mouse = m.st.Mouse
	// term clears the title when the program ends.
	v.WindowTitle = m.st.WindowTitle()
	if c := m.composerCursor(); c != nil && composerRow >= 0 && m.st.Peek == nil {
		c.Y += composerRow
		v.Cursor = c
		m.searchCursor(c, composerRow)
	}

	return v
}

func trimmed(s string) string { return strings.TrimSpace(s) }

// needsSession reports whether an effect talks to the open session.
func needsSession(e state.Effect) bool {
	switch e.(type) {
	case state.EffSubmit, state.EffSteer, state.EffSteerQueued, state.EffSetSettings, state.EffShell:
		return true
	}

	return false
}
