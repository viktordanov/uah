package session

import (
	"fmt"
	"time"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/hooks"
)

// Commands and internal events handled by the loop.
type (
	cmdSubmit struct {
		text string
		when When
	}
	cmdInterrupt   struct{}
	cmdSteerQueued struct{}
	cmdWithdraw    struct{ id string }
	cmdSettings    struct{ settings Settings }
	cmdClose       struct{ reply chan error }
	request        struct {
		cmd   any
		reply chan reply
	}
	reply struct {
		value any
		err   error
	}
	evStarted struct {
		run    engine.Run
		err    error
		inputs []core.UserInput
	}
	evRun   struct{ event core.Event }
	evEnded struct {
		result core.Result
		err    error
	}
	evHook struct {
		result hooks.Result
		at     time.Time
	}
	evStartChecked  struct{ decision hooks.Decision }
	evPromptChecked struct {
		id       string
		decision hooks.Decision
	}
	evStopChecked struct {
		gen      int
		decision hooks.Decision
	}
)

func call[T any](s *Session, cmd any) (T, error) {
	var zero T
	r := request{cmd: cmd, reply: make(chan reply, 1)}
	select {
	case s.in <- r:
	case <-s.done:
		return zero, ErrClosed
	}
	select {
	case res := <-r.reply:
		if res.err != nil {
			return zero, res.err
		}
		v, _ := res.value.(T)

		return v, nil
	case <-s.done:
		return zero, ErrClosed
	}
}

func (s *Session) loop() { //nolint:gocyclo // a dispatch switch over a closed set; see docs/documentation/architecture.md
	defer close(s.done)
	defer close(s.out)
	defer s.stop()
	defer s.stopShells()
	if s.hooks.jobs != nil {
		defer close(s.hooks.jobs)
	}
	defer s.saveQueue() // what a close left queued
	for msg := range s.in {
		switch m := msg.(type) {
		case request:
			value, err := s.handle(m.cmd)
			m.reply <- reply{value: value, err: err}
		case cmdClose:
			if s.run == nil && s.state != StateStarting {
				s.sessionEnd(s.ctx)
				s.state = StateClosed
				m.reply <- nil

				return
			}
			s.closeReply = m.reply
			s.interruptLive()
		case evStarted:
			if s.onStarted(m) {
				return
			}
		case evRun:
			s.onRunEvent(m.event)
		case evDo:
			m()
		case evNotify:
			s.emit(m.event)
		case evGrant:
			s.onGrant(m.grant)
		case evEnded:
			if s.onEnded(m) {
				return
			}
		case evHook:
			r := m.result
			s.emit(HookRan{
				At: m.at, Event: string(r.Hook.Event), Command: r.Hook.Command, Source: string(r.Hook.Source),
				Outcome: string(r.Outcome), Reason: r.Reason, Duration: r.Duration,
			})
		case evStartChecked:
			s.showMessages(m.decision)
			s.hooks.startContext = m.decision.Context
		case evPromptChecked:
			s.onPromptChecked(m)
		case evStopChecked:
			s.onStopChecked(m)
		case cmdAsk:
			s.onAsk(m)
		case cmdAskGone:
			s.answer(m.id, m.answer)
		case cmdQuestions:
			s.onQuestions(m)
		case cmdQuestionsGone:
			s.dropQuestions(m.id)
		}
		s.saveQueue()
	}
}

func (s *Session) handle(cmd any) (any, error) {
	if s.closeReply != nil {
		return nil, ErrClosed
	}
	switch c := cmd.(type) {
	case cmdSubmit:
		return s.onSubmit(c), nil
	case cmdInterrupt:
		s.pauseGoalForInterrupt()
		s.restartAfterStop = false
		s.stopShells()
		s.stopReview()
		s.interruptLive()

		return struct{}{}, nil
	case cmdSteerQueued:
		return s.onSteerQueued(), nil
	case cmdWithdraw:
		return s.onWithdraw(c.id), nil
	case cmdSettings:
		return s.onSettings(c.settings), nil
	case cmdCompact:
		s.onCompact(c.focus)

		return struct{}{}, nil
	case cmdClear:
		s.onClear()

		return struct{}{}, nil
	case cmdRewind:
		return struct{}{}, s.onRewind(c.id)
	case cmdResolve:
		return struct{}{}, s.onResolve(c)
	case cmdAnswer:
		return struct{}{}, s.onAnswer(c)
	case cmdShell:
		return s.onShell(c), nil
	case cmdReview:
		return s.onReview(c)
	case cmdGoal:
		return s.onGoal(c)
	case cmdGoalTool:
		return s.onGoalTool(c)
	}

	return nil, fmt.Errorf("unknown session command %T", cmd)
}
