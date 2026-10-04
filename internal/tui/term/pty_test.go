//go:build darwin || linux

package term_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/tui/term"
)

// These tests run term.Run on a real pseudo-terminal. The test binary runs
// itself again as the child: a wrapper that is the session leader of the
// pty and records its termios before and after, and under it the program,
// a small model on term.Run. The parent reads the pty into a VT emulator,
// which answers the program's queries, and checks the screen, the bytes
// written, and the terminal's modes.

const (
	// childEnv picks the role of a re-executed test binary.
	childEnv = "TERM_PTY_CHILD"
	// dirEnv is the directory the child writes its records to.
	dirEnv = "TERM_PTY_DIR"
	// leaveSeq is what leaving writes last: the reverse of entering.
	leaveSeq = "\x1b[<u\x1b[>4m\x1b[?2004l\x1b[?7h\x1b[?25h\x1b[?1049l"
	// ptyDeadline bounds every wait on the child.
	ptyDeadline = 30 * time.Second
)

func TestMain(m *testing.M) {
	switch os.Getenv(childEnv) {
	case "wrap":
		os.Exit(wrapChild())
	case "tui":
		os.Exit(tuiChild())
	}
	os.Exit(m.Run())
}

// wrapChild is the session leader of the pty. It records the terminal's
// modes, runs the program under it, records them again once the program
// ended (however it ended), and exits with its status. It forwards the
// signals that stop the program, which the parent sends to it.
func wrapChild() int {
	dir := os.Getenv(dirEnv)
	if err := recordStty(filepath.Join(dir, "before")); err != nil {
		fmt.Fprintln(os.Stderr, err)

		return 3
	}
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)

		return 3
	}
	cmd := exec.Command(exe, "-test.run=^$")
	cmd.Env = append(os.Environ(), childEnv+"=tui")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGINT)
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, err)

		return 3
	}
	go func() {
		for s := range sigs {
			_ = cmd.Process.Signal(s)
		}
	}()
	_ = cmd.Wait()
	code := cmd.ProcessState.ExitCode()
	if err := recordStty(filepath.Join(dir, "after")); err != nil {
		fmt.Fprintln(os.Stderr, err)

		return 3
	}

	return code
}

// recordStty writes the terminal's modes, as `stty -g` prints them, to
// path.
func recordStty(path string) error {
	cmd := exec.Command("stty", "-g")
	cmd.Stdin = os.Stdin
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("stty -g: %w", err)
	}

	return os.WriteFile(path, bytes.TrimSpace(out), 0o600)
}

// tuiChild runs ptyModel on the process's terminal.
func tuiChild() int {
	m := ptyModel{dir: os.Getenv(dirEnv)}
	if _, err := term.Run(context.Background(), m, term.Options{}); err != nil {
		fmt.Fprintln(os.Stderr, "run:", err)

		return 1
	}

	return 0
}

// ptyModel shows the size it got, the keys, the last paste, and the last
// program run. q quits, p panics in a command, u panics in Update, and e
// runs a program that reads a line with the terminal released.
type ptyModel struct {
	dir   string
	w, h  int
	keys  []string
	paste string
	exec  string
}

// execDoneMsg is the exec callback's message.
type execDoneMsg struct{ err error }

func (m ptyModel) Init() term.Cmd { return nil }

func (m ptyModel) Update(msg term.Msg) (term.Model, term.Cmd) {
	switch msg := msg.(type) {
	case term.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
	case term.PasteMsg:
		m.paste = msg.Content
	case execDoneMsg:
		m.exec = "done"
		if msg.err != nil {
			m.exec = "error " + msg.err.Error()
		}
	case term.KeyPressMsg:
		k := msg.String()
		m.keys = append(m.keys, k)
		switch k {
		case "q":
			return m, term.Quit
		case "p":
			return m, func() term.Msg { panic("pty command panic") }
		case "u":
			panic("pty update panic")
		case "e":
			script := `stty -a > "$TERM_PTY_DIR/exec-stty"; printf 'editor-ran> '; read line; printf '%s' "$line" > "$TERM_PTY_DIR/exec-line"`

			return m, term.Exec(&execCmd{args: []string{"sh", "-c", script}}, func(err error) term.Msg { return execDoneMsg{err} })
		}
	}

	return m, nil
}

func (m ptyModel) View() term.View {
	lines := []string{
		"pty child ready",
		fmt.Sprintf("size %dx%d", m.w, m.h),
		"keys " + strings.Join(m.keys, ","),
		"paste " + m.paste,
		"exec " + m.exec,
	}
	for len(lines) < m.h-1 {
		lines = append(lines, "")
	}
	lines = append(lines, fmt.Sprintf("bottom %dx%d", m.w, m.h))

	return term.NewView(strings.Join(lines, "\n"))
}

// execCmd is a program as term.Exec runs it, killed when its context ends.
type execCmd struct {
	args     []string
	ctx      context.Context
	stdin    io.Reader
	out, err io.Writer
}

func (c *execCmd) SetStdin(r io.Reader)           { c.stdin = r }
func (c *execCmd) SetStdout(w io.Writer)          { c.out = w }
func (c *execCmd) SetStderr(w io.Writer)          { c.err = w }
func (c *execCmd) SetContext(ctx context.Context) { c.ctx = ctx }

func (c *execCmd) Run() error {
	ctx := c.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := exec.CommandContext(ctx, c.args[0], c.args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = c.stdin, c.out, c.err

	return cmd.Run()
}

// ptyRun is a child on a pty, the emulator that shows its output, and the
// raw bytes it wrote.
type ptyRun struct {
	t      *testing.T
	master *os.File
	// slave stays open in the parent, so stty can read the modes while the
	// child runs.
	slave *os.File
	cmd   *exec.Cmd
	dir   string
	done  chan struct{}

	mu  sync.Mutex
	em  *vt.Emulator
	log bytes.Buffer
}

func startPty(t *testing.T, cols, rows int) *ptyRun {
	t.Helper()
	master, slave, err := pty.Open()
	if err != nil {
		t.Skipf("no pty: %v", err)
	}
	require.NoError(t, pty.Setsize(master, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)}))
	exe, err := os.Executable()
	require.NoError(t, err)

	r := &ptyRun{t: t, master: master, slave: slave, dir: t.TempDir(), done: make(chan struct{}), em: vt.NewEmulator(cols, rows)}
	r.cmd = exec.Command(exe, "-test.run=^$")
	// Without atexit_sleep_ms=0 a race-enabled child sleeps a second as it
	// exits.
	r.cmd.Env = append(childEnviron(), childEnv+"=wrap", dirEnv+"="+r.dir, "TERM=xterm-256color",
		"GORACE="+strings.TrimSpace(os.Getenv("GORACE")+" atexit_sleep_ms=0"))
	r.cmd.Stdin, r.cmd.Stdout, r.cmd.Stderr = slave, slave, slave
	r.cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	require.NoError(t, r.cmd.Start())
	go func() {
		_ = r.cmd.Wait()
		close(r.done)
	}()
	go r.readOutput()
	// The emulator writes its answers to queries into a pipe, which blocks
	// its Write until they are read: they go back to the child as input.
	go func() { _, _ = io.Copy(master, r.em) }()
	t.Cleanup(func() {
		select {
		case <-r.done:
		default:
			_ = r.cmd.Process.Kill()
			<-r.done
		}
		_ = master.Close()
		_ = slave.Close()
		if c, ok := r.em.InputPipe().(io.Closer); ok {
			_ = c.Close()
		}
		if t.Failed() {
			t.Logf("screen:\n%s\nraw output: %q", strings.Join(r.rows(), "\n"), r.output())
		}
	})

	return r
}

// childEnviron is the environment without what changes how term talks to
// the terminal.
func childEnviron() []string {
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch k {
		case "TERM", "TERM_PROGRAM", "COLORTERM", "NO_COLOR", "SSH_TTY", "TMUX", "GORACE", childEnv, dirEnv:
			continue
		}
		env = append(env, kv)
	}

	return env
}

func (r *ptyRun) readOutput() {
	buf := make([]byte, 4096)
	for {
		n, err := r.master.Read(buf)
		if n > 0 {
			r.mu.Lock()
			r.log.Write(buf[:n])
			_, _ = r.em.Write(buf[:n])
			r.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

// output is every byte the child wrote.
func (r *ptyRun) output() string {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.log.String()
}

// rows is the emulator's screen, a string a row, trailing spaces cut.
func (r *ptyRun) rows() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, r.em.Height())
	for y := range out {
		var b strings.Builder
		for x := range r.em.Width() {
			if c := r.em.CellAt(x, y); c != nil {
				b.WriteString(c.Content)
			}
		}
		out[y] = strings.TrimRight(b.String(), " ")
	}

	return out
}

func (r *ptyRun) altScreen() bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.em.IsAltScreen()
}

// waitFor polls until check holds, or fails the test.
func (r *ptyRun) waitFor(what string, check func() bool) {
	r.t.Helper()
	deadline := time.Now().Add(ptyDeadline)
	for !check() {
		if time.Now().After(deadline) {
			require.FailNow(r.t, "timed out waiting for "+what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// waitScreen waits until the alt screen shows the model's view at w×h with
// these rows below the first two, and the rest blank.
func (r *ptyRun) waitScreen(w, h int, keys, paste, execResult string) {
	r.t.Helper()
	want := make([]string, h)
	want[0] = "pty child ready"
	want[1] = fmt.Sprintf("size %dx%d", w, h)
	want[2] = strings.TrimRight("keys "+keys, " ")
	want[3] = strings.TrimRight("paste "+paste, " ")
	want[4] = strings.TrimRight("exec "+execResult, " ")
	want[h-1] = fmt.Sprintf("bottom %dx%d", w, h)
	r.waitFor(fmt.Sprintf("the screen %q", want), func() bool {
		if !r.altScreen() {
			return false
		}
		got := r.rows()

		return len(got) == h && strings.Join(got, "\n") == strings.Join(want, "\n")
	})
}

func (r *ptyRun) send(s string) {
	r.t.Helper()
	_, err := r.master.WriteString(s)
	require.NoError(r.t, err)
}

// resize resizes the emulator, then the pty, which sends SIGWINCH.
func (r *ptyRun) resize(w, h int) {
	r.t.Helper()
	r.mu.Lock()
	r.em.Resize(w, h)
	r.mu.Unlock()
	require.NoError(r.t, pty.Setsize(r.master, &pty.Winsize{Rows: uint16(h), Cols: uint16(w)}))
}

func (r *ptyRun) signal(s os.Signal) {
	r.t.Helper()
	require.NoError(r.t, r.cmd.Process.Signal(s))
}

// wait waits for the child to exit and returns its status.
func (r *ptyRun) wait() int {
	r.t.Helper()
	select {
	case <-r.done:
	case <-time.After(ptyDeadline):
		require.FailNow(r.t, "timed out waiting for the child to exit")
	}

	return r.cmd.ProcessState.ExitCode()
}

// stty runs stty on the pty, from the parent.
func (r *ptyRun) stty(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("stty", args...)
	cmd.Stdin = r.slave
	out, err := cmd.Output()
	require.NoError(r.t, err)

	return string(out)
}

func (r *ptyRun) file(name string) string {
	r.t.Helper()
	b, err := os.ReadFile(filepath.Join(r.dir, name))
	require.NoError(r.t, err)

	return string(b)
}

// assertRaw checks that stty's report shows raw mode: no canonical input
// and no echo.
func assertRaw(t *testing.T, stty string) {
	t.Helper()
	fields := strings.Fields(stty)
	assert.Contains(t, fields, "-icanon")
	assert.Contains(t, fields, "-echo")
}

// assertCooked checks that stty's report shows cooked mode.
func assertCooked(t *testing.T, stty string) {
	t.Helper()
	fields := strings.Fields(stty)
	assert.Contains(t, fields, "icanon")
	assert.Contains(t, fields, "echo")
}

// assertRestored checks that the child exited with the terminal as it
// found it: the leave sequence written, the main screen back, and the same
// modes.
func (r *ptyRun) assertRestored() {
	r.t.Helper()
	r.waitFor("the leave sequence", func() bool { return strings.Contains(r.output(), leaveSeq) })
	assert.False(r.t, r.altScreen(), "the emulator is back on the main screen")
	assert.Equal(r.t, r.file("before"), r.file("after"), "the terminal's modes are restored")
}

func TestPtyEnterAndLeave(t *testing.T) {
	t.Parallel()
	r := startPty(t, 80, 24)
	r.waitScreen(80, 24, "", "", "")
	assertRaw(t, r.stty("-a"))
	assert.True(t, strings.HasPrefix(r.output(), "\x1b[?1049h"), "the alt screen comes first")

	r.send("q")
	assert.Equal(t, 0, r.wait())
	r.assertRestored()
	assert.True(t, strings.HasSuffix(r.output(), leaveSeq), "the output ends with the leave sequence: %q", r.output())
}

func TestPtyResize(t *testing.T) {
	t.Parallel()
	r := startPty(t, 80, 24)
	r.waitScreen(80, 24, "", "", "")

	r.resize(100, 30)
	r.waitScreen(100, 30, "", "", "")
	r.resize(60, 15)
	r.waitScreen(60, 15, "", "", "")

	r.send("q")
	assert.Equal(t, 0, r.wait())
	r.assertRestored()
}

func TestPtyExecRoundTrip(t *testing.T) {
	t.Parallel()
	r := startPty(t, 80, 24)
	r.waitScreen(80, 24, "", "", "")

	r.send("e")
	r.waitFor("the editor's prompt on the main screen", func() bool {
		return !r.altScreen() && strings.Contains(strings.Join(r.rows(), "\n"), "editor-ran>")
	})
	out := r.output()
	assert.Less(t, strings.Index(out, leaveSeq), strings.Index(out, "editor-ran>"), "the terminal is released before the editor runs")
	assertCooked(t, r.file("exec-stty"))

	// Cooked mode turns the return into a newline and echoes the line.
	r.send("hello\r")
	r.waitScreen(80, 24, "e", "", "done")
	assert.Equal(t, "hello", r.file("exec-line"))
	assertRaw(t, r.stty("-a"))
	assert.Contains(t, r.output()[strings.Index(out, "editor-ran>"):], "hello", "the editor's input was echoed")

	r.send("b")
	r.waitScreen(80, 24, "e,b", "", "done")

	r.send("q")
	assert.Equal(t, 0, r.wait())
	r.assertRestored()
}

func TestPtyRestoresOnPanic(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, key, text string }{
		{"command", "p", "pty command panic"},
		{"update", "u", "pty update panic"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := startPty(t, 80, 24)
			r.waitScreen(80, 24, "", "", "")

			r.send(tc.key)
			assert.NotEqual(t, 0, r.wait())
			r.assertRestored()
			r.waitFor("the panic text", func() bool { return strings.Contains(r.output(), tc.text) })
			out := r.output()
			assert.Less(t, strings.LastIndex(out, leaveSeq), strings.Index(out, tc.text), "the terminal is restored before the panic is printed")
		})
	}
}

func TestPtyStopsOnSignal(t *testing.T) {
	t.Parallel()
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGHUP, syscall.SIGINT} {
		t.Run(sig.String(), func(t *testing.T) {
			t.Parallel()
			r := startPty(t, 80, 24)
			r.waitScreen(80, 24, "", "", "")

			r.signal(sig)
			assert.Equal(t, 0, r.wait())
			r.assertRestored()
			assert.True(t, strings.HasSuffix(r.output(), leaveSeq), "the output ends with the leave sequence: %q", r.output())
		})
	}
}

func TestPtyKeys(t *testing.T) {
	t.Parallel()
	r := startPty(t, 80, 24)
	r.waitScreen(80, 24, "", "", "")

	r.send("a")
	r.waitScreen(80, 24, "a", "", "")
	r.send("\x1b[13;2u")
	r.waitScreen(80, 24, "a,shift+enter", "", "")
	r.send("\x1b[200~paste\x1b[201~")
	r.waitScreen(80, 24, "a,shift+enter", "paste", "")

	r.send("q")
	assert.Equal(t, 0, r.wait())
}

func TestPtyIdle(t *testing.T) {
	t.Parallel()
	r := startPty(t, 80, 24)
	r.waitScreen(80, 24, "", "", "")

	n := len(r.output())
	time.Sleep(300 * time.Millisecond)
	assert.Equal(t, n, len(r.output()), "nothing is written while idle: %q", r.output()[n:])

	r.send("q")
	assert.Equal(t, 0, r.wait())
}

func TestPtyResizeDuringExec(t *testing.T) {
	t.Parallel()
	r := startPty(t, 80, 24)
	r.waitScreen(80, 24, "", "", "")

	r.send("e")
	r.waitFor("the editor's prompt on the main screen", func() bool {
		return !r.altScreen() && strings.Contains(strings.Join(r.rows(), "\n"), "editor-ran>")
	})
	r.resize(100, 30)
	r.send("hello\r")
	r.waitScreen(100, 30, "e", "", "done")

	r.send("q")
	assert.Equal(t, 0, r.wait())
	r.assertRestored()
}

// Keys still queued when the program quits do not hold it: the input
// reader stops even when its queue is full.
func TestPtyQuitsWithInputQueued(t *testing.T) {
	t.Parallel()
	r := startPty(t, 80, 24)
	r.waitScreen(80, 24, "", "", "")

	r.send("q" + strings.Repeat("a", 400))
	assert.Equal(t, 0, r.wait())
	r.assertRestored()
}

// ctrl+c in the editor interrupts the editor, not the TUI: the SIGINT the
// terminal sends with it is dropped, and the TUI comes back.
func TestPtyInterruptInTheEditor(t *testing.T) {
	t.Parallel()
	r := startPty(t, 80, 24)
	r.waitScreen(80, 24, "", "", "")

	r.send("e")
	r.waitFor("the editor's prompt", func() bool {
		return !r.altScreen() && strings.Contains(strings.Join(r.rows(), "\n"), "editor-ran>")
	})
	r.send("\x03")
	r.waitFor("the TUI back with the editor's error", func() bool {
		rows := strings.Join(r.rows(), "\n")

		return r.altScreen() && strings.Contains(rows, "exec error")
	})
	r.send("b")
	r.waitFor("the next key", func() bool { return strings.Contains(strings.Join(r.rows(), "\n"), "keys e,b") })

	r.send("q")
	assert.Equal(t, 0, r.wait())
	r.assertRestored()
}

// SIGTERM while the editor runs ends uah at once: the editor is killed and
// the terminal restored, where the loop would otherwise wait for it.
func TestPtyStopsDuringTheEditor(t *testing.T) {
	t.Parallel()
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGHUP} {
		t.Run(sig.String(), func(t *testing.T) {
			t.Parallel()
			r := startPty(t, 80, 24)
			r.waitScreen(80, 24, "", "", "")

			r.send("e")
			r.waitFor("the editor's prompt", func() bool {
				return !r.altScreen() && strings.Contains(strings.Join(r.rows(), "\n"), "editor-ran>")
			})
			r.signal(sig)
			assert.Equal(t, 0, r.wait())
			r.assertRestored()
			assert.False(t, r.altScreen())
		})
	}
}
