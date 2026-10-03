package embedded

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/viktordanov/uah-core/harness/contextbuilder"
	"github.com/viktordanov/uah-core/harness/coordinator"
	"github.com/viktordanov/uah-core/harness/inbox"
	"github.com/viktordanov/uah-core/harness/llm"
	"github.com/viktordanov/uah-core/harness/operation"
	"github.com/viktordanov/uah-core/harness/sessionstore"
	"github.com/viktordanov/uah-core/harness/tool"

	"github.com/viktordanov/uagent/core"
	"github.com/viktordanov/uagent/harness"

	"github.com/viktordanov/uah/internal/approval"
	"github.com/viktordanov/uah/internal/compaction"
	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/instructions"
	"github.com/viktordanov/uah/internal/session"
)

// The runner's defaults.
const (
	toolHeartbeatInterval = 10 * time.Minute
	exitInterrupted       = 130
)

// backend is the harness.Backend that starts the coordinator in process.
type backend struct{ e *Engine }

// Start reproduces the runner's Run: provider client, session store, tools,
// inbox, context builder, and coordinator. It never loads the workspace .env.
func (b backend) Start(ctx context.Context, l harness.Launch) (harness.Process, error) {
	start, _ := ctx.Value(startKey{}).(startValue)
	w := &wiring{
		e: b.e, l: l, getenv: b.e.cfg.Getenv, emit: start.emit, notify: start.opts.Notify, ask: start.opts.Ask,
		askAnytime: start.opts.AskAnytime, askUser: start.opts.AskUser, goal: start.opts.Goal, inject: start.opts.Inject, tier: start.opts.ServiceTier, adaptive: start.opts.AdaptiveEffort,
		mode: newModeCell(start.opts, b.e.cfg),
	}
	a, err := w.start(ctx, start.opts)
	if err != nil {
		err = errors.Join(err, w.cleanup())
		_ = l.Stdout.Close()

		return nil, err
	}

	return a, nil
}

// wiring holds what a starting run has opened, so a failure can close it.
// Each step that opens a resource adds its closer; once the coordinator
// starts, its goroutine owns them.
type wiring struct {
	e      *Engine
	l      harness.Launch
	getenv func(string) string
	emit   func(core.Event)
	// notify reaches the session after the run ends (nil: emit only).
	notify func(core.Event)
	ask    approval.Ask
	// askAnytime asks the user also after the run ends: subagents' approvals
	// go to it, after their own auto-review.
	askAnytime approval.Ask
	// askUser asks the user the agent's questions (request_user_input).
	askUser engine.AskUser
	// goal answers the goal tools with the session's goal.
	goal engine.GoalTool
	// inject gives the session's agent a message without a turn of its own.
	inject func(string) func()
	// tier and adaptive are the run's service tier and adaptive effort
	// when it started.
	tier     string
	adaptive string
	// mode is the run's permission mode, which Run.SetMode changes.
	mode *modeCell
	// bashTools, when set, keeps Bash's definition in each model request
	// in step with the mode.
	bashTools func([]llm.Tool) []llm.Tool
	closers   []closer
}

// closer releases something a starting run opened. One that saves the
// session's state fails the run when it fails; the others' errors are
// dropped.
type closer struct {
	close func() error
	saves bool
}

// closeAll runs the closers in reverse and returns the saving ones' errors.
func closeAll(closers []closer) error {
	var errs []error
	for _, c := range slices.Backward(closers) {
		if err := c.close(); err != nil && c.saves {
			errs = append(errs, err)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("failed to save the session: %w", err)
	}

	return nil
}

// cleanup closes what a failed start opened.
func (w *wiring) cleanup() error {
	err := closeAll(w.closers)
	w.closers = nil

	return err
}

// start opens the run's resources in the runner's order and starts the
// coordinator.
func (w *wiring) start(ctx context.Context, opts engine.Options) (*agent, error) {
	req := w.l.Request
	messages, err := requestMessages(req)
	if err != nil {
		return nil, err
	}
	if req.SessionID == "" {
		req.SessionID = uuid.NewString() // as openStore would, so the prepared context knows it
	}
	messages = w.prepared(ctx, req, messages)
	model, sw, err := w.client(req, opts)
	if err != nil {
		return nil, err
	}
	w.closers = append(w.closers, closer{close: sw.Close})
	sw.seen, sw.cacheKey = w.e.last.recorder(req.SessionID), w.e.cacheKey(req.SessionID)
	if w.e.cfg.AutoReview || w.ask != nil || w.mode.get().ReviewerDecides() {
		w.ask = w.reviewedAsk(sw, req) //nolint:contextcheck,nolintlint // on Linux, the sandbox probes bwrap once per process, with its own timeout; not on darwin
	}
	if sc := w.e.scope(req.SessionID); sc != nil && sc.NeverAsk {
		w.ask = neverAsk
	} else if sc != nil && len(sc.Approve) > 0 {
		w.ask = sc.ask(w.ask, w.mode.get) // before the auto-reviewer, as a rule would be
	}
	s, err := w.openStore(ctx, req, messages)
	if err != nil {
		return nil, err
	}
	if sw.searches, err = w.searchLog(req.Provider, string(s.id)); err != nil {
		return nil, err
	}

	// The run stops through the inbox; the harness cancels only after the grace period.
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	w.closers = append(w.closers, closer{close: func() error { cancel(); return nil }})
	// An interrupt ends the approvals at once: the coordinator waits for
	// them before it reads the stop.
	approvals, stopApprovals := context.WithCancel(runCtx)
	w.closers = append(w.closers, closer{close: func() error { stopApprovals(); return nil }})

	registry, err := w.tools(runCtx, approvals, req, s.id)
	if err != nil {
		return nil, err
	}
	prefetch := newPrefetcher(approvals, registry, s.id, w.e.transcript(req.SessionID).note)
	registry = prefetch.wrap(registry)
	sw.tools = w.bashTools
	sw.images = w.e.pastedImages
	sw.stream, sw.text, sw.diag = w.emit, opts.Stream, w.l.Stderr
	sw.adaptive.steps = session.AdaptiveSteps(opts.AdaptiveEffort)
	if err := w.effortUpdates(ctx, sw, s, req); err != nil {
		return nil, err
	}
	operations := operation.NewLocalOperationManager(runCtx, newMCPJobs(runCtx, w.e.cfg.MCP), newAgentJobs(runCtx, w.e.cfg.Subagents, string(s.id)), newPatchJobs(runCtx), newQuestionJobs(runCtx), newGoalJobs(runCtx))
	first := compaction.Trigger("")
	switch {
	case opts.Clear:
		first = compaction.TriggerClear
	case opts.Compact:
		first = compaction.TriggerManual
	}
	comp, err := w.compactor(runCtx, s, sw, first, opts.CompactFocus)
	if err != nil {
		return nil, err
	}
	w.closers = append(w.closers, closer{close: comp.stop})
	a, err := newAgent(runCtx, cancel, sw, s.restored, req.Effort, messages, s.early != nil)
	if err != nil {
		return nil, err
	}
	a.compactor, a.mode, a.stopApprovals = comp, w.mode, stopApprovals

	builder := newContextBuilder(registry, model, req)
	for _, t := range w.hostedTools(req.Provider, req.SessionID) {
		builder.AddTool(t)
	}
	obs := &observer{sessionID: s.id, out: w.l.Stdout, cancel: cancel, emit: w.emit, early: s.early}
	observerID := s.store.AddObserver(obs.observe)
	prefetchID := s.store.AddObserver(prefetch.observe) // after obs, which writes the response first
	coord := coordinator.New(coordinator.Dependencies{
		ToolHeartbeatInterval: toolHeartbeatInterval,
		SessionID:             s.id,
		Inbox:                 a.inputs,
		Restored:              s.restored,
		Sessions:              s.store,
		ContextBuilder:        builder,
		LLM:                   comp,
		Tools:                 registry,
		Operations:            operations,
		Wake:                  wakePolicy(),
		EffortUpdate:          sw.effortUpdate,
	})
	w.launch(runCtx, a, coord, obs, func() { s.store.RemoveObserver(prefetchID); s.store.RemoveObserver(observerID) })

	return a, nil
}

// compactor wraps the switcher with the session's compactions and seeds the
// context in use from the session's last response. ctx is the run's: a
// compaction lives until the run ends.
func (w *wiring) compactor(ctx context.Context, s runStore, sw *switcher, first compaction.Trigger, focus string) (*compactor, error) {
	log := compaction.OpenLog(w.l.SessionsDir, string(s.id))
	records, corrupt, err := log.Records()
	if err != nil {
		return nil, err
	}
	cuts, err := compaction.OpenRewinds(w.l.SessionsDir, string(s.id)).Records()
	if err != nil {
		return nil, err
	}
	var rec *compaction.Record
	if n := len(records); n > 0 {
		rec, records = &records[n-1], records[:n-1]
	}
	if corrupt > 0 && w.e.cfg.Logger != nil {
		w.e.cfg.Logger.WarnContext(ctx, "skipped unreadable lines in the compaction log",
			slog.String("path", log.Path()),
			slog.Int("lines", corrupt),
		)
	}
	used, err := lastUsage(ctx, s.store, s.id)
	if err != nil {
		return nil, err
	}
	emit := w.emit
	if emit == nil {
		emit = func(core.Event) {}
	}
	cfg := w.e.cfg
	before := func(ctx context.Context, t compaction.Trigger) error {
		if cfg.BeforeCompact == nil {
			return nil
		}

		return cfg.BeforeCompact(ctx, string(s.id), t)
	}

	return &compactor{
		ctx: ctx, next: sw, log: log, emit: emit, logger: cfg.Logger, before: before, remote: w.remoteCompaction(), window: cfg.ContextWindow, windows: w.e.models.Window, settings: cfg.Compaction,
		record: rec, older: records, cuts: cuts, pending: first, focus: focus, used: used,
	}, nil
}

// effortUpdates turns effort updates on for the run when they are on and
// the backend has not rejected them in the session (adaptive.go,
// effortfallback.go): each request then carries the session's first
// effort, so the session's runs share one prompt cache.
func (w *wiring) effortUpdates(ctx context.Context, sw *switcher, s runStore, req core.Request) error {
	if !w.e.cfg.EffortUpdates {
		return nil
	}
	dir := w.e.sessionsDir()
	if off, err := updatesRejected(dir, string(s.id)); err != nil || off {
		return err
	}
	items, err := allItems(ctx, s.store, s.id)
	if err != nil {
		return err
	}
	sw.base = cmp.Or(firstEffort(items), reasoningEffort(req.Effort))
	sw.updates = func(model string) bool { return w.e.models.EffortUpdates(req.Provider, model) }
	sw.offUpdates = func(e engine.EffortUpdatesOff) error { return saveUpdatesRejected(dir, string(s.id), e) }

	return nil
}

// remoteCompaction reports whether the run's compactions go to the
// provider: remote_compaction is on and the provider can.
func (w *wiring) remoteCompaction() bool {
	p, err := w.e.provider(w.l.Request.Provider)

	return err == nil && p.RemoteCompaction && w.e.cfg.Compaction.Remote
}

// newAgent opens the inbox and submits the initial settings and messages,
// unless openStore recorded them (recorded).
func newAgent(ctx context.Context, cancel context.CancelFunc, sw *switcher, restored sessionstore.ResumeState, effort string, messages []core.UserInput, recorded bool) (*agent, error) {
	inputs, err := inbox.New(ctx, restored.ExternalInputIDs)
	if err != nil {
		return nil, fmt.Errorf("failed to open the inbox: %w", err)
	}
	a := &agent{ctx: ctx, cancel: cancel, inputs: inputs, llm: sw, done: make(chan struct{})}
	if err := sw.setUltra(effort == effortUltra); err != nil {
		return nil, err
	}
	if !recorded {
		if err := a.SetEffort(effort); err != nil {
			return nil, err
		}
		for _, m := range messages {
			if err := a.Send(m); err != nil {
				return nil, err
			}
		}
	}
	// Stop when idle, as the runner does: messages sent before the agent is
	// idle keep it running, so live input works until the run ends.
	if err := a.control(inbox.ControlMessage{Mode: inbox.StopWhenIdle}); err != nil {
		return nil, err
	}

	return a, nil
}

// newContextBuilder returns the builder with the model, the system prompt
// (uah's default base instructions when the request has none), and the registry's skills and tools.
func newContextBuilder(registry tool.Registry, model string, req core.Request) contextbuilder.Builder {
	builder := contextbuilder.NewBuilder(registry.Skills()...)
	builder.SetModel(llm.Model{ID: model, ReasoningEffort: reasoningEffort(req.Effort)})
	prompt := req.SystemPrompt
	if prompt == "" {
		prompt = instructions.DefaultPrompt
	}
	builder.SetSystemPrompt(prompt)
	for _, d := range registry.StaticDefinitions() {
		builder.AddTool(d.Tool)
	}

	return builder
}

// launch hands the closers to a goroutine that runs the coordinator, records
// the exit code, and then closes them and the output.
func (w *wiring) launch(ctx context.Context, a *agent, coord coordinator.Coordinator, obs *observer, detach func()) {
	closers := w.closers
	w.closers = nil
	go func() {
		for _, it := range obs.early { // once the output is read
			obs.observe(obs.sessionID, it)
		}
		err := runCoordinator(ctx, coord)
		a.llm.settle(5 * time.Second) // a canceled request ends promptly
		detach()
		if oerr := obs.err(); oerr != nil {
			err = oerr
		}
		w.finish(a, err, closers)
	}()
}

// finish records how the run ended, closes what it opened, and closes its
// output. A failure to save the session is reported as the run's error and
// fails a run that would have succeeded; an interrupted run keeps its code.
func (w *wiring) finish(a *agent, err error, closers []closer) {
	switch {
	case a.interrupted.Load():
		a.code = exitInterrupted
	case err != nil:
		w.fail(runError(err))
		a.code = 1
	}
	if serr := closeAll(closers); serr != nil {
		w.fail(serr)
		if a.code == 0 {
			a.code = 1
		}
	}
	_ = w.l.Stdout.Close()
	close(a.done)
}

// fail reports the run's error to the session and to stderr.
func (w *wiring) fail(err error) {
	writeError(w.l.Stdout, err)
	_, _ = fmt.Fprintf(w.l.Stderr, "embedded: %v\n", err)
}

// requestMessages returns the request's messages, or its prompt as one message.
func requestMessages(req core.Request) ([]core.UserInput, error) {
	if len(req.Messages) > 0 {
		return req.Messages, nil
	}
	if strings.TrimSpace(req.Prompt) == "" {
		return nil, errors.New("the request has no messages")
	}

	return []core.UserInput{{ID: uuid.NewString(), Text: req.Prompt}}, nil
}

// runCoordinator runs the coordinator and turns a panic into an error, so
// runner code cannot take down the TUI; the session file stays intact.
func runCoordinator(ctx context.Context, c coordinator.Coordinator) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("the agent panicked: %v", p)
		}
	}()
	if err := c.Run(ctx); err != nil {
		if ctx.Err() != nil {
			return nil // stopped by cancellation
		}

		return fmt.Errorf("the coordinator stopped: %w", err)
	}

	return nil
}
