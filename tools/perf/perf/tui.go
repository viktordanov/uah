package perf

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"

	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/internal/tui/bubble"
	"github.com/viktordanov/uah/internal/tui/term"
)

// The TUI runs as `uah` runs it, on term's loop and line renderer, on a 120x40 true-color terminal whose output is counted and
// dropped and whose input never comes. A probe around the model times
// every Update and View and watches the screen for text.

const (
	termWidth  = 120
	termHeight = 40
)

// probe wraps the TUI's model.
type probe struct {
	mu      sync.Mutex
	inner   term.Model
	updates int
	views   []time.Duration
	// screen is the last view's text; changed is signaled after each view.
	screen  string
	changed chan struct{}
	// opening is set by the update that takes the opened session and its
	// history; opened is when the view after it was made.
	opening bool
	opened  time.Time
}

// openedMsg is the type of the message that brings the TUI its session
// and the transcript; the probe matches it by name, since it is the
// TUI's own.
const openedMsg = "bubble.openedMsg"

func (p *probe) Init() term.Cmd { return p.inner.Init() }

func (p *probe) Update(msg term.Msg) (term.Model, term.Cmd) {
	next, cmd := p.inner.Update(msg)
	p.mu.Lock()
	p.inner = next
	p.updates++
	if p.opened.IsZero() && fmt.Sprintf("%T", msg) == openedMsg {
		p.opening = true
	}
	p.mu.Unlock()

	return p, cmd
}

func (p *probe) View() term.View {
	start := time.Now()
	v := p.inner.View()
	took := time.Since(start)
	screen := ansi.Strip(v.Content)
	p.mu.Lock()
	p.views = append(p.views, took)
	p.screen = screen
	if p.opening {
		p.opening, p.opened = false, start.Add(took)
	}
	p.mu.Unlock()
	select {
	case p.changed <- struct{}{}:
	default:
	}

	return v
}

// stats are the updates and views so far, and the views' durations.
func (p *probe) stats() (updates int, views []time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.updates, append([]time.Duration(nil), p.views...)
}

// waitScreen waits until match holds for the screen; it holds the
// probe's lock while it matches.
func (p *probe) waitScreen(what string, match func(string) bool) error {
	deadline := time.After(waitTimeout)
	for {
		p.mu.Lock()
		ok := match(p.screen)
		p.mu.Unlock()
		if ok {
			return nil
		}
		select {
		case <-p.changed:
		case <-time.After(50 * time.Millisecond):
		case <-deadline:
			return fmt.Errorf("timed out waiting for the screen to show %s", what)
		}
	}
}

// waitOpened waits for the first view of the opened session, and returns
// when it was made.
func (p *probe) waitOpened() (time.Time, error) {
	var opened time.Time
	err := p.waitScreen("the opened session", func(string) bool {
		opened = p.opened

		return !opened.IsZero()
	})

	return opened, err
}

// terminal counts what the renderer writes, and when.
type terminal struct {
	mu    sync.Mutex
	bytes int
	at    []time.Time
}

func (t *terminal) Write(b []byte) (int, error) {
	t.mu.Lock()
	t.bytes += len(b)
	t.at = append(t.at, time.Now())
	t.mu.Unlock()

	return len(b), nil
}

func (t *terminal) stats() (bytes, writes int) {
	t.mu.Lock()
	defer t.mu.Unlock()

	return t.bytes, len(t.at)
}

// firstWriteAfter is the first write at or after at.
func (t *terminal) firstWriteAfter(at time.Time) (time.Time, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, w := range t.at {
		if !w.Before(at) {
			return w, true
		}
	}

	return time.Time{}, false
}

// TUI is a running TUI.
type TUI struct {
	p      *term.Program
	cancel context.CancelFunc
	probe  *probe
	term   *terminal
	done   chan error
	// input is the program's input; closing it ends its reader.
	input *io.PipeWriter
	// sess is the session the TUI opened.
	mu   sync.Mutex
	sess *session.Session
}

// StartTUI starts the TUI on e, resuming id ("" for a new session).
func StartTUI(ctx context.Context, e *Env, id string) *TUI {
	t := &TUI{term: &terminal{}, done: make(chan error, 1)}
	deps := bubble.Deps{
		SessionID: id,
		Open: func(ctx context.Context, id string) (*session.Session, []session.LoadedRun, error) {
			s, _, err := e.Open(ctx, id, true)
			if err != nil {
				return nil, nil, err
			}
			t.mu.Lock()
			t.sess = s
			t.mu.Unlock()
			if id == "" {
				return s, nil, nil
			}
			history, err := session.Load(e.Home, id)
			if err != nil {
				_ = s.Close()

				return nil, nil, fmt.Errorf("failed to load the history: %w", err)
			}

			return s, history, nil
		},
		Sessions: func() ([]session.Info, error) { return session.Sessions(e.Home) },
		Version:  "perf",
	}
	t.probe = &probe{inner: bubble.New(ctx, deps), changed: make(chan struct{}, 1)}
	in, input := io.Pipe() // input that never comes
	t.input = input
	t.p = term.NewProgram(t.probe, term.Options{
		In: in, Out: t.term, Width: termWidth, Height: termHeight,
		Env: []string{"TERM=xterm-256color"}, Profile: colorprofile.TrueColor,
	})
	ctx, t.cancel = context.WithCancel(ctx)
	go func() {
		_, err := t.p.Run(ctx)
		t.done <- err
	}()

	return t
}

// Send sends a message to the program.
func (t *TUI) Send(msg term.Msg) { t.p.Send(msg) }

// Type types text and presses enter.
func (t *TUI) Type(text string) {
	for _, r := range text {
		t.p.Send(term.KeyPressMsg{Code: r, Text: string(r)})
	}
	t.p.Send(term.KeyPressMsg{Code: term.KeyEnter})
}

// Stop quits the program and closes its session.
func (t *TUI) Stop() error {
	t.p.Quit()
	var err error
	select {
	case err = <-t.done:
	case <-time.After(waitTimeout):
		t.cancel()
		err = errors.New("the TUI did not quit")
	}
	_ = t.input.Close()
	t.mu.Lock()
	s := t.sess
	t.mu.Unlock()
	if s != nil {
		err = errors.Join(err, s.Close())
	}
	if errors.Is(err, context.Canceled) {
		err = nil
	}

	return err
}

// shows reports whether the screen contains text.
func shows(text string) func(string) bool {
	return func(screen string) bool { return strings.Contains(screen, text) }
}

// idleScreen holds when no run is shown working: the footer offers no
// interrupt.
func idleScreen(screen string) bool { return !strings.Contains(screen, "esc to interrupt") }
