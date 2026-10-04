package session

import (
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/hooks"
)

// onSubmit accepts a message and sends it through its UserPromptSubmit hooks,
// or dispatches it directly when there are none to wait for.
func (s *Session) onSubmit(c cmdSubmit) core.UserInput {
	input := core.UserInput{ID: uuid.NewString(), Text: c.text}
	s.afterTool[input.ID] = c.when == SendAfterTool
	s.emit(InputQueued{At: time.Now(), Input: input, AfterTool: s.afterTool[input.ID]})
	s.hooks.stopStreak = 0
	s.hooks.stopGen++ // a pending Stop hook no longer decides anything
	if s.hooks.jobs != nil && (s.hooks.runner.Has(hooks.UserPromptSubmit, "") || s.hooks.runner.Has(hooks.SessionStart, "")) {
		// Through the worker even without UserPromptSubmit hooks, so the
		// SessionStart context is ready and the order is kept.
		s.hooks.checking = append(s.hooks.checking, pendingInput{input: input, steer: c.when == SendNow})
		in := s.hookInput(hooks.UserPromptSubmit)
		in.Prompt = input.Text
		s.hooks.jobs <- func() {
			var d hooks.Decision
			if s.hooks.runner.Has(hooks.UserPromptSubmit, "") {
				d = s.hooks.runner.Run(s.ctx, in)
			}
			s.post(evPromptChecked{id: input.ID, decision: d})
		}

		return input
	}
	s.dispatch(input, c.when == SendNow)

	return input
}

// onWithdraw takes back a message that is waiting for its hooks or queued,
// and reports whether it found one.
func (s *Session) onWithdraw(id string) bool {
	if i := slices.IndexFunc(s.hooks.checking, func(p pendingInput) bool { return p.input.ID == id }); i >= 0 {
		s.hooks.checking = slices.Delete(s.hooks.checking, i, i+1)
		s.emit(InputWithdrawn{At: time.Now(), ID: id})

		return true
	}
	i := slices.IndexFunc(s.queue, func(in core.UserInput) bool { return in.ID == id })
	if i < 0 {
		return false
	}
	s.queue = slices.Delete(s.queue, i, i+1)
	s.emit(InputWithdrawn{At: time.Now(), ID: id})

	return true
}

// onSteerQueued sends every queued message now, in order, as a steer
// sends one, and reports how many it sent. Messages still waiting for
// their hooks go the same way once the hooks allow them. While idle (the
// queue a user interrupt kept) the messages start a run together.
func (s *Session) onSteerQueued() int {
	for i := range s.hooks.checking {
		s.hooks.checking[i].steer = true
	}
	queued := s.queue
	s.queue = nil
	if s.state == StateIdle {
		if len(queued) > 0 {
			s.startRun(queued)
		}

		return len(queued) + len(s.hooks.checking)
	}
	for _, in := range queued {
		s.dispatch(in, true)
	}

	return len(queued) + len(s.hooks.checking)
}

// dispatch starts a run with the message, sends it live, or queues it.
func (s *Session) dispatch(input core.UserInput, steer bool) {
	switch s.state {
	case StateIdle:
		// Messages left queued by an interrupt go out first, in order.
		inputs := slices.Concat(s.queue, []core.UserInput{input})
		s.queue = nil
		s.startRun(inputs)
	case StateRunning:
		if steer {
			if err := s.run.Send(input); err == nil {
				s.markSent([]core.UserInput{input})
				s.live = append(s.live, input)

				return
			}
		}
		s.queue = append(s.queue, input)
		if steer {
			s.restartAfterStop = true
			s.interruptLive()
		}
		s.sendAfterTool()
	case StateStarting:
		if steer {
			s.startSteers = append(s.startSteers, input) // sent live once the run starts
			return
		}
		s.queue = append(s.queue, input)
	case StateStopping:
		s.queue = append(s.queue, input)
		if steer {
			s.restartAfterStop = true
		}
	case StateClosed:
	}
}

// sendAfterTool steers the queued SendAfterTool messages into the live run,
// in order, once no model response or tool call is under way.
func (s *Session) sendAfterTool() {
	if s.state != StateRunning || s.modelBusy || len(s.hooks.tools) > 0 {
		return
	}
	steer := slices.DeleteFunc(slices.Clone(s.queue), func(in core.UserInput) bool { return !s.afterTool[in.ID] })
	s.queue = slices.DeleteFunc(s.queue, func(in core.UserInput) bool { return s.afterTool[in.ID] })
	for _, in := range steer {
		delete(s.afterTool, in.ID)
		s.dispatch(in, true)
	}
}

// onSettings applies new settings to the live run when it can, and otherwise
// from the next run.
func (s *Session) onSettings(next Settings) Applied {
	prev := s.settings
	s.settings = next
	s.current.Store(&next) // before the live run hears of it: a child it spawns from now on starts with next
	applied := AppliedNextRun
	if s.state == StateRunning && s.run != nil {
		onlyLiveFields := next.Provider == prev.Provider && next.Workspace == prev.Workspace && next.BaseURL == prev.BaseURL
		changed := next.Effort != prev.Effort || next.Model != prev.Model || next.ServiceTier != prev.ServiceTier ||
			next.AdaptiveEffort != prev.AdaptiveEffort || next.Mode != prev.Mode
		if s.setLive(prev, next) && onlyLiveFields && changed {
			applied = AppliedLive
		}
	}
	s.emit(SettingsChanged{At: time.Now(), Settings: next, Applied: applied})
	s.saveSettings(next)

	return applied
}

// setLive passes the fields that changed to the live run, each even when
// one before it failed, and reports whether it took them all.
func (s *Session) setLive(prev, next Settings) bool {
	live := true
	set := func(changed bool, apply func() error) {
		if changed && apply() != nil {
			live = false
		}
	}
	set(next.Effort != prev.Effort, func() error { return s.run.SetEffort(next.Effort) })
	set(next.Model != prev.Model, func() error { return s.run.SetModel(next.Model) })
	set(next.ServiceTier != prev.ServiceTier, func() error { return s.run.SetServiceTier(next.ServiceTier) })
	set(next.AdaptiveEffort != prev.AdaptiveEffort, func() error { return s.run.SetAdaptiveEffort(next.AdaptiveEffort) })
	set(next.Mode != prev.Mode, func() error { return s.run.SetMode(next.Mode) })

	return live
}

// interruptLive stops the live run, or the run that is starting.
func (s *Session) interruptLive() {
	s.declinePending(true) // a run waiting for the user cannot stop
	switch s.state {
	case StateRunning:
		s.state = StateStopping
		s.run.Interrupt()
	case StateStarting:
		s.interruptWhenStarted = true
	case StateStopping: // a second interrupt forces the stop
		s.run.Kill()
	case StateIdle, StateClosed:
	}
}
