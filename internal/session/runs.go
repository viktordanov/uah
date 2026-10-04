package session

import (
	"fmt"
	"slices"
	"time"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/engine"
)

// startRun starts a run with the messages. The engine starts it on another
// goroutine and the loop gets evStarted.
func (s *Session) startRun(inputs []core.UserInput) {
	// Injected messages that waited for a run go first (Inject).
	s.noteFirstPrompt(inputs)
	inputs, s.held = slices.Concat(s.held, inputs), nil
	s.state = StateStarting
	s.markSent(inputs)
	s.noteGoalRun()
	req := s.settings.request(s.id, inputs)
	opts := engine.Options{
		ServiceTier: s.settings.ServiceTier, AdaptiveEffort: s.settings.AdaptiveEffort, Mode: s.settings.Mode, Compact: s.compactPending, CompactFocus: s.compactFocus, Clear: s.clearPending,
		Ask: s.askFunc(false), AskAnytime: s.askFunc(true), AskUser: s.askUserFunc(), Notify: s.notify, Inject: s.Inject, Stream: s.stream,
		Goal: s.goalToolFunc(), Settings: s.liveSettings, Grants: s.grants,
	}
	sink := func(e core.Event) { s.in <- evRun{event: e} }
	go func() {
		run, err := s.eng.Start(s.ctx, req, opts, sink)
		s.in <- evStarted{run: run, err: err, inputs: inputs}
	}()
}

// liveSettings are the session's settings now, which a run's subagents
// start with; any goroutine may call it.
func (s *Session) liveSettings() engine.LiveSettings {
	c := s.current.Load()

	return engine.LiveSettings{Model: c.Model, Effort: c.Effort, ServiceTier: c.ServiceTier, AdaptiveEffort: c.AdaptiveEffort, Mode: c.Mode}
}

// markSent records messages that went to the runner until it echoes them.
func (s *Session) markSent(inputs []core.UserInput) {
	ids := make([]string, 0, len(inputs))
	for _, in := range inputs {
		s.sent[in.ID] = true
		ids = append(ids, in.ID)
	}
	s.emit(InputSent{At: time.Now(), IDs: ids})
}

// onStarted handles a run that started or failed to start, and reports
// whether the session closed.
func (s *Session) onStarted(m evStarted) bool {
	if m.err != nil {
		ids := make([]string, 0, len(m.inputs))
		for _, in := range slices.Concat(m.inputs, s.startSteers) {
			delete(s.sent, in.ID)
			ids = append(ids, in.ID)
		}
		s.startSteers = nil
		s.emit(InputFailed{At: time.Now(), IDs: ids, Reason: m.err.Error()})
		s.emit(Notice{At: time.Now(), Level: LevelError, Message: m.err.Error()})
		s.interruptWhenStarted, s.restartAfterStop = false, false
		s.state = StateIdle
		if s.closeReply != nil {
			s.finishClose(nil)

			return true
		}
		s.emit(Idle{At: time.Now()})

		return false
	}
	s.run = m.run
	s.state = StateRunning
	go func() {
		result, err := m.run.Wait()
		s.in <- evEnded{result: result, err: err}
	}()
	steers := s.startSteers
	s.startSteers = nil
	for _, in := range steers {
		if err := s.run.Send(in); err != nil {
			s.queue = append(s.queue, in)

			continue
		}
		s.markSent([]core.UserInput{in})
		s.live = append(s.live, in)
	}
	if s.interruptWhenStarted || s.closeReply != nil {
		s.interruptWhenStarted = false
		s.interruptLive()
	}

	return false
}

// onRunEvent passes a run event on and notes what hooks and delivery need.
func (s *Session) onRunEvent(e core.Event) {
	s.emit(e)
	switch v := e.(type) {
	case core.RunStarted:
		s.hooks.runID = v.RunID
	case core.ToolCalled:
		s.hooks.tools[v.CallID] = v
	case core.TurnStarted, core.ModelResponded:
		_, s.modelBusy = v.(core.TurnStarted) // a model request is under way until its response
	case core.ToolFinished:
		s.postToolUse(v)
		delete(s.hooks.tools, v.CallID)
		s.sendAfterTool()
	}
	s.onGoalRunEvent(e)
	s.sendGoalSteer() // one held while the run started
	s.noteCompaction(e)
	if m, ok := e.(core.UserMessage); ok && s.sent[m.ID] {
		delete(s.sent, m.ID)
		s.emit(InputDelivered{At: time.Now(), ID: m.ID})
	}
}

// onEnded handles the end of a run and reports whether the session closed.
func (s *Session) onEnded(m evEnded) bool {
	s.run = nil
	s.noteLast(nil)
	s.declinePending(false)
	if m.err != nil {
		s.emit(Notice{At: time.Now(), Level: LevelError, Message: m.err.Error()})
	}
	userStopped := s.state == StateStopping && !s.restartAfterStop
	s.endGoalRun(m.result, m.err, userStopped)
	s.requeueUnread(userStopped && s.closeReply == nil) // a close keeps them queued too
	s.live, s.modelBusy = nil, false
	clear(s.afterTool) // they go out with the next run, or stay queued
	if len(s.sent) > 0 {
		ids := make([]string, 0, len(s.sent))
		for id := range s.sent {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		clear(s.sent)
		s.emit(InputFailed{At: time.Now(), IDs: ids, Reason: fmt.Sprintf("the run ended (%s) before the runner accepted them", m.result.Status)})
	}
	if s.closeReply != nil {
		s.finishClose(m.err)

		return true
	}
	s.state = StateIdle
	s.restartAfterStop = false
	clear(s.hooks.tools)
	if len(s.queue) > 0 && !userStopped {
		inputs := s.queue
		s.queue = nil
		s.startRun(inputs)

		return false
	}
	if len(s.hooks.checking) > 0 {
		return false // a message is on its way through its hooks
	}
	if !userStopped && s.checkStop() {
		return false
	}
	s.goIdle(userStopped)

	return false
}

// requeueUnread puts messages sent into the run that it never read back at
// the front of the queue. After a user stop they show as queued again.
func (s *Session) requeueUnread(userStopped bool) {
	var unread []core.UserInput
	for _, in := range s.live {
		if s.sent[in.ID] {
			delete(s.sent, in.ID)
			unread = append(unread, in)
		}
	}
	s.queue = slices.Concat(unread, s.queue)
	if userStopped {
		for _, in := range unread {
			s.emit(InputQueued{At: time.Now(), Input: in})
		}
	}
}

// finishClose runs SessionEnd hooks and answers the pending Close.
func (s *Session) finishClose(err error) {
	s.sessionEnd(s.ctx)
	s.state = StateClosed
	s.closeReply <- err
}

// emit sends on the ordered output stream. Only the loop goroutine calls it.
// evNotify is an engine event from outside a run (Options.Notify).
type evNotify struct{ event core.Event }

// notify hands an engine event to the loop; it drops it once the session
// has closed.
func (s *Session) notify(e core.Event) { s.post(evNotify{event: e}) }

func (s *Session) emit(e core.Event) {
	s.out <- e
}
