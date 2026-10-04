package term

import (
	"context"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/colorprofile"
)

// FrameRate is the most frames a second the screen is drawn. A change
// after a pause draws at once; changes that come faster are drawn
// together, one frame per 1/FrameRate.
const FrameRate = 30

// Options say where a program runs. The zero value runs on the process's
// terminal.
type Options struct {
	// In and Out are the terminal (default os.Stdin and os.Stdout).
	In  io.Reader
	Out io.Writer
	// Width and Height, when set, fix the screen's size and make the
	// program headless, for tests and tools/perf: no raw mode, no signals,
	// and no resizes.
	Width, Height int
	// Env is the environment that names the terminal: TERM, COLORTERM,
	// NO_COLOR (default os.Environ()).
	Env []string
	// Profile is the colour profile the output is downsampled to (default:
	// detected from Out and Env); styles below true colour are converted.
	Profile colorprofile.Profile

	// clock is the time source (default the real one); loop tests fake it.
	clock clock
}

// Program runs a model on a terminal: one goroutine owns the model, takes
// input, resizes, and command results in order, and draws a frame when
// something changed, at most FrameRate a second. While nothing happens it
// sleeps: no timer runs.
type Program struct {
	model Model
	opts  Options
	// msgs carries command results and Send's messages to the loop.
	msgs chan Msg
	// done is closed when Run returns, so commands still running drop
	// their results.
	done     chan struct{}
	doneOnce sync.Once
	ctx      context.Context

	term   *terminal
	screen *Screen
	clock  clock
	// dirty is set when an update ran since the last frame; last is when
	// the last frame was drawn.
	dirty bool
	last  time.Time
	quit  bool
	// What the terminal was last told.
	mouse bool
	title string
	// cursorStyle is the DECSCUSR set (0: the terminal's own), and
	// cursorColor the OSC 12 colour ("": the terminal's own).
	cursorStyle int
	cursorColor string
	// panicked is a command's panic, re-raised on the loop.
	panicked any
	// fatal ends the loop with an error: the terminal could not be taken
	// back after Exec, or frames keep failing to write.
	fatal error
	// writeFails counts the frames that failed to write in a row; resend
	// makes the next frame send the modes again after one did.
	writeFails int
	resend     bool
}

// NewProgram returns a program that runs model.
func NewProgram(model Model, opts Options) *Program {
	if opts.clock == nil {
		opts.clock = realClock{}
	}
	if opts.Env == nil {
		opts.Env = os.Environ()
	}
	if opts.In == nil {
		opts.In = os.Stdin
	}
	if opts.Out == nil {
		opts.Out = os.Stdout
	}

	return &Program{model: model, opts: opts, msgs: make(chan Msg, 256), done: make(chan struct{}), clock: opts.clock}
}

// Run runs model until it quits or ctx ends, and returns the model as it
// was last.
func Run(ctx context.Context, model Model, opts Options) (Model, error) {
	return NewProgram(model, opts).Run(ctx)
}

// Send gives the model a message, as a command's result would. It does
// nothing once the program ended.
func (p *Program) Send(msg Msg) {
	select {
	case p.msgs <- msg:
	case <-p.done:
	}
}

// Quit ends the program after one last frame.
func (p *Program) Quit() { p.Send(QuitMsg{}) }

// Run runs the program until the model quits, ctx ends, or the process is
// told to stop (SIGTERM, SIGHUP, SIGINT); each leaves the terminal as it
// found it, a panic too, which Run raises again after the terminal is
// restored.
func (p *Program) Run(ctx context.Context) (final Model, err error) {
	ctx, cancel := context.WithCancel(ctx)
	p.ctx = ctx
	defer cancel()
	defer p.doneOnce.Do(func() { close(p.done) })

	t, err := openTerminal(p.opts)
	if err != nil {
		return p.model, err
	}
	p.term = t
	defer func() {
		if r := recover(); r != nil {
			_ = t.leave(p.restore())
			panic(r)
		}
	}()
	if err := t.enter(); err != nil {
		_ = t.leave("")

		return p.model, err
	}
	w, h := t.size()
	p.screen = NewScreen(w, h)
	if err := t.startInput(ctx); err != nil {
		_ = t.leave("")

		return p.model, err
	}

	err = p.loop(w, h)
	if lerr := t.leave(p.restore()); err == nil {
		err = lerr
	}

	return p.model, err
}

// loop is the event loop: it waits for the next input, signal, command
// result, or the frame timer, takes whatever else is already waiting, and
// then draws or arms the timer.
func (p *Program) loop(w, h int) error {
	p.update(WindowSizeMsg{Width: w, Height: h})
	p.exec(p.model.Init())
	var (
		frame <-chan time.Time
		stop  func()
	)
	defer func() {
		if stop != nil {
			stop()
		}
	}()
	for !p.quit {
		// A frame that failed to write leaves dirty set, and is tried
		// again on the timer.
		for p.dirty && frame == nil {
			if wait := time.Second/FrameRate - p.clock.Since(p.last); wait > 0 {
				frame, stop = p.clock.Timer(wait)
			} else {
				p.draw()
			}
		}
		select {
		case <-p.ctx.Done():
			return nil
		case msg := <-p.term.input:
			p.handle(msg)
		case <-p.term.winch:
			p.resize()
		case sig := <-p.term.stop:
			if p.term.stops(sig) {
				return nil
			}
		case err := <-p.term.inputErr:
			return fmt.Errorf("failed to read the terminal: %w", err)
		case msg := <-p.msgs:
			p.handle(msg)
		case <-frame:
			frame, stop = nil, nil
			p.draw()
		}
		p.drain()
		if p.panicked != nil {
			panic(p.panicked)
		}
		if p.fatal != nil {
			return p.fatal
		}
	}
	p.draw()

	return nil
}

// drain takes the input and results already waiting, so a burst (a
// paste, a wheel spin, a batch of events) costs one frame.
func (p *Program) drain() {
	for range 256 {
		if p.quit {
			return
		}
		select {
		case msg := <-p.term.input:
			p.handle(msg)
		case msg := <-p.msgs:
			p.handle(msg)
		default:
			return
		}
	}
}

// handle runs the loop's own messages and gives the rest to the model.
func (p *Program) handle(msg Msg) {
	switch msg := msg.(type) {
	case nil:
	case QuitMsg:
		p.quit = true
	case BatchMsg:
		for _, c := range msg {
			p.exec(c)
		}
	case multiMsg:
		for _, m := range msg {
			p.handle(m)
		}
	case modeReportMsg:
		p.term.onModeReport(msg, p.screen)
	case setClipboardMsg:
		_ = p.term.write(oscClipboard(string(msg)))
	case execMsg:
		p.runExec(msg)
	case panicMsg:
		p.panicked = msg.String()
	default:
		p.update(msg)
	}
}

func (p *Program) update(msg Msg) {
	var cmd Cmd
	p.model, cmd = p.model.Update(msg)
	p.dirty = true
	p.exec(cmd)
}

// panicMsg is a command's panic and its stack, raised again on the loop so
// the terminal is restored before the program dies.
type panicMsg struct {
	value any
	stack []byte
}

func (m panicMsg) String() string {
	return fmt.Sprintf("%v [recovered in a command]\n\n%s", m.value, m.stack)
}

// exec runs a command in a goroutine of its own and sends its result to
// the loop.
func (p *Program) exec(cmd Cmd) {
	if cmd == nil {
		return
	}
	go func() {
		var msg Msg
		func() {
			defer func() {
				if r := recover(); r != nil {
					msg = panicMsg{value: r, stack: debug.Stack()}
				}
			}()
			msg = cmd()
		}()
		if msg == nil {
			return
		}
		select {
		case p.msgs <- msg:
		case <-p.done:
		}
	}()
}

func (p *Program) resize() {
	w, h, ok := p.term.newSize()
	if !ok {
		return
	}
	p.screen.Resize(w, h)
	p.update(WindowSizeMsg{Width: w, Height: h})
}

// runExec runs a program with the terminal released: the input stops, the
// terminal leaves the alt screen and its modes, and after the program it
// enters them again and the whole screen is drawn.
//
// The program's error goes to its callback; failing to release the
// terminal or take it back ends the loop, which then restores what it
// can.
func (p *Program) runExec(msg execMsg) {
	if err := p.term.release(p.restore()); err != nil {
		p.fatal = fmt.Errorf("failed to release the terminal: %w", err)

		return
	}
	// Keys typed before the program started are neither its nor, after
	// it, the TUI's.
	p.term.dropInput()
	msg.cmd.SetStdin(p.term.in)
	msg.cmd.SetStdout(p.term.out)
	msg.cmd.SetStderr(p.term.out)
	err := msg.cmd.Run()
	if rerr := p.term.reacquire(p.ctx); rerr != nil {
		p.fatal = fmt.Errorf("failed to take the terminal back: %w", rerr)

		return
	}
	// The terminal may have been resized while it was released, so its
	// size is read again; the SIGWINCH still queued then finds no change.
	p.resize()
	p.screen.Invalidate()
	p.mouse, p.title, p.cursorStyle, p.cursorColor = false, "", 0, ""
	if msg.fn != nil {
		p.update(msg.fn(err))
	} else {
		p.dirty = true
	}
}

// restore is what leaving the terminal resets beyond its modes: the
// title, and the cursor's style and colour, when the program set them.
func (p *Program) restore() string {
	var b strings.Builder
	if p.title != "" {
		b.WriteString(oscTitle(""))
	}
	if p.cursorStyle != 0 {
		b.WriteString("\x1b[0 q")
	}
	if p.cursorColor != "" {
		b.WriteString(cursorColor(""))
	}

	return b.String()
}

// draw builds the view and writes what changed: the terminal's modes, its
// title, and the rows.
func (p *Program) draw() {
	p.dirty = false
	p.last = p.clock.Now()
	v := p.model.View()
	var pre strings.Builder
	if p.resend {
		p.resend = false
		p.mouse, p.title, p.cursorStyle, p.cursorColor = !v.Mouse, "\x00", -1, "\x00"
	}
	if v.Mouse != p.mouse {
		p.mouse = v.Mouse
		pre.WriteString(mouseMode(v.Mouse))
	}
	if v.WindowTitle != p.title {
		p.title = v.WindowTitle
		pre.WriteString(oscTitle(v.WindowTitle))
	}
	if c := v.Cursor; c != nil {
		// The shape and colour stay while the cursor hides.
		if style := c.style(); style != p.cursorStyle {
			p.cursorStyle = style
			pre.WriteString("\x1b[" + strconv.Itoa(style) + " q")
		}
		if col := hexColor(c.Color); col != p.cursorColor {
			p.cursorColor = col
			pre.WriteString(cursorColor(col))
		}
	}
	frame := p.screen.Frame(strings.Split(v.Content, "\n"), v.Cursor)
	if pre.Len() == 0 && frame == nil {
		return
	}
	if err := p.term.writeFrame(pre.String(), frame); err != nil {
		// The terminal may show part of the frame: the next one draws it
		// all and sends the modes again; frames that keep failing end
		// the program.
		p.screen.Invalidate()
		p.resend, p.dirty = true, true
		if p.writeFails++; p.writeFails >= maxWriteFails {
			p.fatal = err
		}

		return
	}
	p.writeFails = 0
}

// maxWriteFails is how many frames in a row may fail to write before the
// program ends.
const maxWriteFails = 3

// clock is the loop's time: the frame budget and its one-shot timer.
type clock interface {
	Now() time.Time
	Since(t time.Time) time.Duration
	// Timer fires once after d; stop cancels it.
	Timer(d time.Duration) (<-chan time.Time, func())
}

type realClock struct{}

func (realClock) Now() time.Time                  { return time.Now() }
func (realClock) Since(t time.Time) time.Duration { return time.Since(t) }

func (realClock) Timer(d time.Duration) (<-chan time.Time, func()) {
	t := time.NewTimer(d)

	return t.C, func() { t.Stop() }
}
