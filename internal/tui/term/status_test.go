package term

import (
	"context"
	"encoding/base64"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/colorprofile"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStatusReport(t *testing.T) {
	b64 := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	assert.Equal(t, "\x1b]7501;state=idle:app=uah\x1b\\", oscStatus("uah", Status{State: StateIdle}))
	assert.Equal(t, "\x1b]7501;state=blocked:kind=permission:app=uah:msg="+b64("Approve rm -rf build")+"\x1b\\",
		oscStatus("uah", Status{State: StateBlocked, Kind: KindPermission, Msg: "Approve rm -rf build"}))
	assert.Equal(t, "\x1b]7501;state=working:id=agent/scout:app=uah:title="+b64("scout (explorer)")+":msg="+b64("Bash ls")+"\x1b\\",
		oscStatus("uah", Status{ID: "agent/scout", State: StateWorking, Title: "scout (explorer)", Msg: "Bash ls"}))
	assert.NotContains(t, oscStatus("uah", Status{State: StateWorking, Kind: KindAuth}), "kind=", "a kind only with blocked")
	assert.Equal(t, "\x1b]7501;state=clear:id=agent/scout\x1b\\", oscStatusClear("agent/scout"))
}

// A report that breaks a limit is dropped whole, so the text is made to
// fit: control characters become spaces, and it is cut on a character's
// boundary.
func TestStatusTextFitsTheLimits(t *testing.T) {
	decode := func(s string) string {
		b, err := base64.StdEncoding.DecodeString(s)
		require.NoError(t, err)

		return string(b)
	}
	assert.Equal(t, "a b", decode(statusText("a\x1bb", 100)), "an ESC cannot end the sequence")
	assert.Equal(t, "a b", decode(statusText("a\u0085b", 100)), "nor a C1 control")

	long := decode(statusText(strings.Repeat("é", 2000), maxStatusMsg)) // 4000 bytes
	assert.Len(t, long, 2048)
	assert.Equal(t, strings.Repeat("é", 1024), long)
	odd := decode(statusText("a"+strings.Repeat("é", 200), maxStatusTitle))
	assert.Len(t, odd, 191, "a character that would cross the limit goes whole")
	assert.True(t, strings.HasSuffix(odd, "é"))

	full := oscStatus("uah", Status{
		ID: StatusID("agent", strings.Repeat("x", 40)), State: StateBlocked, Kind: KindPermission,
		Title: strings.Repeat("t", 500), Msg: strings.Repeat("m", 5000),
	})
	assert.LessOrEqual(t, len(full), 4096, "the whole sequence fits")
}

func TestStatusText_EncodesNewlinesAsSpaces(t *testing.T) {
	b, err := base64.StdEncoding.DecodeString(statusText("a\nb\tc\r", 100))
	require.NoError(t, err)
	assert.Equal(t, "a b c ", string(b))
}

func TestStatusID(t *testing.T) {
	for _, c := range []struct {
		in   []string
		want string
	}{
		{[]string{"agent", "scout"}, "agent/scout"},
		{[]string{"agent", "Scout #2"}, "agent/Scout-2"},
		{[]string{"agent", "a/b"}, "agent/a-b"},
		{[]string{"agent", "Émile"}, "agent/mile"},
		{[]string{"agent", "!!"}, "agent/_"},
		{[]string{"agent", strings.Repeat("x", 40)}, "agent/" + strings.Repeat("x", 32)},
		{strings.Split(strings.Repeat("a/", 10)+"a", "/"), strings.TrimSuffix(strings.Repeat("a/", 8), "/")},
		{[]string{"agent", strings.Repeat("x", 31) + " y"}, "agent/" + strings.Repeat("x", 31)}, // cut at the "-": no dash left at the end
	} {
		got := StatusID(c.in...)
		assert.Equal(t, c.want, got, "%q", c.in)
		assert.True(t, validStatusID(got), got)
	}
	assert.True(t, validStatusID("agent/a-"), "the grammar takes a dash anywhere")
	assert.False(t, validStatusID("agent/"+strings.Repeat("x", 33)))
	assert.False(t, validStatusID(strings.Repeat("a/", 9)+"a"))
	assert.False(t, validStatusID("agent//x"))
	assert.False(t, validStatusID("agent/a b"))
}

// The loop sends a record when it changes, not on every frame, clears one
// the view leaves out, sends them all again after a failed write, and on
// leaving clears the subagents' records and leaves the program's own idle.
func TestLoopReportsStatus(t *testing.T) {
	h := startHarness(t, counter{})
	h.waitFor("the first frame", func() bool { return h.log.viewCount() == 1 })
	assert.NotContains(t, h.out.String(), "7501", "no records, no reports")

	working := Status{State: StateWorking, Msg: "Thinking"}
	agent := Status{ID: "agent/scout", State: StateWorking, Title: "scout"}
	h.clock.advance(time.Second)
	h.p.Send(statusMsg{working, agent})
	h.waitFor("the reports", func() bool { return strings.Contains(h.out.String(), "id=agent/scout") })
	assert.Equal(t, 1, strings.Count(h.out.String(), oscStatus("uah", working)))

	h.clock.advance(time.Second)
	h.p.Send(countMsg{}) // a frame with the same records
	h.waitFor("the frame", func() bool { return h.row(0) == "count 1" })
	assert.Equal(t, 1, strings.Count(h.out.String(), oscStatus("uah", working)), "an unchanged record is not sent again")

	h.clock.advance(time.Second)
	h.p.Send(statusMsg{{State: StateDone, Msg: "Fixed"}, {ID: "bad id", State: StateWorking}})
	h.waitFor("the agent's clear", func() bool { return strings.Contains(h.out.String(), oscStatusClear("agent/scout")) })
	assert.Contains(t, h.out.String(), oscStatus("uah", Status{State: StateDone, Msg: "Fixed"}))
	assert.NotContains(t, h.out.String(), "bad id", "an ID the terminal would refuse is not sent")

	h.clock.advance(time.Second)
	h.p.Send(statusMsg{working, agent})
	h.waitFor("the agent again", func() bool { return strings.Count(h.out.String(), "id=agent/scout:") == 2 })
	require.NoError(t, h.stop())
	out := h.out.String()
	leave := out[strings.LastIndex(out, resetFrame):]
	assert.Contains(t, leave, oscStatusClear("agent/scout")+oscStatus("uah", Status{State: StateIdle}))
}

func TestLoopReportsNoStatusWithoutAnApp(t *testing.T) {
	p := &Program{opts: Options{}}
	var pre strings.Builder
	if p.opts.StatusApp != "" {
		p.writeStatus(&pre, []Status{{State: StateWorking}})
	}
	assert.Empty(t, pre.String())
	assert.Empty(t, p.leaveStatus("uah stopped: boom"))
}

func TestLeaveStatusReportsAFailure(t *testing.T) {
	p := &Program{opts: Options{StatusApp: "uah"}}
	assert.Equal(t, oscStatus("uah", Status{State: StateError, Msg: "uah stopped: boom"}), p.leaveStatus("uah stopped: boom"))
	assert.Empty(t, p.leaveStatus(""), "nothing sent, nothing to reset")
}

// A clear in a frame that fails to write is sent with the next one, and a
// record changed in it is sent again.
func TestLoopClearsAfterAFailedWrite(t *testing.T) {
	out := &failing{}
	clk := newFakeClock()
	inR, inW := io.Pipe()
	defer inW.Close()
	log := newEvents()
	agent := Status{ID: "agent/scout", State: StateDone}
	p := NewProgram(counter{log: log, status: []Status{{State: StateWorking}, agent}}, Options{
		In: inR, Out: out, Width: 20, Height: 4,
		Profile: colorprofile.TrueColor, StatusApp: "uah", clock: clk,
	})
	go func() { _, _ = p.Run(context.Background()) }()
	defer p.Quit()
	waitUntil(t, func() bool { return strings.Contains(out.String(), "id=agent/scout") })

	out.fail.Store(true)
	clk.advance(time.Second)
	p.Send(statusMsg{{State: StateIdle}})
	waitFailedRetry(t, out, clk)
	out.fail.Store(false)
	before := len(out.String())
	clk.advance(time.Second)
	waitUntil(t, func() bool { return strings.Contains(out.String()[before:], oscStatusClear("agent/scout")) })
	assert.Contains(t, out.String()[before:], oscStatus("uah", Status{State: StateIdle}))
}

// A frame that fails to write may have sent part of its reports: a record
// it added and the next frame leaves out is cleared.
func TestLoopClearsWhatAFailedWriteMaySend(t *testing.T) {
	out := &failing{}
	clk := newFakeClock()
	inR, inW := io.Pipe()
	defer inW.Close()
	log := newEvents()
	p := NewProgram(counter{log: log, status: []Status{{State: StateWorking}}}, Options{In: inR, Out: out, Width: 20, Height: 4,
		Profile: colorprofile.TrueColor, StatusApp: "uah", clock: clk})
	go func() { _, _ = p.Run(context.Background()) }()
	defer p.Quit()
	waitUntil(t, func() bool { return strings.Contains(out.String(), "state=working") })

	out.fail.Store(true)
	clk.advance(time.Second)
	p.Send(statusMsg{{State: StateWorking}, {ID: "agent/scout", State: StateDone}})
	waitFailedRetry(t, out, clk)
	out.fail.Store(false)
	p.Send(statusMsg{{State: StateIdle}})
	waitUntil(t, func() bool {
		clk.advance(time.Second) // the frame after the failed one waits for its timer
		return strings.Contains(out.String(), oscStatusClear("agent/scout"))
	})
}

// Leaving sends the last reports also when the terminal was already
// released, as while ctrl+g's editor runs.
func TestLeaveStatusWithTheTerminalReleased(t *testing.T) {
	out := &output{}
	p := &Program{opts: Options{StatusApp: "uah"}, term: &terminal{out: out}, status: map[string]string{"": "x", "agent/scout": "y"}}
	p.sendLeaveStatus("")
	assert.Equal(t, oscStatusClear("agent/scout")+oscStatus("uah", Status{State: StateIdle}), out.String())
}

// A clear removes the records beneath it too: a record kept beneath one
// the view leaves out is reported again after the clear.
func TestWriteStatusRestoresWhatAClearTook(t *testing.T) {
	p := &Program{opts: Options{StatusApp: "uah"}}
	own := Status{State: StateWorking}
	scout := Status{ID: "agent/scout", State: StateWorking}
	var pre strings.Builder
	p.status = p.writeStatus(&pre, []Status{own, {ID: "agent", State: StateWorking}, scout})

	pre.Reset()
	p.status = p.writeStatus(&pre, []Status{own, scout})
	assert.Equal(t, oscStatusClear("agent")+oscStatus("uah", scout), pre.String(), "the clear first, then the child again")

	pre.Reset()
	p.status = p.writeStatus(&pre, []Status{own, scout})
	assert.Empty(t, pre.String())
}

// waitFailedRetry waits until a frame failed to write and the loop armed
// the timer that tries it again, so the clock is advanced past that timer.
func waitFailedRetry(t *testing.T, out *failing, clk *fakeClock) {
	t.Helper()
	waitUntil(t, func() bool { return out.failed.Load() > 0 && clk.pending() > 0 })
}
