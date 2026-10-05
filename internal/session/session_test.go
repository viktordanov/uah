package session_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/approval"
	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/session"
)

// fakeCaps are what a fake run takes live; a change it does not take
// fails with errNotLive, as a real run's does once it stopped, and the
// session applies it from the next run. SlowStop ignores an interrupt:
// only a kill ends the run.
type fakeCaps struct{ LiveInput, LiveEffort, LiveMode, LiveAdaptive, SlowStop bool }

var errNotLive = errors.New("the run takes no live changes")

// fakeEngine scripts runs: each run echoes its messages (unless noEcho) and
// ends when the test finishes it or it is interrupted.
type fakeEngine struct {
	// gate, when set, holds Start until it is closed.
	gate     chan struct{}
	caps     fakeCaps
	noEcho   bool
	startErr error
	started  chan *fakeRun
}

func newFakeEngine(caps fakeCaps) *fakeEngine {
	return &fakeEngine{caps: caps, started: make(chan *fakeRun, 16)}
}

func (e *fakeEngine) Name() string   { return "fake" }
func (e *fakeEngine) Priority() bool { return false }

func (e *fakeEngine) Start(ctx context.Context, req core.Request, opts engine.Options, sink core.Sink) (engine.Run, error) {
	if e.gate != nil {
		<-e.gate
	}
	if e.startErr != nil {
		return nil, e.startErr
	}
	r := &fakeRun{ctx: ctx, req: req, opts: opts, sink: sink, caps: e.caps, end: make(chan core.Status, 1), done: make(chan struct{})}
	sink(core.RunStarted{At: time.Now(), RunID: fmt.Sprintf("run-%d", len(e.started)), SessionID: req.SessionID})
	if !e.noEcho {
		for _, m := range req.Messages {
			sink(core.UserMessage{At: time.Now(), ID: m.ID, Text: m.Text})
		}
	}
	go r.wait()
	e.started <- r

	return r, nil
}

type fakeRun struct {
	ctx    context.Context
	req    core.Request
	opts   engine.Options
	sink   core.Sink
	caps   fakeCaps
	end    chan core.Status
	done   chan struct{}
	once   sync.Once
	result core.Result
	killed atomic.Bool
	sent   []core.UserInput
	effort string
	mode   approval.Mode
	// adaptive is the adaptive effort the run took live.
	adaptive string
	// unread accepts live messages without ever reading them, like a run
	// that went idle just as they arrived.
	unread bool
}

func (r *fakeRun) wait() {
	status := <-r.end
	r.result = core.Result{Request: r.req, Status: status}
	r.sink(core.RunFinished{At: time.Now(), Result: r.result})
	close(r.done)
}

func (r *fakeRun) finish(status core.Status) { r.once.Do(func() { r.end <- status }) }

func (r *fakeRun) Send(in core.UserInput) error {
	if !r.caps.LiveInput {
		return errNotLive
	}
	r.sent = append(r.sent, in)
	switch {
	case r.unread:
	case in.Role == core.RoleDeveloper:
		r.sink(core.DeveloperMessage{At: time.Now(), ID: in.ID, Text: in.Text})
	default:
		r.sink(core.UserMessage{At: time.Now(), ID: in.ID, Text: in.Text})
	}

	return nil
}

func (r *fakeRun) SetEffort(e string) error {
	if !r.caps.LiveEffort {
		return errNotLive
	}
	r.effort = e

	return nil
}

func (r *fakeRun) SetModel(string) error       { return errNotLive }
func (r *fakeRun) SetServiceTier(string) error { return errNotLive }

func (r *fakeRun) SetAdaptiveEffort(v string) error {
	if !r.caps.LiveAdaptive {
		return errNotLive
	}
	r.adaptive = v

	return nil
}

func (r *fakeRun) SetMode(m approval.Mode) error {
	if !r.caps.LiveMode {
		return errNotLive
	}
	r.mode = m

	return nil
}
func (r *fakeRun) Compact(string) error { return errNotLive }
func (r *fakeRun) Clear() error         { return errNotLive }
func (r *fakeRun) Kill()                { r.killed.Store(true); r.finish(core.StatusInterrupted) }

func (r *fakeRun) Interrupt() {
	if !r.caps.SlowStop {
		r.finish(core.StatusInterrupted)
	}
}

func (r *fakeRun) Wait() (core.Result, error) {
	<-r.done

	return r.result, nil
}

type harness struct {
	t      *testing.T
	eng    *fakeEngine
	s      *session.Session
	events []core.Event
}

func newHarness(t *testing.T, caps fakeCaps) *harness {
	t.Helper()
	eng := newFakeEngine(caps)
	s, err := session.Open(context.Background(), eng, session.Options{Settings: settings()})
	require.NoError(t, err)
	h := &harness{t: t, eng: eng, s: s}
	t.Cleanup(func() { _ = s.Close() })
	h.until(isType[session.SessionOpened])

	return h
}

func settings() session.Settings {
	return session.Settings{Provider: "openai-codex", Model: "gpt-6-sol", Effort: "high", Workspace: "/workspace"}
}

// until reads events until one matches, and returns it.
func (h *harness) until(match func(core.Event) bool) core.Event {
	h.t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case e, ok := <-h.s.Events():
			require.True(h.t, ok, "events closed before a match")
			h.events = append(h.events, e)
			if match(e) {
				return e
			}
		case <-timeout:
			h.t.Fatalf("no matching event; saw %d events", len(h.events))
		}
	}
}

func (h *harness) nextRun() *fakeRun {
	h.t.Helper()
	select {
	case r := <-h.eng.started:
		return r
	case <-time.After(5 * time.Second):
		h.t.Fatal("no run started")

		return nil
	}
}

func isType[T core.Event](e core.Event) bool { _, ok := e.(T); return ok }

func typesOf(events []core.Event) []string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, fmt.Sprintf("%T", e))
	}

	return out
}

func texts(msgs []core.UserInput) []string {
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, m.Text)
	}

	return out
}

func TestSession_SubmitWhenIdleStartsARun(t *testing.T) {
	h := newHarness(t, fakeCaps{})

	in, err := h.s.Submit("hello")
	require.NoError(t, err)
	run := h.nextRun()
	h.until(isType[session.InputDelivered])
	run.finish(core.StatusOK)
	h.until(isType[session.Idle])

	assert.Equal(t, []string{"hello"}, texts(run.req.Messages))
	assert.Equal(t, in.ID, run.req.Messages[0].ID)
	assert.Equal(t, h.s.ID(), run.req.SessionID)
	assert.Equal(t, []string{
		"session.SessionOpened", "session.InputQueued", "session.InputSent", "core.RunStarted",
		"core.UserMessage", "session.InputDelivered", "core.RunFinished", "session.Idle",
	}, typesOf(h.events))
}

// TestSession_RunHasNoDeadline pins that a turn runs as long as it needs:
// the engine gets no timeout and a context with no deadline.
func TestSession_RunHasNoDeadline(t *testing.T) {
	h := newHarness(t, fakeCaps{})

	_, err := h.s.Submit("hello")
	require.NoError(t, err)
	run := h.nextRun()

	assert.Zero(t, run.req.Timeout)
	_, ok := run.ctx.Deadline()
	assert.False(t, ok, "the run's context has a deadline")
	run.finish(core.StatusOK)
	h.until(isType[session.Idle])
}

func TestSession_QueueWhileRunning(t *testing.T) {
	h := newHarness(t, fakeCaps{})
	_, err := h.s.Submit("first")
	require.NoError(t, err)
	first := h.nextRun()

	_, err = h.s.Submit("second")
	require.NoError(t, err)
	_, err = h.s.Submit("third")
	require.NoError(t, err)
	first.finish(core.StatusOK)
	next := h.nextRun()

	assert.Equal(t, []string{"second", "third"}, texts(next.req.Messages), "queued messages go out together, in order")
	next.finish(core.StatusOK)
	h.until(isType[session.Idle])
}

func TestSession_InterruptKeepsTheQueue(t *testing.T) {
	h := newHarness(t, fakeCaps{})
	_, err := h.s.Submit("work")
	require.NoError(t, err)
	run := h.nextRun()
	_, err = h.s.Submit("later")
	require.NoError(t, err)

	require.NoError(t, h.s.Interrupt())
	finished := h.until(isType[core.RunFinished]).(core.RunFinished)
	h.until(isType[session.Idle])

	assert.Equal(t, core.StatusInterrupted, finished.Result.Status)
	assert.Equal(t, core.StatusInterrupted, run.result.Status)
	select {
	case r := <-h.eng.started:
		t.Fatalf("no run should start after an interrupt, got %v", texts(r.req.Messages))
	case <-time.After(100 * time.Millisecond):
	}

	_, err = h.s.Submit("now")
	require.NoError(t, err)
	next := h.nextRun()
	assert.Equal(t, []string{"later", "now"}, texts(next.req.Messages), "the kept queue goes out before the new message")
	next.finish(core.StatusOK)
}

// TestSession_SecondInterruptKills: an interrupt while the run is already
// stopping forces the stop.
func TestSession_SecondInterruptKills(t *testing.T) {
	h := newHarness(t, fakeCaps{SlowStop: true})
	_, err := h.s.Submit("work")
	require.NoError(t, err)
	run := h.nextRun()

	require.NoError(t, h.s.Interrupt())
	assert.False(t, run.killed.Load(), "the first interrupt is graceful")
	require.NoError(t, h.s.Interrupt())
	h.until(isType[core.RunFinished])

	assert.True(t, run.killed.Load())
}

func TestSession_SteerNow(t *testing.T) {
	t.Run("without live input: interrupt and restart with the queue", func(t *testing.T) {
		h := newHarness(t, fakeCaps{})
		_, err := h.s.Submit("work")
		require.NoError(t, err)
		first := h.nextRun()
		_, err = h.s.Submit("queued")
		require.NoError(t, err)

		_, err = h.s.SteerNow("change of plan")
		require.NoError(t, err)
		next := h.nextRun()

		assert.Equal(t, core.StatusInterrupted, first.result.Status)
		assert.Equal(t, []string{"queued", "change of plan"}, texts(next.req.Messages))
		next.finish(core.StatusOK)
	})

	t.Run("live input: the message reaches the running agent", func(t *testing.T) {
		h := newHarness(t, fakeCaps{LiveInput: true})
		_, err := h.s.Submit("work")
		require.NoError(t, err)
		run := h.nextRun()
		h.until(isType[session.InputDelivered])

		in, err := h.s.SteerNow("look at the tests too")
		require.NoError(t, err)
		delivered := h.until(isType[session.InputDelivered]).(session.InputDelivered)

		assert.Equal(t, in.ID, delivered.ID)
		assert.Equal(t, []string{"look at the tests too"}, texts(run.sent))
		run.finish(core.StatusOK)
		h.until(isType[session.Idle])
	})
}

func TestSession_Failures(t *testing.T) {
	t.Run("a run that cannot start fails its messages", func(t *testing.T) {
		h := newHarness(t, fakeCaps{})
		h.eng.startErr = errors.New("preflight blocked the run: run codex login")

		in, err := h.s.Submit("hello")
		require.NoError(t, err)
		failed := h.until(isType[session.InputFailed]).(session.InputFailed)
		notice := h.until(isType[session.Notice]).(session.Notice)
		h.until(isType[session.Idle])

		assert.Equal(t, []string{in.ID}, failed.IDs)
		assert.Contains(t, failed.Reason, "run codex login")
		assert.Equal(t, "error", notice.Level)
	})

	t.Run("messages the runner never accepted fail when the run ends", func(t *testing.T) {
		h := newHarness(t, fakeCaps{})
		h.eng.noEcho = true

		in, err := h.s.Submit("hello")
		require.NoError(t, err)
		h.nextRun().finish(core.StatusFailed)
		failed := h.until(isType[session.InputFailed]).(session.InputFailed)

		assert.Equal(t, []string{in.ID}, failed.IDs)
		assert.Contains(t, failed.Reason, "error")
	})

	t.Run("empty messages are rejected", func(t *testing.T) {
		h := newHarness(t, fakeCaps{})

		_, err := h.s.Submit("  ")

		require.Error(t, err)
	})
}

func TestSession_Withdraw(t *testing.T) {
	h := newHarness(t, fakeCaps{})
	_, err := h.s.Submit("work")
	require.NoError(t, err)
	run := h.nextRun()
	queued, err := h.s.Submit("never mind")
	require.NoError(t, err)

	ok, err := h.s.Withdraw(queued.ID)
	require.NoError(t, err)
	again, err := h.s.Withdraw(queued.ID)
	require.NoError(t, err)
	run.finish(core.StatusOK)
	h.until(isType[session.Idle])

	assert.True(t, ok)
	assert.False(t, again)
	select {
	case r := <-h.eng.started:
		t.Fatalf("a withdrawn message must not start a run, got %v", texts(r.req.Messages))
	case <-time.After(100 * time.Millisecond):
	}
}

func TestSession_SetSettings(t *testing.T) {
	t.Run("without live settings: changes apply at the next run", func(t *testing.T) {
		h := newHarness(t, fakeCaps{})
		_, err := h.s.Submit("work")
		require.NoError(t, err)
		run := h.nextRun()
		next := settings()
		next.Effort = "low"

		applied, err := h.s.SetSettings(next)
		require.NoError(t, err)
		_, err = h.s.Submit("more")
		require.NoError(t, err)
		run.finish(core.StatusOK)
		second := h.nextRun()

		assert.Equal(t, session.AppliedNextRun, applied)
		assert.Equal(t, "high", run.req.Effort)
		assert.Equal(t, "low", second.req.Effort)
		second.finish(core.StatusOK)
	})

	t.Run("live effort applies now", func(t *testing.T) {
		h := newHarness(t, fakeCaps{LiveEffort: true})
		_, err := h.s.Submit("work")
		require.NoError(t, err)
		run := h.nextRun()
		next := settings()
		next.Effort = "max"

		applied, err := h.s.SetSettings(next)
		require.NoError(t, err)

		assert.Equal(t, session.AppliedLive, applied)
		assert.Equal(t, "max", run.effort)
		run.finish(core.StatusOK)
	})

	t.Run("a failed live change does not stop the ones after it", func(t *testing.T) {
		h := newHarness(t, fakeCaps{LiveAdaptive: true})
		_, err := h.s.Submit("work")
		require.NoError(t, err)
		run := h.nextRun()
		next := settings()
		next.ServiceTier = "priority" // the run refuses it
		next.AdaptiveEffort = session.AdaptiveOneStep

		applied, err := h.s.SetSettings(next)
		require.NoError(t, err)

		assert.Equal(t, session.AppliedNextRun, applied, "not all of it applied live")
		assert.Equal(t, session.AdaptiveOneStep, run.adaptive, "adaptive effort still reached the run")
		run.finish(core.StatusOK)
	})

	t.Run("invalid settings are rejected", func(t *testing.T) {
		h := newHarness(t, fakeCaps{})
		bad := settings()
		bad.Effort = "huge"

		_, err := h.s.SetSettings(bad)

		require.Error(t, err)
	})

	t.Run("yolo mode needs --yolo", func(t *testing.T) {
		h := newHarness(t, fakeCaps{})
		assert.False(t, h.s.Yolo())

		_, err := h.s.SetSettings(settings().WithMode(approval.ModeYolo))

		require.ErrorContains(t, err, "needs --yolo")
		_, err = session.Open(context.Background(), h.eng, session.Options{Settings: settings().WithMode(approval.ModeYolo)})
		require.ErrorContains(t, err, "needs --yolo")

		s, err := session.Open(context.Background(), h.eng, session.Options{Settings: settings().WithMode(approval.ModeYolo), Yolo: true})
		require.NoError(t, err)
		t.Cleanup(func() { _ = s.Close() })
		assert.True(t, s.Yolo())
		_, err = s.SetSettings(settings().WithMode(approval.ModeReadOnly))
		require.NoError(t, err)
		_, err = s.SetSettings(settings().WithMode(approval.ModeYolo))
		require.NoError(t, err, "back to yolo with --yolo")
	})
}

func TestSession_CloseInterruptsTheLiveRun(t *testing.T) {
	h := newHarness(t, fakeCaps{})
	_, err := h.s.Submit("work")
	require.NoError(t, err)
	run := h.nextRun()

	require.NoError(t, h.s.Close())

	assert.Equal(t, core.StatusInterrupted, run.result.Status)
	for range h.s.Events() { // drain until closed
	}
	_, err = h.s.Submit("after close")
	require.ErrorIs(t, err, session.ErrClosed)
}

func TestSession_RequeuesLiveMessagesTheRunNeverRead(t *testing.T) {
	h := newHarness(t, fakeCaps{LiveInput: true})
	_, err := h.s.Submit("first")
	require.NoError(t, err)
	run := <-h.eng.started
	run.unread = true
	steer, err := h.s.SteerNow("also this")
	require.NoError(t, err)
	h.until(isType[session.InputSent])
	run.finish(core.StatusOK)

	next := <-h.eng.started
	require.Len(t, next.req.Messages, 1)
	assert.Equal(t, steer.ID, next.req.Messages[0].ID, "the next run carries the unread message")
	h.until(func(e core.Event) bool {
		d, ok := e.(session.InputDelivered)

		return ok && d.ID == steer.ID
	})
	next.finish(core.StatusOK)
	h.until(isType[session.Idle])
	for _, e := range h.events {
		assert.NotEqual(t, "session.InputFailed", fmt.Sprintf("%T", e))
	}
}

func TestSession_SteerWhileStartingGoesLive(t *testing.T) {
	h := newHarness(t, fakeCaps{LiveInput: true})
	h.eng.gate = make(chan struct{})
	_, err := h.s.Submit("work")
	require.NoError(t, err)
	steer, err := h.s.SteerNow("and this")
	require.NoError(t, err)
	close(h.eng.gate)

	run := h.nextRun()
	h.until(func(e core.Event) bool {
		d, ok := e.(session.InputDelivered)

		return ok && d.ID == steer.ID
	})
	assert.Equal(t, []string{"and this"}, texts(run.sent), "sent live once the run started, not by a restart")
	assert.Equal(t, core.Status(""), run.result.Status, "the starting run was not interrupted")
	run.finish(core.StatusOK)
	h.until(isType[session.Idle])
	assert.Empty(t, h.eng.started, "no second run")
}
