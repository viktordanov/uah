package agents

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/session"
)

// ReviewLimits bound one /review. A zero field sets no limit.
type ReviewLimits struct {
	// Time is the review's wall-clock limit, and Tokens the input and
	// output tokens of its model responses. When either runs out, the
	// reviewer's run stops, and one last turn, with no tools, asks it to
	// answer with what it has.
	Time   time.Duration
	Tokens int64
	// Command is the limit on each of the reviewer's commands.
	Command time.Duration
}

// The review's command limit, by default: twice the wake policy's
// 5-minute hold, and longer than any command of the reviews uah ran
// before (the longest, 7 minutes). The time and token limits are off by
// default until the owner decides (docs/design/review.md#limits); about
// three times the longest review before would be 30 minutes and 3,000,000
// tokens.
const DefaultReviewCommand = 10 * time.Minute

// reviewLastTurn bounds the last turn a limit gives the reviewer.
const reviewLastTurn = 3 * time.Minute

// lastTurnLimit bounds the last turn: reviewLastTurn, or the time limit
// when that is shorter.
func lastTurnLimit(limits ReviewLimits) time.Duration {
	if limits.Time > 0 {
		return min(limits.Time, reviewLastTurn)
	}

	return reviewLastTurn
}

// lastTurnMessage asks the reviewer for its answer when a limit ran out.
func lastTurnMessage(limit session.ReviewLimit, limits ReviewLimits) string {
	what := fmt.Sprintf("its time limit of %s", limits.Time)
	if limit == session.ReviewTokenLimit {
		what = fmt.Sprintf("its limit of %d tokens", limits.Tokens)
	}

	return "This review reached " + what + ", and its commands were stopped. " +
		"Finish now: give your final answer in the required JSON format, with the findings you have so far " +
		"and, in overall_explanation, what you could not check. Tools are no longer available."
}

// The phases of a review: the reviewer works; a limit ran out and its run
// is stopping; its last turn runs.
const (
	reviewWorking = iota
	reviewStopping
	reviewLastTurnPhase
)

// awaitReview follows the reviewer's session until it answers, and
// returns its first answer. An answer ends the review at once: a call
// still running is stopped, as an interrupt stops it, so no heartbeat or
// late result wakes the reviewer again. When ctx ends first, it
// interrupts the run and waits for it to stop. When a limit runs out, it
// stops the run, offers no tools (noTools), and gives the reviewer one
// last turn to answer.
func awaitReview(ctx context.Context, rs *session.Session, activity func(core.Event), limits ReviewLimits, noTools func(), agent reviewAgent) (session.ReviewAnswer, error) {
	w := reviewWatch{activity: activity, limits: limits, live: map[string]bool{}}
	done := ctx.Done()
	var deadline, lastTurn <-chan time.Time
	if limits.Time > 0 {
		t := time.NewTimer(limits.Time)
		defer t.Stop()
		deadline = t.C
	}
	// submitted brings the last turn's Submit back; the loop goes on
	// reading the session's events meanwhile, so none is lost.
	var submitted chan error
	interrupt := func() { go func() { _ = rs.Interrupt() }() }
	for {
		select {
		case <-done:
			done = nil
			interrupt()
		case <-deadline:
			deadline = nil
			if w.reach(session.ReviewTimeLimit) {
				interrupt()
			}
		case <-lastTurn:
			lastTurn = nil
			interrupt()
		case err := <-submitted:
			submitted = nil
			if err != nil { // no run starts, so no Idle follows
				w.cause = err.Error()

				return w.answer(ctx)
			}
			if ctx.Err() != nil {
				interrupt() // a stop that came while the session was idle reached no run
			}
		case e, ok := <-rs.Events():
			if !ok {
				return session.ReviewAnswer{}, errors.New("the reviewer's session closed")
			}
			agent.observe(e)
			step := w.observe(e)
			if step == stepIdle && agent.busy() {
				step = stepNone // the main agent's message starts another run
			}
			switch step {
			case stepStop:
				interrupt()
			case stepIdle:
				if w.phase != reviewStopping || w.text != "" || ctx.Err() != nil {
					return w.answer(ctx)
				}
				w.phase, w.ran, w.cause = reviewLastTurnPhase, false, ""
				noTools()
				submitted = make(chan error, 1)
				go func(errc chan<- error, text string) {
					_, err := rs.Submit(text)
					errc <- err
				}(submitted, lastTurnMessage(w.limit, limits))
				lastTurn = time.After(lastTurnLimit(limits))
			case stepNone:
			}
		}
	}
}

// What an event means for awaitReview.
type step int

const (
	stepNone step = iota
	// stepStop: stop the reviewer's run.
	stepStop
	// stepIdle: the reviewer's session went idle after a run.
	stepIdle
)

// reviewWatch folds the reviewer's events: its tool events, a failed
// command's output, and its model responses (their tokens) go to
// activity, and its first answer, its runs' tokens, and a failure are
// kept.
type reviewWatch struct {
	activity func(core.Event)
	limits   ReviewLimits
	phase    int
	// live are the calls that still run.
	live map[string]bool
	// text is the reviewer's first answer; ran says a run finished, and
	// status is how the last one ended.
	text   string
	ran    bool
	status core.Status
	runOut string
	// tokens are the runs' tokens, and used the responses' so far.
	tokens, used core.Tokens
	limit        session.ReviewLimit
	cause        string
}

// observe takes one event and says what to do.
func (w *reviewWatch) observe(e core.Event) step {
	switch e := e.(type) {
	case core.ToolCalled:
		w.live[e.CallID] = true
		w.report(e)
	case core.ToolFinished:
		delete(w.live, e.CallID)
		w.report(e)
	case core.ToolStarted, engine.ToolOutput:
		w.report(e)
	case core.ModelResponded:
		w.report(e)
		w.used = w.used.Add(e.Usage)
		if w.limits.Tokens > 0 && w.used.InputTokens+w.used.OutputTokens >= w.limits.Tokens && w.reach(session.ReviewTokenLimit) {
			return stepStop
		}
	case engine.Answered:
		return w.answered(e.Text)
	case core.RunFinished:
		w.ran, w.status, w.runOut = true, e.Result.Status, e.Result.Answer
		w.tokens = w.tokens.Add(e.Result.Stats.Tokens)
	case session.Idle:
		if w.ran || w.cause != "" {
			return stepIdle
		}
	default:
		w.failed(e)
	}

	return stepNone
}

// report passes one of the reviewer's tool events or model responses on.
func (w *reviewWatch) report(e core.Event) {
	if w.activity != nil {
		w.activity(e)
	}
}

// answered keeps the reviewer's first answer; a call still running, or
// the last turn, stops the run, so nothing wakes the reviewer again.
func (w *reviewWatch) answered(text string) step {
	if w.text != "" || strings.TrimSpace(text) == "" {
		return stepNone
	}
	w.text = text
	if w.phase == reviewStopping { // it answered as the limit ran out: no limit stopped it
		w.phase, w.limit = reviewWorking, ""
	}
	if len(w.live) > 0 || w.phase == reviewLastTurnPhase {
		return stepStop
	}

	return stepNone
}

// failed keeps why the reviewer's run failed.
func (w *reviewWatch) failed(e core.Event) {
	switch e := e.(type) {
	case core.RunnerError:
		w.cause = e.Message
	case session.InputFailed:
		w.cause = e.Reason
	case session.Notice:
		if e.Level == session.LevelError {
			w.cause = e.Message
		}
	}
}

// reach notes that a limit ran out while the reviewer works without an
// answer, and reports whether the run must stop for its last turn.
func (w *reviewWatch) reach(limit session.ReviewLimit) bool {
	if w.phase != reviewWorking || w.text != "" {
		return false
	}
	w.phase, w.limit = reviewStopping, limit

	return true
}

// answer is the reviewer's first answer, or why there is none, with the
// tokens its runs used either way.
func (w *reviewWatch) answer(ctx context.Context) (session.ReviewAnswer, error) {
	a := session.ReviewAnswer{Tokens: w.tokens, Limit: w.limit, Interrupted: ctx.Err() != nil}
	if a.Tokens == (core.Tokens{}) {
		a.Tokens = w.used // a run that ended without a result
	}
	switch {
	case ctx.Err() != nil:
		return a, fmt.Errorf("the review stopped: %w", ctx.Err())
	case w.text != "":
		a.Text = w.text

		return a, nil
	case w.ran && w.status == core.StatusOK && w.limit == "":
		a.Text = w.runOut

		return a, nil
	case w.cause != "":
		return a, errors.New(readable(w.cause))
	case w.limit != "":
		return a, fmt.Errorf("the reviewer did not answer after the %s limit", w.limit)
	case w.ran:
		return a, errors.New(strings.TrimSpace(fmt.Sprintf("the review ended with status %s. %s", w.status, w.runOut)))
	}

	return a, errors.New("the reviewer did not answer")
}
