package term

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeClock is the loop's clock, moved by hand. It counts the timers armed
// and pending, so a test can tell the loop sleeps without one.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
	armed  int
}

type fakeTimer struct {
	at time.Time
	c  chan time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{now: time.Unix(1_000_000, 0)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

func (c *fakeClock) Since(t time.Time) time.Duration { return c.Now().Sub(t) }

func (c *fakeClock) Timer(d time.Duration) (<-chan time.Time, func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{at: c.now.Add(d), c: make(chan time.Time, 1)}
	c.timers = append(c.timers, t)
	c.armed++

	return t.c, func() { c.drop(t) }
}

func (c *fakeClock) drop(t *fakeTimer) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, x := range c.timers {
		if x == t {
			c.timers = append(c.timers[:i], c.timers[i+1:]...)

			return
		}
	}
}

// pending is the number of timers that have not fired.
func (c *fakeClock) pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return len(c.timers)
}

// advance moves the clock and fires the timers due.
func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	kept := c.timers[:0]
	for _, t := range c.timers {
		if t.at.After(c.now) {
			kept = append(kept, t)

			continue
		}
		t.c <- c.now
	}
	c.timers = kept
}

// counter is a model that shows how many messages of type countMsg it got,
// and logs every message and view.
type counter struct {
	n     int
	mouse bool
	title string
	log   *events
	init  Cmd
}

type countMsg struct{}

// sameMsg changes nothing on screen.
type sameMsg struct{}

type (
	mouseMsg string
	titleMsg string
)

func (m counter) Init() Cmd { return m.init }

func (m counter) Update(msg Msg) (Model, Cmd) {
	m.log.add(fmt.Sprintf("update %T", msg))
	switch msg := msg.(type) {
	case countMsg:
		m.n++
	case mouseMsg:
		m.mouse = msg == "on"
	case titleMsg:
		m.title = string(msg)
	case cmdMsg:
		return m, msg.cmd
	}

	return m, nil
}

func (m counter) View() View {
	m.log.view()
	v := NewView(fmt.Sprintf("count %d\nsecond row", m.n))
	v.Cursor = NewCursor(2, 1)
	v.Mouse, v.WindowTitle = m.mouse, m.title

	return v
}

// cmdMsg makes the model return cmd.
type cmdMsg struct{ cmd Cmd }

type events struct {
	mu      sync.Mutex
	list    []string
	views   int
	changed chan struct{}
}

func newEvents() *events { return &events{changed: make(chan struct{}, 1)} }

func (e *events) add(s string) {
	e.mu.Lock()
	e.list = append(e.list, s)
	e.mu.Unlock()
	e.signal()
}

func (e *events) view() {
	e.mu.Lock()
	e.views++
	e.mu.Unlock()
	e.signal()
}

func (e *events) signal() {
	select {
	case e.changed <- struct{}{}:
	default:
	}
}

func (e *events) viewCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()

	return e.views
}

func (e *events) all() []string {
	e.mu.Lock()
	defer e.mu.Unlock()

	return append([]string(nil), e.list...)
}

// output is the terminal's output, safe to read while the loop writes.
type output struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	writes int
}

func (o *output) Write(b []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.writes++

	return o.buf.Write(b)
}

func (o *output) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()

	return o.buf.String()
}

func (o *output) count() int {
	o.mu.Lock()
	defer o.mu.Unlock()

	return o.writes
}

// harness runs a model headless on a fake clock.
type harness struct {
	t     *testing.T
	p     *Program
	clock *fakeClock
	log   *events
	out   *output
	in    *io.PipeWriter
	done  chan error
	final Model
}

func startHarness(t *testing.T, m counter, before ...Msg) *harness {
	t.Helper()
	h := &harness{t: t, clock: newFakeClock(), log: newEvents(), out: &output{}, done: make(chan error, 1)}
	m.log = h.log
	inR, inW := io.Pipe()
	h.in = inW
	h.p = NewProgram(m, Options{In: inR, Out: h.out, Width: 20, Height: 4, Env: []string{"TERM=xterm-256color"},
		Profile: colorprofile.TrueColor, clock: h.clock})
	for _, msg := range before {
		h.p.Send(msg)
	}
	go func() {
		final, err := h.p.Run(context.Background())
		h.final = final
		h.done <- err
	}()
	t.Cleanup(func() {
		h.p.Quit()
		<-h.p.done
		_ = inW.Close()
	})

	return h
}

// waitFor waits until check holds, with the loop's work done.
func (h *harness) waitFor(what string, check func() bool) {
	h.t.Helper()
	deadline := time.After(5 * time.Second)
	for !check() {
		select {
		case <-h.log.changed:
		case <-time.After(time.Millisecond):
		case <-deadline:
			h.t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// settle waits until the loop is asleep: no message waiting and nothing
// changing for a while.
func (h *harness) settle() {
	h.t.Helper()
	for {
		before, writes := len(h.log.all()), h.out.count()
		time.Sleep(20 * time.Millisecond)
		if len(h.log.all()) == before && h.out.count() == writes && len(h.p.msgs) == 0 {
			return
		}
	}
}

// row is row y of the screen the output draws, in an emulator.
func (h *harness) row(y int) string {
	em := vt.NewEmulator(20, 4)
	// The emulator answers queries on a pipe no one reads, so they go.
	out := strings.NewReplacer(queryKeys, "", queryBackground, "", querySync, "").Replace(h.out.String())
	_, _ = em.Write([]byte(out))

	return strings.TrimRight(strings.Split(em.String(), "\n")[y], " ")
}

func (h *harness) stop() error {
	h.t.Helper()
	h.p.Quit()
	select {
	case err := <-h.done:
		return err
	case <-time.After(5 * time.Second):
		h.t.Fatal("the program did not quit")
	}

	return nil
}

func TestLoopDrawsTheFirstFrameAtOnce(t *testing.T) {
	h := startHarness(t, counter{})
	h.waitFor("the first frame", func() bool { return strings.Contains(h.out.String(), "count 0") })
	assert.Equal(t, 1, h.log.viewCount())
	out := h.out.String()
	assert.True(t, strings.HasPrefix(out, altScreenOn+cursorHide+autowrapOff+pasteOn+keysOn), "enter: %q", out)
	assert.Contains(t, out, queryKeys+queryBackground)
	require.NoError(t, h.stop())
}

func TestLoopSleepsWithoutATimerWhenIdle(t *testing.T) {
	h := startHarness(t, counter{})
	h.waitFor("the first frame", func() bool { return h.log.viewCount() == 1 })
	h.settle()
	assert.Equal(t, 0, h.clock.pending(), "no timer while idle")
	assert.Equal(t, 0, h.clock.armed)
	h.clock.advance(time.Hour)
	h.settle()
	assert.Equal(t, 1, h.log.viewCount(), "no frame without a change")
	require.NoError(t, h.stop())
}

func TestLoopWritesNothingForAnUnchangedView(t *testing.T) {
	h := startHarness(t, counter{})
	h.waitFor("the first frame", func() bool { return h.log.viewCount() == 1 })
	h.settle()
	writes := h.out.count()
	h.clock.advance(time.Second)
	h.p.Send(sameMsg{})
	h.waitFor("the view", func() bool { return h.log.viewCount() == 2 })
	h.settle()
	assert.Equal(t, writes, h.out.count(), "the same frame is not written")
	require.NoError(t, h.stop())
}

// A burst already waiting when the loop wakes costs one frame.
func TestLoopDrawsABurstOnce(t *testing.T) {
	burst := make([]Msg, 100)
	for i := range burst {
		burst[i] = countMsg{}
	}
	h := startHarness(t, counter{}, burst...)
	h.waitFor("the burst", func() bool { return h.log.viewCount() >= 1 })
	h.settle()
	// The first frame came at once; the burst waits for the frame budget.
	h.clock.advance(time.Second / FrameRate)
	h.waitFor("the burst's frame", func() bool { return h.row(0) == "count 100" })
	h.settle()
	assert.Equal(t, 2, h.log.viewCount())
	assert.Equal(t, 0, h.clock.pending())
	require.NoError(t, h.stop())
}

// A change after a pause draws at once; one within the frame budget waits
// for it, on one timer.
func TestLoopEchoesAtOnceAfterAPauseAndKeepsTheBudget(t *testing.T) {
	h := startHarness(t, counter{})
	h.waitFor("the first frame", func() bool { return h.log.viewCount() == 1 })
	h.settle()
	h.clock.advance(time.Second)
	h.p.Send(countMsg{})
	h.waitFor("the echo", func() bool { return h.log.viewCount() == 2 })
	assert.Equal(t, 0, h.clock.pending(), "drawn without a timer")

	// Within the budget: no frame until it passes.
	h.clock.advance(10 * time.Millisecond)
	h.p.Send(countMsg{})
	h.waitFor("the update", func() bool { return h.clock.pending() == 1 })
	h.p.Send(countMsg{})
	h.settle()
	assert.Equal(t, 2, h.log.viewCount())
	assert.Equal(t, 1, h.clock.pending(), "one timer for the frame")
	h.clock.advance(time.Second/FrameRate - 10*time.Millisecond - time.Millisecond)
	h.settle()
	assert.Equal(t, 2, h.log.viewCount(), "not before the budget")
	h.clock.advance(time.Millisecond)
	h.waitFor("the frame", func() bool { return h.log.viewCount() == 3 })
	assert.Equal(t, "count 3", h.row(0))
	h.settle()
	assert.Equal(t, 0, h.clock.pending())
	require.NoError(t, h.stop())
}

// Commands run off the loop and their results come back to Update; a
// batch runs each, and Quit ends the program after a last frame.
func TestLoopRunsCommands(t *testing.T) {
	type fromInit struct{}
	type fromBatch struct{ i int }
	h := startHarness(t, counter{init: func() Msg { return fromInit{} }})
	h.waitFor("init's result", func() bool { return contains(h.log.all(), "update term.fromInit") })
	h.p.Send(cmdMsg{cmd: Batch(
		func() Msg { return fromBatch{1} }, nil, func() Msg { return fromBatch{2} },
		func() Msg { return countMsg{} },
	)})
	h.waitFor("the batch", func() bool { return count(h.log.all(), "update term.fromBatch") == 2 })
	h.p.Send(cmdMsg{cmd: Tick(0, func(time.Time) Msg { return countMsg{} })})
	h.waitFor("the tick", func() bool { return count(h.log.all(), "update term.countMsg") == 2 })
	h.clock.advance(time.Second)
	require.NoError(t, h.stop())
	assert.Equal(t, 2, h.final.(counter).n) //nolint:forcetypeassert // the model's own type
	// The size comes first, then Init's result.
	assert.Equal(t, "update term.WindowSizeMsg", h.log.all()[0])
	assert.Equal(t, 0, count(h.log.all(), "update term.QuitMsg"), "the loop's own messages stay out of Update")
}

func TestLoopSetsMouseAndTitleAndRestoresThem(t *testing.T) {
	h := startHarness(t, counter{})
	h.waitFor("the first frame", func() bool { return h.log.viewCount() == 1 })
	h.clock.advance(time.Second)
	h.p.Send(mouseMsg("on"))
	h.p.Send(titleMsg("uah · busy"))
	h.waitFor("the modes", func() bool { return strings.Contains(h.out.String(), "uah · busy") })
	out := h.out.String()
	assert.Contains(t, out, mouseOn)
	assert.Contains(t, out, "\x1b]2;uah · busy\x07")
	require.NoError(t, h.stop())
	out = h.out.String()
	leave := resetFrame + mouseOff + "\x1b]2;\x07" + "\x1b[0 q" + keysOff + pasteOff + autowrapOn + cursorShow + altScreenOff
	assert.True(t, strings.HasSuffix(out, leave), "leave: %q", out[max(0, len(out)-120):])
}

// The cursor's shape and colour are set when they change, and reset when
// the program ends.
func TestLoopSetsTheCursorStyle(t *testing.T) {
	out := &output{}
	p := NewProgram(blinking{}, Options{In: strings.NewReader(""), Out: out, Width: 10, Height: 2, Profile: colorprofile.TrueColor})
	p.Send(QuitMsg{})
	_, err := p.Run(context.Background())
	require.NoError(t, err)
	assert.Contains(t, out.String(), "\x1b[1 q\x1b]12;#c0c0c0\x07")
	assert.Contains(t, out.String(), "\x1b[0 q\x1b]112\x07"+keysOff)
}

type blinking struct{}

func (blinking) Init() Cmd                 { return nil }
func (b blinking) Update(Msg) (Model, Cmd) { return b, nil }

func (blinking) View() View {
	v := NewView("hi")
	v.Cursor = &Cursor{Position: Position{X: 2}, Blink: true, Color: ansi.BasicColor(7)}

	return v
}

func TestLoopCopiesWithOSC52(t *testing.T) {
	h := startHarness(t, counter{})
	h.p.Send(cmdMsg{cmd: SetClipboard("hi")})
	h.waitFor("the copy", func() bool { return strings.Contains(h.out.String(), "\x1b]52;c;aGk=\x07") })
	require.NoError(t, h.stop())
}

// Synchronized output is used only once the terminal says it has it.
func TestLoopSyncsOnlyWhenTheTerminalAnswers(t *testing.T) {
	h := startHarness(t, counter{})
	h.waitFor("the first frame", func() bool { return h.log.viewCount() == 1 })
	assert.Contains(t, h.out.String(), querySync)
	assert.NotContains(t, h.out.String(), "\x1b[?2026h")
	_, err := h.in.Write([]byte("\x1b[?2026;2$y")) // DECRPM: supported, reset
	require.NoError(t, err)
	h.settle()
	h.clock.advance(time.Second)
	h.p.Send(countMsg{})
	h.waitFor("the frame", func() bool { return h.row(0) == "count 1" })
	assert.Contains(t, h.out.String(), "\x1b[?2026h")
	require.NoError(t, h.stop())
}

// Keys typed on the terminal reach Update as term's own messages.
func TestLoopDecodesInput(t *testing.T) {
	h := startHarness(t, counter{})
	_, err := h.in.Write([]byte("a\x1b[13;2u\x1b[200~pasted\x1b[201~"))
	require.NoError(t, err)
	h.waitFor("the paste", func() bool { return contains(h.log.all(), "update term.PasteMsg") })
	assert.GreaterOrEqual(t, count(h.log.all(), "update term.KeyPressMsg"), 2)
	require.NoError(t, h.stop())
}

// A panic in a command is raised again on the loop, after the terminal is
// restored.
func TestLoopRestoresTheTerminalOnAPanic(t *testing.T) {
	out := &output{}
	p := NewProgram(counter{log: newEvents(), init: func() Msg { panic("boom") }},
		Options{In: strings.NewReader(""), Out: out, Width: 20, Height: 4, Env: []string{"TERM=xterm"}, Profile: colorprofile.TrueColor})
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		_, _ = p.Run(context.Background())
	}()
	require.NotNil(t, recovered)
	assert.Contains(t, fmt.Sprint(recovered), "boom")
	assert.True(t, strings.HasSuffix(out.String(), cursorShow+altScreenOff), "restored: %q", out.String())
}

func TestLoopRestoresTheTerminalOnAPanicInUpdate(t *testing.T) {
	out := &output{}
	p := NewProgram(panicky{}, Options{In: strings.NewReader(""), Out: out, Width: 20, Height: 4, Profile: colorprofile.TrueColor})
	assert.PanicsWithValue(t, "update", func() { _, _ = p.Run(context.Background()) })
	assert.True(t, strings.HasSuffix(out.String(), cursorShow+altScreenOff))
}

type panicky struct{}

func (panicky) Init() Cmd { return func() Msg { return countMsg{} } }

func (panicky) Update(msg Msg) (Model, Cmd) {
	if _, ok := msg.(countMsg); ok {
		panic("update")
	}

	return panicky{}, nil
}

func (panicky) View() View { return NewView("") }

// ctrl+g's editor runs with the terminal released, and the screen is
// drawn whole after it.
func TestLoopExecReleasesTheTerminal(t *testing.T) {
	h := startHarness(t, counter{})
	h.waitFor("the first frame", func() bool { return h.log.viewCount() == 1 })
	h.settle()
	run := &fakeEditor{}
	h.p.Send(cmdMsg{cmd: Exec(run, func(err error) Msg { return editedMsg{err} })})
	h.waitFor("the callback", func() bool { return contains(h.log.all(), "update term.editedMsg") })
	h.clock.advance(time.Second)
	h.waitFor("the redraw", func() bool { return h.log.viewCount() == 2 })
	h.settle()
	out := h.out.String()
	i := strings.Index(out, "editor ran")
	require.Positive(t, i)
	assert.True(t, strings.HasSuffix(out[:i], altScreenOff), "left before: %q", out[max(0, i-40):i])
	assert.True(t, strings.HasPrefix(out[i+len("editor ran"):], altScreenOn), "entered after")
	assert.Contains(t, out[i:], "\x1b[2J\x1b[1Hcount 0", "a full redraw after")
	require.NoError(t, h.stop())
}

type editedMsg struct{ err error }

type fakeEditor struct {
	out io.Writer
}

func (f *fakeEditor) SetStdin(io.Reader)    {}
func (f *fakeEditor) SetStdout(w io.Writer) { f.out = w }
func (f *fakeEditor) SetStderr(io.Writer)   {}

func (f *fakeEditor) Run() error {
	_, err := io.WriteString(f.out, "editor ran")

	return err
}

func TestRunStopsWithTheContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	inR, inW := io.Pipe()
	defer inW.Close()
	out := &output{}
	done := make(chan error, 1)
	go func() {
		_, err := Run(ctx, counter{log: newEvents()}, Options{In: inR, Out: out, Width: 10, Height: 2, Profile: colorprofile.TrueColor})
		done <- err
	}()
	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop")
	}
	assert.True(t, strings.HasSuffix(out.String(), altScreenOff))
}

func TestOpenTerminalRefusesAPipe(t *testing.T) {
	_, err := Run(context.Background(), counter{log: newEvents()}, Options{In: strings.NewReader(""), Out: io.Discard})
	assert.True(t, errors.Is(err, errNotTerminal))
}

func TestColoursAreDownsampled(t *testing.T) {
	out := &output{}
	p := NewProgram(styled{}, Options{In: strings.NewReader(""), Out: out, Width: 10, Height: 1, Profile: colorprofile.ANSI256})
	p.Send(QuitMsg{})
	_, err := p.Run(context.Background())
	require.NoError(t, err)
	assert.NotContains(t, out.String(), "38;2;")
	assert.Contains(t, out.String(), "38;5;")
}

type styled struct{}

func (styled) Init() Cmd                 { return nil }
func (s styled) Update(Msg) (Model, Cmd) { return s, nil }
func (styled) View() View                { return NewView("\x1b[38;2;255;100;0mhi\x1b[m") }

func TestTitleDropsControlCharacters(t *testing.T) {
	assert.Equal(t, "\x1b]2;ab\x07", oscTitle("a\x07\x1bb"))
}

func TestQuerySyncFor(t *testing.T) {
	assert.True(t, querySyncFor([]string{"TERM=xterm-256color"}))
	assert.False(t, querySyncFor([]string{"TERM=xterm-256color", "TERM_PROGRAM=Apple_Terminal"}))
	assert.False(t, querySyncFor([]string{"TERM=xterm-256color", "SSH_TTY=/dev/ttys001"}))
	assert.True(t, querySyncFor([]string{"TERM=xterm-ghostty", "SSH_TTY=/dev/ttys001"}))
}

func contains(list []string, s string) bool { return count(list, s) > 0 }

func count(list []string, s string) int {
	n := 0
	for _, x := range list {
		if x == s {
			n++
		}
	}

	return n
}

// On a dumb terminal, or with no TERM, the colours go but the control
// sequences stay, so the screen is still drawn in place.
func TestDumbTerminalKeepsControlSequences(t *testing.T) {
	for _, env := range [][]string{{"TERM=dumb"}, {}} {
		out := &output{}
		p := NewProgram(styled{}, Options{In: strings.NewReader(""), Out: out, Width: 10, Height: 2, Env: env})
		p.Send(QuitMsg{})
		_, err := p.Run(context.Background())
		require.NoError(t, err)
		assert.Contains(t, out.String(), altScreenOn)
		assert.Contains(t, out.String(), "\x1b[1H")
		assert.Contains(t, out.String(), "hi")
		assert.NotContains(t, out.String(), "38;2;")
	}
}

// failing is an output whose writes fail while fail is set.
type failing struct {
	output
	fail atomic.Bool
}

var errBroken = errors.New("broken pipe")

func (f *failing) Write(b []byte) (int, error) {
	if f.fail.Load() {
		return 0, errBroken
	}

	return f.output.Write(b)
}

// A frame that fails to write is drawn whole the next time; frames that
// keep failing end the program with the error.
func TestFrameWriteErrors(t *testing.T) {
	out := &failing{}
	clk := newFakeClock()
	inR, inW := io.Pipe()
	defer inW.Close()
	log := newEvents()
	p := NewProgram(counter{log: log}, Options{In: inR, Out: out, Width: 20, Height: 4, Profile: colorprofile.TrueColor, clock: clk})
	done := make(chan error, 1)
	go func() {
		_, err := p.Run(context.Background())
		done <- err
	}()
	waitUntil(t, func() bool { return log.viewCount() == 1 })
	out.fail.Store(true)
	clk.advance(time.Second)
	p.Send(countMsg{})
	waitUntil(t, func() bool { return log.viewCount() == 2 })
	out.fail.Store(false)
	before := len(out.String())
	clk.advance(time.Second)
	waitUntil(t, func() bool { return log.viewCount() == 3 })
	waitUntil(t, func() bool { return len(out.String()) > before })
	assert.Contains(t, out.String()[before:], "\x1b[2J", "the failed frame is drawn whole")

	out.fail.Store(true)
	for i := range maxWriteFails {
		clk.advance(time.Second)
		p.Send(countMsg{})
		waitUntil(t, func() bool { return log.viewCount() >= 4+i })
	}
	select {
	case err := <-done:
		require.ErrorIs(t, err, errBroken)
	case <-time.After(5 * time.Second):
		t.Fatal("the program did not end on failing writes")
	}
}

// A terminal that cannot be taken back after Exec ends the program with
// the error; the program's own error goes to its callback.
func TestExecReacquireFailureEndsTheProgram(t *testing.T) {
	out := &failing{}
	inR, inW := io.Pipe()
	defer inW.Close()
	log := newEvents()
	p := NewProgram(counter{log: log}, Options{In: inR, Out: out, Width: 20, Height: 4, Profile: colorprofile.TrueColor})
	done := make(chan error, 1)
	go func() {
		_, err := p.Run(context.Background())
		done <- err
	}()
	waitUntil(t, func() bool { return log.viewCount() == 1 })
	p.Send(cmdMsg{cmd: Exec(breaker{out}, func(err error) Msg { return editedMsg{err} })})
	select {
	case err := <-done:
		require.ErrorIs(t, err, errBroken)
		assert.Contains(t, err.Error(), "take the terminal back")
	case <-time.After(5 * time.Second):
		t.Fatal("the program did not end")
	}
	assert.False(t, contains(log.all(), "update term.editedMsg"))
}

// breaker is a program that breaks the terminal's output while it runs.
type breaker struct{ out *failing }

func (breaker) SetStdin(io.Reader)  {}
func (breaker) SetStdout(io.Writer) {}
func (breaker) SetStderr(io.Writer) {}

func (b breaker) Run() error {
	b.out.fail.Store(true)

	return nil
}

func waitUntil(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !check() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(time.Millisecond)
	}
}
