package term

import (
	"context"
	"io"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A reader blocked on a full queue returns once stop is closed and its
// input ends, so quitting with keys queued does not hang.
func TestReadInputStopsWithAFullQueue(t *testing.T) {
	pr, pw := io.Pipe()
	out := make(chan Msg) // never read
	stop := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- readInput(context.Background(), pr, "xterm", out, stop) }()
	go func() { _, _ = pw.Write([]byte(strings.Repeat("a", 400))) }()
	time.Sleep(50 * time.Millisecond)
	close(stop)
	_ = pw.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("readInput did not return after stop")
	}
}

// The keys decoded before the input ends are delivered.
func TestReadInputDeliversTheLastKeys(t *testing.T) {
	out := make(chan Msg, 16)
	err := readInput(context.Background(), strings.NewReader("abc"), "xterm", out, make(chan struct{}))
	require.NoError(t, err)
	var got []string
	for len(out) > 0 {
		got = append(got, (<-out).(KeyPressMsg).String()) //nolint:forcetypeassert // keys only
	}
	assert.Equal(t, []string{"a", "b", "c"}, got)
}

// A terminal's input that ends without a signal ends the program: it is
// reported, where a headless program's input may end.
func TestInputEndReportedOnATerminal(t *testing.T) {
	for _, tty := range []bool{true, false} {
		pr, pw := io.Pipe()
		tm := &terminal{in: pr, tty: tty, eofEnds: tty, input: make(chan Msg, 64)}
		require.NoError(t, tm.startInput(context.Background()))
		_ = pw.Close()
		<-tm.readDone
		select {
		case msg := <-tm.input:
			assert.True(t, tty, "headless input ending is no error")
			assert.ErrorIs(t, msg.(inputEndMsg).err, errTerminalGone) //nolint:forcetypeassert // the only message
		default:
			assert.False(t, tty, "a terminal's input ending is reported")
		}
	}
}

// A SIGINT stops the program only while it has the terminal and not just
// after a program it ran there, where ctrl+c sent it; other signals always
// stop it.
func TestStopSignals(t *testing.T) {
	tm := &terminal{entered: true}
	assert.True(t, tm.stops(syscall.SIGINT))
	tm.released = time.Now()
	assert.False(t, tm.stops(syscall.SIGINT), "just after the editor")
	assert.True(t, tm.stops(syscall.SIGTERM))
	tm.released = time.Now().Add(-2 * interruptGrace)
	assert.True(t, tm.stops(syscall.SIGINT))
	tm.entered = false
	assert.False(t, tm.stops(syscall.SIGINT), "while the editor runs")
	assert.True(t, tm.stops(syscall.SIGHUP))
}

func TestDropInput(t *testing.T) {
	tm := &terminal{input: make(chan Msg, 4)}
	tm.input <- KeyPressMsg{Code: KeyEnter}
	tm.input <- KeyPressMsg{Code: 'a'}
	tm.dropInput()
	assert.Empty(t, tm.input)
}
