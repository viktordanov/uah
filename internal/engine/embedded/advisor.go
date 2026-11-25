package embedded

import (
	"cmp"
	"context"
	"encoding/json"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/viktordanov/uah-core/harness/llm"
	"github.com/viktordanov/uah-core/harness/operation"
	"github.com/viktordanov/uah-core/harness/tool"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/compaction"
	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/sandbox"
)

// The advisor (docs/design/advisor.md) is a tool with no parameters: a
// call sends the conversation so far to a stronger model, which answers
// with advice, the call's result. The conversation is the session's last
// turn request as sent and its response up to and including the call
// (lastRequests.transcript), so the advisor sees what the agent saw,
// compacted and elided. The call runs as a remote job on its own
// goroutine, under the configured timeout and no shorter deadline; an
// interrupt or the run's end cancels it. Whatever goes wrong (the limit,
// the timeout, the provider, a stale transcript) is a result the agent
// reads, such as "The advisor is unavailable (timeout after 10m); go on
// without it.", never a failed call.

const (
	toolAdvisor        = engine.AdvisorToolName
	advisorPlanType    = operation.RemoteJobPlanType(engine.AdvisorPlanType)
	advisorPlanVersion = operation.RemoteJobPlanVersion(1)
)

// Advisor configures the advisor. It is off while neither Model nor
// Command is set; app checks that one is at most. Model is the model
// backend's model, on the session's provider (Provider.Advisor), and
// Effort its effort ("": the session's). Command is the command
// backend's program and arguments (advisorexec.go), on any provider,
// which reads the transcript as Input (engine.AdvisorInputText when "",
// or engine.AdvisorInputJSON), cut to MaxInputTokens
// (engine.DefaultAdvisorMaxInputTokens when 0). Timeout bounds a call
// (engine.DefaultAdvisorTimeout when 0) and MaxUses the calls of a run
// (engine.DefaultAdvisorMaxUses when 0). Subagents offers it to
// subagents too. Off, set by UAH_ADVISOR=off, keeps it off, also through
// /advisor.
type Advisor struct {
	Model   string
	Effort  string
	Command []string
	// Program is the path Command's program runs as: app finds it in PATH
	// when the session opens and resolves its links (pinAdvisorProgram);
	// "" when PATH lacked it, and no command runs. Command[0] stays the
	// program's name, as the command sees it.
	Program string
	// Refused is why app dropped Command when the session opened
	// (pinAdvisorProgram): its program, or a link to it, is where the
	// agent's commands can write. /advisor says so in place of "set one".
	Refused        string
	Input          string
	MaxInputTokens int64
	Timeout        time.Duration
	MaxUses        int
	Subagents      bool
	Off            bool
}

func (a Advisor) input() string { return cmp.Or(a.Input, engine.AdvisorInputText) }

func (a Advisor) maxInputTokens() int64 {
	if a.MaxInputTokens > 0 {
		return a.MaxInputTokens
	}

	return engine.DefaultAdvisorMaxInputTokens
}

func (a Advisor) timeout() time.Duration {
	if a.Timeout > 0 {
		return a.Timeout
	}

	return engine.DefaultAdvisorTimeout
}

func (a Advisor) maxUses() int {
	if a.MaxUses > 0 {
		return a.MaxUses
	}

	return engine.DefaultAdvisorMaxUses
}

// advisorDescription says what the advisor is and when to call it; it
// comes and goes with the tool, so the system prompt needs no section of
// its own. It gives reasons rather than bans: GPT models take a ban
// literally, even where the user asks otherwise.
func advisorDescription(maxUses int) string {
	return "Ask a stronger model for advice on your work. It reads this whole conversation, your tool calls and their results included, " +
		"and answers in text; it cannot run tools, so it knows only what the conversation shows.\n\n" +
		"Call it:\n" +
		"- once you have oriented yourself (read the task and the relevant code) and before substantive work: before you write or edit files, or commit to an approach;\n" +
		"- when you are stuck, or results do not fit what you expected;\n" +
		"- when you consider changing your approach;\n" +
		"- before you say the task is done, once the work is saved.\n\n" +
		"On a task of more than a few steps, call it at least before you commit to an approach and before you say you are done: " +
		"it helps most early, before an approach sets. A question you can answer at once needs no call.\n\n" +
		"Call it on its own, not beside edits or commands in one response: it sees only the results you already have. " +
		fmt.Sprintf("A call can take minutes, and a run has at most %d. ", maxUses) +
		"Weigh its advice against your evidence: when what you found contradicts it, say so in another call rather than switching silently."
}

// advisorPlan is the remote job's plan: the call it answers, whose
// transcript it reads.
type advisorPlan struct {
	CallID string `json:"call_id"`
}

// advisorRegistry offers the advisor, and resolves its name even when it
// is not offered, so a session with past calls resumes anywhere.
type advisorRegistry struct {
	tool.Registry

	t       advisorTranslator
	maxUses int
}

func withAdvisor(r tool.Registry, t advisorTranslator, maxUses int) tool.Registry {
	return advisorRegistry{Registry: r, t: t, maxUses: maxUses}
}

func (r advisorRegistry) StaticDefinitions() []tool.Definition {
	defs := r.Registry.StaticDefinitions()
	if r.t.offered {
		defs = append(defs, tool.Definition{Tool: llm.Tool{
			Type: llm.ToolFunction, Name: toolAdvisor, Description: advisorDescription(r.maxUses),
			Parameters: map[string]any{schemaType: jsonObject, schemaProperties: map[string]any{}, "additionalProperties": false},
		}})
	}

	return defs
}

func (r advisorRegistry) Resolve(name string) (tool.Translator, bool) {
	if t, ok := r.Registry.Resolve(name); ok || name != toolAdvisor {
		return t, ok
	}

	return r.t, true
}

// offersAdvisor reports whether the run's session gets the advisor, and
// whether its calls are refused. The main agent gets it while the advisor
// is on for its session; a subagent with [advisor] subagents, and a child
// forked from the main agent, which keeps its parent's tools so its
// requests keep the parent's prompt cache, and is refused at the call.
// A child is one with the subagent prefix or a tree root other than
// itself, as a resumed child from before the prefix has. Subagents never
// start subagents (agents.MaxDepth), so a fork's parent is the main agent.
func (w *wiring) offersAdvisor(req core.Request) (offered, refused bool) {
	if !w.e.advisorOffered(req.Provider, req.SessionID) || slices.Contains(req.DisallowedTools, toolAdvisor) {
		return false, false
	}
	child := isSubagent(req.SessionID) || w.e.treeRoot(req.SessionID) != req.SessionID
	if !child || w.e.cfg.Advisor.Subagents {
		return true, false
	}
	f, ok := w.e.cfg.Subagents.(interface{ Forked(sessionID string) bool })
	forked := ok && f.Forked(req.SessionID)

	return forked, forked
}

// advisorNotForSubagents refuses a forked child's call while subagents do
// not get the advisor.
const advisorNotForSubagents = "the advisor is not available to subagents in this session"

// advisorTranslator submits a call as an advisor job and reads its result.
type advisorTranslator struct {
	offered bool
	// refused is a forked child's: offered, refused at the call.
	refused bool
}

func (t advisorTranslator) Translate(ctx tool.Context, call llm.ToolCall) tool.CallStatus {
	switch {
	case !t.offered:
		return tool.ErrorStatus(fmt.Sprintf("tool %q is not available in this session", toolAdvisor), 0)
	case t.refused:
		return tool.ErrorStatus(advisorNotForSubagents, 0)
	}
	data, err := json.Marshal(advisorPlan{CallID: call.CallID})
	if err != nil {
		return tool.ErrorStatus(fmt.Sprintf("failed to encode the advisor call: %v", err), 0)
	}
	spec, err := operation.NewRemoteJobSpec(operation.RemoteJobPlan{Type: advisorPlanType, Version: advisorPlanVersion, Data: jsontext.Value(data)})
	if err != nil {
		return tool.ErrorStatus(fmt.Sprintf("failed to build the advisor call: %v", err), 0)
	}

	return tool.CallStatus{WaitingFor: []operation.ID{ctx.Submit(spec)}}
}

func (advisorTranslator) TranslateResult(callID string, status tool.CallStatus, ops []operation.Operation) (llm.ToolResult, error) {
	result := llm.ToolResult{CallID: callID}
	text := func(s string) {
		result.Output = append(result.Output, llm.ToolResultOutput{Kind: llm.ToolResultText, Value: s})
	}
	if status.Error != "" {
		text("Error: " + status.Error)

		return result, nil
	}
	if len(ops) != 1 {
		return result, fmt.Errorf("advisor call %q has %d operations, want 1", callID, len(ops))
	}
	state, err := operation.DecodeRemoteJobState(ops[0])
	if err != nil {
		return result, fmt.Errorf("failed to decode advisor call %q: %w", callID, err)
	}
	switch ops[0].Status {
	case operation.StatusReady, operation.StatusAwaiting, operation.StatusCanceling:
		text("The advisor is still reading the conversation.")
	case operation.StatusFailed:
		if state.TerminalResult != "" { // run's: the text names why
			text(state.TerminalResult)
		} else {
			text(unavailable(state.TerminalError))
		}
	case operation.StatusCanceled:
		text("The advisor call was canceled.")
	case operation.StatusCompleted:
		text(state.TerminalResult)
	}

	return result, nil
}

// unavailable is the result of a call that got no advice, and why.
func unavailable(why string) string {
	return fmt.Sprintf("The advisor is unavailable (%s); go on without it.", why)
}

// The results of a call that does not reach the advisor.
const (
	advisorOff         = "The advisor is off for this session; go on without it."
	advisorInterrupted = "The advisor call was interrupted by a restart and not repeated; call it again if you still need advice."
)

func advisorLimit(n int) string {
	calls := "calls"
	if n == 1 {
		calls = "call"
	}

	return fmt.Sprintf("The advisor's limit for this run is reached (%d %s); go on without it.", n, calls)
}

// advisorJobs is the runner's RemoteJobHandler for advisor calls, one per
// run, which counts the run's calls. Each call runs on its own goroutine.
type advisorJobs struct {
	ctx       context.Context
	e         *Engine
	sw        *switcher
	provider  string
	sessionID string
	// cacheKey is the advisor calls' prompt cache key: the session's under
	// a prefix of its own, as the reviewer's is. On the session's own key,
	// which its turn requests use, a call found none of the call before it
	// in the cache.
	cacheKey string
	// logPath is the session's advisor log (engine.AdvisorRecord).
	logPath string
	// workspace is where a command runs; changeable reports whether the
	// session's commands could change a program, under its sandbox policy
	// in workspace-write mode with its grants now, or it now leads
	// elsewhere through a link (nil: no sandbox).
	workspace  string
	changeable func(path string) bool
	updates    chan operation.Operation
	uses       atomic.Int64
	// calls are the backend calls in flight, which close waits for, so a
	// command's process group is stopped before the run ends.
	calls sync.WaitGroup

	mu      sync.Mutex
	cancels map[operation.ID]context.CancelFunc
	// closed is set by close: no backend call starts after it.
	closed bool
}

func (w *wiring) advisorJobs(ctx context.Context, sw *switcher, req core.Request, sessionID string) *advisorJobs {
	j := &advisorJobs{
		ctx: ctx, e: w.e, sw: sw, provider: req.Provider, sessionID: sessionID, cacheKey: advisorCacheKey(w.e.promptCacheKey(sessionID)),
		logPath: filepath.Join(w.l.SessionsDir, sessionID+engine.AdvisorLogSuffix), workspace: req.Workspace,
		updates: make(chan operation.Operation), cancels: map[operation.ID]context.CancelFunc{},
	}
	if w.e.cfg.Sandbox != nil { // without one, commands run outside it too
		j.changeable = func(path string) bool {
			// app pinned the program with its links resolved: one that
			// leads elsewhere now was changed.
			return sandbox.ResolvePath(path) != path || w.policy(req, sandbox.WorkspaceWrite).Changeable(path)
		}
	}
	w.closers = append(w.closers, closer{close: j.close})

	return j
}

// advisorCloseWait bounds how long the run's end waits for its calls: a
// canceled command is killed at once, and its pipes close within
// advisorWaitDelay.
const advisorCloseWait = 5 * time.Second

// close cancels the calls in flight and waits, up to advisorCloseWait, for
// their backends to return and their log lines to be written, so a
// command's process group is gone and the call recorded before the run
// ends and the process may exit.
func (j *advisorJobs) close() error {
	j.mu.Lock()
	j.closed = true
	for _, cancel := range j.cancels {
		cancel()
	}
	j.mu.Unlock()
	done := make(chan struct{})
	go func() { j.calls.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(advisorCloseWait):
	}

	return nil
}

// advisorCacheKey is the advisor's prompt cache key for a session whose
// turn requests use key.
func advisorCacheKey(key string) string { return "uah-advisor-" + key }

func (*advisorJobs) RemoteJobPlanType() operation.RemoteJobPlanType       { return advisorPlanType }
func (*advisorJobs) RemoteJobPlanVersion() operation.RemoteJobPlanVersion { return advisorPlanVersion }
func (j *advisorJobs) RemoteJobUpdates() <-chan operation.Operation       { return j.updates }

// AddRemoteJob starts a call. A call that had already started before a
// restart completes without advice: its transcript was in memory. A
// canceling one (the tool policy's, policyOperations) is canceled.
func (j *advisorJobs) AddRemoteJob(op operation.Operation) error {
	state, err := operation.DecodeRemoteJobState(op)
	if err != nil {
		return err // the runner's own error
	}
	var plan advisorPlan
	if err := json.Unmarshal(state.Plan.Data, &plan); err != nil {
		return err // the operation manager reports it
	}
	switch op.Status {
	case operation.StatusReady:
	case operation.StatusCanceling:
		go j.finish(operation.CancelRemoteJob(op))

		return nil
	default:
		state.TerminalResult = advisorInterrupted
		go j.finish(operation.UpdateRemoteJob(op, state, operation.StatusCompleted))

		return nil
	}
	ctx, cancel := context.WithCancel(j.ctx)
	j.mu.Lock()
	j.cancels[op.ID] = cancel
	j.mu.Unlock()
	go j.run(ctx, op, state, plan)

	return nil
}

// CancelRemoteJob cancels a running call, and with it its model call; it
// then reports canceled.
func (j *advisorJobs) CancelRemoteJob(id operation.ID, _ string) error {
	j.mu.Lock()
	cancel := j.cancels[id]
	j.mu.Unlock()
	if cancel != nil {
		cancel()
	}

	return nil
}

func (j *advisorJobs) run(ctx context.Context, op operation.Operation, state operation.RemoteJobState, plan advisorPlan) {
	defer func() {
		j.mu.Lock()
		j.cancels[op.ID]()
		delete(j.cancels, op.ID)
		j.mu.Unlock()
	}()
	step, err := operation.UpdateRemoteJob(op, state, operation.StatusAwaiting)
	if err != nil || !j.send(*step.Operation) {
		return
	}
	op = *step.Operation
	text, why := j.advise(ctx, plan.CallID)
	if ctx.Err() != nil && j.ctx.Err() == nil { // canceled by the coordinator
		j.finish(operation.CancelRemoteJob(op))

		return
	}
	state.TerminalResult = text
	status := operation.StatusCompleted
	if why != "" {
		// The call fails, so the transcript, uah exec, and the run's stats
		// show it; the agent still reads text, which says to go on.
		state.TerminalError, status = why, operation.StatusFailed
	}
	j.finish(operation.UpdateRemoteJob(op, state, status))
}

// advise answers one call: the advice, or why there is none. why is the
// reason a call failed: it reached no advisor, or the advisor gave no
// advice ("" for advice, and for the advisor off or past its limit, which
// are not failures). text is what the agent reads either way.
func (j *advisorJobs) advise(ctx context.Context, callID string) (text, why string) {
	setup, on := j.e.advisorFor(j.provider, j.sessionID)
	if !on {
		return advisorOff, ""
	}
	t, ok := j.e.last.transcript(j.sessionID, callID)
	if !ok {
		return failed("the conversation it reads is not available: another model request came after this call", "")
	}
	limit := j.e.cfg.Advisor.maxUses()
	if j.uses.Add(1) > int64(limit) {
		return advisorLimit(limit), ""
	}
	backend := j.backend(setup, t)
	timeout := j.e.cfg.Advisor.timeout()
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if !j.begin() {
		return failed("the run ended", "")
	}
	start := time.Now()
	a, err := backend.advise(callCtx, t)
	j.record(callID, start, a, err)
	j.calls.Done() // after the log line, which close waits for too
	switch {
	case ctx.Err() != nil:
		return "", "" // canceled: run reports the cancel
	case callCtx.Err() != nil: // the timeout passed
		return failed("timeout after "+shortDuration(timeout), "")
	case err != nil:
		return failed(oneLineError(err), errorDetail(err))
	}

	return a.text, ""
}

// failed is a call that got no advice: what the agent reads, the reason
// with detail after it, and the reason.
func failed(why, detail string) (text, reason string) { return unavailable(why) + detail, why }

// begin counts a backend call in flight; false once close began.
func (j *advisorJobs) begin() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return false
	}
	j.calls.Add(1)

	return true
}

// detailedError is an error with more to say than its first line, such as
// the end of a command's stderr, which follows the unavailable result.
type detailedError interface {
	error
	detail() string
}

func errorDetail(err error) string {
	if d, ok := errors.AsType[detailedError](err); ok && d.detail() != "" {
		return "\n\n" + d.detail()
	}

	return ""
}

// backend is the call's backend: the configured command, or the model
// backend on the session's client.
func (j *advisorJobs) backend(setup advisorSetup, t advisorTranscript) advisorBackend {
	if len(setup.command) > 0 {
		cfg := j.e.cfg.Advisor

		return advisorCommand{
			argv: setup.command, program: cfg.Program, input: cfg.input(), maxTokens: cfg.maxInputTokens(), dir: j.workspace,
			searches: j.sw.searches.snapshot(), changeable: j.changeable,
		}
	}
	effort := setup.effort
	if effort == "" {
		effort = t.effort
	}
	window := compaction.ContextWindow(setup.model, 0, j.e.models.Window)
	if setup.model == t.model {
		window = compaction.ContextWindow(setup.model, j.e.cfg.ContextWindow, j.e.models.Window)
	}

	return advisorModel{
		adapter: j.sw.directKind(kindAdvisor), model: setup.model, effort: effort,
		window: window, cacheKey: j.cacheKey,
	}
}

// record appends the call to the session's advisor log, for `uah stats`;
// a call that sent nothing to a model or ran no command is left out, and
// a failure to write is dropped.
func (j *advisorJobs) record(callID string, start time.Time, a advice, err error) {
	if a.model == "" {
		return
	}
	u := a.usage
	rec := engine.AdvisorRecord{
		At: start.UTC(), CallID: callID, Model: a.model, Effort: a.effort, DurationMS: time.Since(start).Milliseconds(),
		Usage: engine.AdvisorUsage{Input: u.InputTokens, Cached: u.CachedInputTokens, Output: u.OutputTokens, Reasoning: u.ReasoningTokens},
	}
	if err != nil {
		rec.Error = oneLineError(err)
	}
	line, merr := json.Marshal(rec)
	if merr != nil {
		return
	}
	f, oerr := os.OpenFile(j.logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if oerr != nil {
		return
	}
	_, _ = f.Write(append(line, '\n'))
	_ = f.Close()
}

// finish sends a step's operation; a step that cannot be built was already
// validated when the job was added, so it is dropped.
func (j *advisorJobs) finish(step operation.Step, err error) {
	if err == nil && step.Operation != nil {
		j.send(*step.Operation)
	}
}

func (j *advisorJobs) send(op operation.Operation) bool {
	select {
	case j.updates <- op:
		return true
	case <-j.ctx.Done():
		return false
	}
}

// advisorHold is how long the wake policy holds an advisor call: its
// timeout, plus waitSlack (waitHold); false for any other tool.
func advisorHold(call llm.ToolCall, timeout time.Duration) (time.Duration, bool) {
	return timeout, call.Name == toolAdvisor
}

// shortDuration writes d as a duration without its zero units: 10m, 1h30m,
// 2s.
func shortDuration(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}

	return s
}

// oneLineError is an error's first line, cut to 300 bytes, for a result.
func oneLineError(err error) string {
	s, _, _ := strings.Cut(strings.TrimSpace(err.Error()), "\n")
	if len(s) > 300 {
		s = strings.ToValidUTF8(s[:300], "") + "…"
	}

	return s
}
