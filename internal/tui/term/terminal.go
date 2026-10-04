package term

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image/color"
	"io"
	"os"
	"os/signal"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
	xterm "github.com/charmbracelet/x/term"
)

// The terminal's modes while the TUI runs, set in this order on enter and
// reset in the reverse order on leave.
const (
	altScreenOn  = "\x1b[?1049h" // the alt screen, saving the cursor
	altScreenOff = "\x1b[?1049l"
	cursorHide   = "\x1b[?25l"
	cursorShow   = "\x1b[?25h"
	autowrapOff  = "\x1b[?7l" // a line past the edge is cut, not wrapped
	autowrapOn   = "\x1b[?7h"
	pasteOn      = "\x1b[?2004h" // bracketed paste
	pasteOff     = "\x1b[?2004l"
	// modifyOtherKeys 2 and the kitty keyboard protocol's first flag tell
	// shift+enter and ctrl+enter from enter, where the terminal can.
	keysOn  = "\x1b[>4;2m\x1b[>1u"
	keysOff = "\x1b[<u\x1b[>4m"
	// mouseOn reports presses, releases, drags, and the wheel (1002) in
	// SGR coordinates (1006).
	mouseOn  = "\x1b[?1002h\x1b[?1006h"
	mouseOff = "\x1b[?1002l\x1b[?1003l\x1b[?1006l"
	// Queries: the kitty keyboard flags, the background colour (OSC 11),
	// and synchronized output (DECRQM 2026).
	queryKeys       = "\x1b[?u"
	queryBackground = "\x1b]11;?\x07"
	querySync       = "\x1b[?2026$p"
	// resetFrame ends any style and scroll region a frame left behind.
	resetFrame = "\x1b[m\x1b[r"
)

func mouseMode(on bool) string {
	if on {
		return mouseOn
	}

	return mouseOff
}

// oscTitle sets the window title; control characters are dropped, so a
// title cannot end the sequence early.
func oscTitle(title string) string {
	return "\x1b]2;" + strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return -1
		}

		return r
	}, title) + "\x07"
}

// cursorColor sets the cursor's colour (OSC 12), or resets it for "".
func cursorColor(hex string) string {
	if hex == "" {
		return "\x1b]112\x07"
	}

	return "\x1b]12;" + hex + "\x07"
}

// hexColor is c as #rrggbb, or "" for nil.
func hexColor(c color.Color) string {
	if c == nil {
		return ""
	}
	r, g, b, _ := c.RGBA()

	return fmt.Sprintf("#%02x%02x%02x", r>>8, g>>8, b>>8)
}

// oscClipboard copies text to the system clipboard (OSC 52).
func oscClipboard(text string) string {
	return "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(text)) + "\x07"
}

var errNotTerminal = errors.New("not a terminal")

// terminal is the terminal a program runs on: its modes, its input, and
// the signals that resize or stop it.
type terminal struct {
	in  io.Reader
	out io.Writer
	// inFd and outFd are the terminal's descriptors; tty is false when
	// headless.
	inFd, outFd uintptr
	tty         bool
	w, h        int
	env         []string
	profile     colorprofile.Profile
	saved       *xterm.State

	input chan Msg
	// eofEnds makes the input's end an error, as on a terminal; tests
	// set it headless.
	eofEnds bool
	// orig is the terminal's mode before the program first entered it.
	orig  *xterm.State
	winch chan os.Signal
	stop  chan os.Signal
	// reader is the running input reader; readDone is closed when it ends.
	reader   cancelReader
	readDone chan struct{}
	// readStop is closed to stop the reader's handoff of events.
	readStop chan struct{}
	stopping atomic.Bool
	// syncAsked is set when DECRQM 2026 was sent; only its answer turns
	// synchronized output on.
	syncAsked bool
	entered   bool
	// released is when the terminal was last taken back after Exec.
	released time.Time
}

// fder is a file with a descriptor, such as *os.File.
type fder interface{ Fd() uintptr }

func openTerminal(o Options) (*terminal, error) {
	t := &terminal{
		in: o.In, out: o.Out, env: o.Env, w: o.Width, h: o.Height, profile: o.Profile,
		input: make(chan Msg, 64), eofEnds: o.endOnEOF,
	}
	if t.profile == colorprofile.Unknown {
		t.profile = colorprofile.Detect(o.Out, o.Env)
	}
	// Below ASCII (TERM=dumb or unset, or output that is not a
	// terminal) colorprofile drops every escape sequence, cursor moves
	// too; the TUI keeps them and drops only the colours.
	t.profile = max(t.profile, colorprofile.ASCII)
	if o.Width > 0 && o.Height > 0 {
		return t, nil // headless
	}
	in, inOK := o.In.(fder)
	out, outOK := o.Out.(fder)
	if !inOK || !outOK || !xterm.IsTerminal(in.Fd()) || !xterm.IsTerminal(out.Fd()) {
		return nil, errNotTerminal
	}
	t.tty, t.inFd, t.outFd, t.eofEnds = true, in.Fd(), out.Fd(), true
	w, h, err := xterm.GetSize(t.outFd)
	if err != nil {
		return nil, fmt.Errorf("failed to read the terminal's size: %w", err)
	}
	t.w, t.h = w, h

	return t, nil
}

func (t *terminal) size() (w, h int) { return t.w, t.h }

// newSize reads the size after SIGWINCH; ok is false when it did not
// change.
func (t *terminal) newSize() (w, h int, ok bool) {
	if !t.tty {
		return t.w, t.h, false
	}
	w, h, err := xterm.GetSize(t.outFd)
	if err != nil || w <= 0 || h <= 0 || (w == t.w && h == t.h) {
		return t.w, t.h, false
	}
	t.w, t.h = w, h

	return w, h, true
}

// enter puts the terminal in raw mode and the alt screen, turns on the
// input modes, and asks what it supports. The signals that stop the
// program are caught from here on, so leave always runs.
func (t *terminal) enter() error {
	if t.tty {
		saved, err := xterm.MakeRaw(t.inFd)
		if err != nil {
			return fmt.Errorf("failed to put the terminal in raw mode: %w", err)
		}
		t.saved = saved
		if t.orig == nil {
			t.orig = saved
		}
		if t.winch == nil {
			t.winch, t.stop = make(chan os.Signal, 1), make(chan os.Signal, 4)
			signal.Notify(t.winch, syscall.SIGWINCH)
			signal.Notify(t.stop, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGINT)
		}
	}
	t.entered = true
	seq := altScreenOn + cursorHide + autowrapOff + pasteOn + keysOn + queryKeys + queryBackground
	if !t.syncAsked && querySyncFor(t.env) {
		seq += querySync
		t.syncAsked = true
	}

	return t.write(seq)
}

// leave resets what enter set, in the reverse order, and restore (what the
// program set: the title, the cursor's style), and stops catching signals.
func (t *terminal) leave(restore string) error {
	err := t.release(restore)
	if t.winch != nil {
		signal.Stop(t.winch)
		signal.Stop(t.stop)
	}

	return err
}

// release leaves the terminal to another program (ctrl+g's editor); the
// signals stay caught.
func (t *terminal) release(restore string) error {
	t.stopInput()
	if !t.entered {
		return nil
	}
	t.entered = false
	err := t.write(resetFrame + mouseOff + restore + keysOff + pasteOff + autowrapOn + cursorShow + altScreenOff)
	if t.saved != nil {
		if rerr := xterm.Restore(t.inFd, t.saved); rerr != nil && err == nil {
			err = fmt.Errorf("failed to restore the terminal: %w", rerr)
		}
		t.saved = nil
	}

	return err
}

// reset puts the terminal back as the program found it after a program
// on the released terminal was stopped, which may have left its own
// modes: the first mode, the main screen, the cursor shown, autowrap on,
// and no mouse, bracketed paste, or keyboard enhancements.
func (t *terminal) reset() {
	// The modes the program may have turned on are turned off; the kitty
	// flags are set to none rather than popped, as its push is unknown.
	_ = t.write(resetFrame + mouseOff + pasteOff + "\x1b[=0;1u\x1b[>4m" + autowrapOn + cursorShow + altScreenOff)
	if t.orig != nil {
		_ = xterm.Restore(t.inFd, t.orig)
	}
}

// reacquire takes the terminal back after release. It leaves the size
// alone: the caller reads it with newSize, so a resize while the terminal
// was released reaches the screen and the model.
func (t *terminal) reacquire(ctx context.Context) error {
	if err := t.enter(); err != nil {
		return err
	}
	t.released = time.Now()

	return t.startInput(ctx)
}

// interruptGrace is how long after the terminal was taken back a SIGINT is
// still the released program's: ctrl+c in its cooked mode sends SIGINT to
// this process too, and it can arrive after the program ended.
const interruptGrace = time.Second

// stops reports whether sig stops the program. In raw mode ctrl+c is a
// key, so a SIGINT comes from ctrl+c in a program on the released
// terminal (dropped, during it and for interruptGrace after) or from kill.
func (t *terminal) stops(sig os.Signal) bool {
	return sig != syscall.SIGINT || (t.entered && time.Since(t.released) >= interruptGrace)
}

// dropInput drops the input decoded but not yet taken, such as keys typed
// before ctrl+g's editor opened, which belong to neither it nor the TUI
// after it.
func (t *terminal) dropInput() {
	for {
		select {
		case <-t.input:
		default:
			return
		}
	}
}

// inputEndMsg is the input's end or failure, after the keys read before
// it; it ends the program.
type inputEndMsg struct{ err error }

// errTerminalGone is the terminal's input ending while the TUI runs, as
// when the terminal closed without a SIGHUP.
var errTerminalGone = errors.New("the terminal's input ended")

// startInput starts reading and decoding the terminal's input.
func (t *terminal) startInput(ctx context.Context) error {
	r, err := newCancelReader(t.in)
	if err != nil {
		return fmt.Errorf("failed to read the terminal: %w", err)
	}
	t.reader, t.readDone, t.readStop = r, make(chan struct{}), make(chan struct{})
	t.stopping.Store(false)
	termType := lookupEnv(t.env, "TERM")
	go func(done, stop chan struct{}) {
		defer close(done)
		err := readInput(ctx, r, termType, t.input, stop)
		if t.stopping.Load() {
			return
		}
		switch {
		case err == nil || errors.Is(err, io.EOF):
			if !t.eofEnds {
				return // a headless program's input may end
			}
			err = errTerminalGone
		case errors.Is(err, context.Canceled):
			return
		}
		// On the queue, so the keys read before it are handled first.
		select {
		case t.input <- inputEndMsg{err: err}:
		case <-stop:
		}
	}(t.readDone, t.readStop)

	return nil
}

// stopInput cancels the input reader and waits for it to end, so the next
// program on the terminal gets every key.
func (t *terminal) stopInput() {
	if t.reader == nil {
		return
	}
	t.stopping.Store(true)
	close(t.readStop)
	if t.reader.Cancel() {
		<-t.readDone
	}
	_ = t.reader.Close()
	t.reader = nil
}

// onModeReport turns synchronized output on when the terminal says it
// supports it.
func (t *terminal) onModeReport(m modeReportMsg, s *Screen) {
	if m.mode != ansi.ModeSynchronizedOutput || !t.syncAsked {
		return
	}
	switch m.value {
	case ansi.ModeSet, ansi.ModeReset, ansi.ModePermanentlySet:
		s.SetSync(true)
	default:
		s.SetSync(false)
	}
}

// writeFrame writes a frame in one write, its colours downsampled below
// true colour.
func (t *terminal) writeFrame(pre string, frame []byte) error {
	if pre != "" {
		frame = append([]byte(pre), frame...)
	}
	var err error
	if t.profile < colorprofile.TrueColor {
		_, err = (&colorprofile.Writer{Forward: t.out, Profile: t.profile}).Write(frame)
	} else {
		_, err = t.out.Write(frame)
	}
	if err != nil {
		return fmt.Errorf("failed to write to the terminal: %w", err)
	}

	return nil
}

func (t *terminal) write(s string) error {
	if _, err := io.WriteString(t.out, s); err != nil {
		return fmt.Errorf("failed to write to the terminal: %w", err)
	}

	return nil
}

// querySyncFor reports whether to ask the terminal about mode 2026, as
// Bubble Tea did: not Apple's Terminal, which does not answer, and not over
// ssh, where the answer can arrive after the program ended, unless the
// terminal's name says it supports the mode.
func querySyncFor(env []string) bool {
	termType := lookupEnv(env, "TERM")
	for _, name := range []string{"ghostty", "wezterm", "alacritty", "kitty", "rio"} {
		if strings.Contains(termType, name) {
			return true
		}
	}
	if hasEnv(env, "SSH_TTY") {
		return false
	}
	prog, ok := lookup(env, "TERM_PROGRAM")

	return !ok || !strings.Contains(prog, "Apple")
}

func lookupEnv(env []string, key string) string {
	v, _ := lookup(env, key)

	return v
}

func hasEnv(env []string, key string) bool {
	_, ok := lookup(env, key)

	return ok
}

func lookup(env []string, key string) (string, bool) {
	for _, kv := range slices.Backward(env) {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			return v, true
		}
	}

	return "", false
}
