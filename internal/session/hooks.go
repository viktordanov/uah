package session

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/hooks"
	"github.com/viktordanov/uah/internal/patch"
)

// maxStopContinuations stops Stop hooks from keeping the agent going forever.
const maxStopContinuations = 5

// hookState is what the session keeps for its hooks. The loop goroutine owns
// it; hook jobs only read runner.
type hookState struct {
	runner         *hooks.Runner
	transcriptPath string
	resumed        bool
	// jobs feeds the hook worker; it is nil without hooks.
	jobs chan func()
	// checking are messages waiting for their UserPromptSubmit hooks, in order.
	checking []pendingInput
	// startContext is SessionStart hook context for the first message.
	startContext []string
	tools        map[string]core.ToolCalled
	runID        string
	stopGen      int
	stopStreak   int
}

type pendingInput struct {
	input core.UserInput
	steer bool
}

// startHooks starts the hook worker, reports hook results to the loop, and
// runs SessionStart hooks. It does nothing without hooks.
func (s *Session) startHooks(resumed bool) {
	if s.hooks.runner == nil {
		return
	}
	s.hooks.jobs = make(chan func(), eventBuffer)
	go s.work()
	s.hooks.runner.OnResult(func(r hooks.Result) { s.post(evHook{result: r, at: time.Now()}) })
	if s.hooks.runner.Has(hooks.SessionStart, "") {
		in := s.hookInput(hooks.SessionStart)
		in.Source = "startup"
		if resumed {
			in.Source = "resume"
		}
		s.hooks.jobs <- func() { s.post(evStartChecked{decision: s.hooks.runner.Run(s.ctx, in)}) }
	}
}

// work runs hook jobs one at a time, in order.
func (s *Session) work() {
	for job := range s.hooks.jobs {
		job()
	}
}

// post hands a result to the loop, or drops it once the session is closed.
func (s *Session) post(msg any) {
	select {
	case s.in <- msg:
	case <-s.done:
	}
}

// hookInput is the payload fields every hook gets.
func (s *Session) hookInput(event hooks.Event) hooks.Input {
	return hooks.Input{
		Event: event, SessionID: s.id, RunID: s.hooks.runID, Cwd: s.settings.Workspace,
		Model: s.settings.Model, Effort: s.settings.Effort, TranscriptPath: s.hooks.transcriptPath,
	}
}

// onPromptChecked sends a message its hooks allowed, with any added context.
func (s *Session) onPromptChecked(m evPromptChecked) {
	i := slices.IndexFunc(s.hooks.checking, func(p pendingInput) bool { return p.input.ID == m.id })
	if i < 0 {
		return // withdrawn while its hooks ran
	}
	p := s.hooks.checking[i]
	s.hooks.checking = slices.Delete(s.hooks.checking, i, i+1)
	s.showMessages(m.decision)
	if m.decision.Block {
		s.emit(InputFailed{At: time.Now(), IDs: []string{p.input.ID}, Reason: "blocked by a UserPromptSubmit hook: " + m.decision.Reason})
		if s.state == StateIdle && len(s.hooks.checking) == 0 && s.closeReply == nil {
			s.emit(Idle{At: time.Now()})
		}

		return
	}
	extra := slices.Concat(s.hooks.startContext, m.decision.Context)
	s.hooks.startContext = nil
	if len(extra) > 0 {
		p.input.Text += "\n\n" + strings.Join(extra, "\n\n")
	}
	s.dispatch(p.input, p.steer)
}

// showMessages shows the messages hooks returned as notices.
func (s *Session) showMessages(d hooks.Decision) {
	for _, msg := range d.Messages {
		s.emit(Notice{At: time.Now(), Level: LevelInfo, Message: msg})
	}
}

// checkStop runs Stop hooks after a run ends and reports whether there were
// any; the loop then gets evStopChecked.
func (s *Session) checkStop() bool {
	if !s.hooks.runner.Has(hooks.Stop, "") {
		return false
	}
	s.hooks.stopGen++
	gen := s.hooks.stopGen
	in := s.hookInput(hooks.Stop)
	in.StopHookActive = s.hooks.stopStreak > 0
	s.hooks.jobs <- func() { s.post(evStopChecked{gen: gen, decision: s.hooks.runner.Run(s.ctx, in)}) }

	return true
}

// onStopChecked continues the agent when a Stop hook blocked, and otherwise
// reports the session idle.
func (s *Session) onStopChecked(m evStopChecked) {
	if m.gen != s.hooks.stopGen || s.state != StateIdle || s.closeReply != nil {
		return // a message arrived meanwhile
	}
	s.showMessages(m.decision)
	reason := strings.TrimSpace(m.decision.Reason)
	if m.decision.Block && reason != "" {
		if s.hooks.stopStreak >= maxStopContinuations {
			s.emit(Notice{At: time.Now(), Level: LevelWarning, Message: fmt.Sprintf("Stop hooks continued the agent %d times in a row; stopping", s.hooks.stopStreak)})
		} else {
			s.hooks.stopStreak++
			input := core.UserInput{ID: uuid.NewString(), Text: reason}
			s.emit(InputQueued{At: time.Now(), Input: input})
			s.startRun([]core.UserInput{input})

			return
		}
	}
	s.hooks.stopStreak = 0
	s.goIdle(false)
}

// postToolUse runs PostToolUse hooks for a finished tool; they only observe.
func (s *Session) postToolUse(f core.ToolFinished) {
	called, ok := s.hooks.tools[f.CallID]
	if !ok || !s.hooks.runner.Has(hooks.PostToolUse, called.Name) || !s.policy.Allows(called.Name) {
		return // a call the tool policy refused never ran, so no hook sees it
	}
	in := s.hookInput(hooks.PostToolUse)
	in.ToolName, in.ToolUseID = called.Name, f.CallID
	switch {
	case called.Name == patch.ToolName:
		in.ToolInput = patch.HookInput(called.Arguments) // Codex's {"command": patch}
	case json.Valid([]byte(called.Arguments)):
		in.ToolInput = json.RawMessage(called.Arguments)
	}
	in.ToolResponse = &hooks.ToolResponse{Success: f.OK, Detail: f.Detail, Stdout: f.OutPath, Stderr: f.ErrPath}
	s.hooks.jobs <- func() { s.hooks.runner.Run(s.ctx, in) }
}

// sessionEnd runs SessionEnd hooks; each gets at most a second.
func (s *Session) sessionEnd(ctx context.Context) {
	if !s.hooks.runner.Has(hooks.SessionEnd, "") {
		return
	}
	in := s.hookInput(hooks.SessionEnd)
	in.Reason = "exit"
	s.hooks.runner.Run(context.WithoutCancel(ctx), in) // the session is closing; its context may be done
}
