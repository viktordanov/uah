// Package embedded is the engine that runs uah-core's packages in process.
// It reproduces the runner's wiring (cmd/internal/agentrunner/run.go, as of
// unreal-agent-runner v0.1.1) behind uagent's harness, so runs keep every
// guard, the session lock, and the run records, and it adds what a
// subprocess cannot offer: messages, effort, model, and service tier changes
// that reach a live run.
package embedded

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sync"

	"github.com/viktordanov/uagent/core"
	"github.com/viktordanov/uagent/harness"

	"github.com/viktordanov/uah/internal/approval"
	"github.com/viktordanov/uah/internal/compaction"
	"github.com/viktordanov/uah/internal/contextprep"
	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/engine/codexauth"
	"github.com/viktordanov/uah/internal/hooks"
	"github.com/viktordanov/uah/internal/mcp"
	"github.com/viktordanov/uah/internal/models"
	"github.com/viktordanov/uah/internal/review"
	"github.com/viktordanov/uah/internal/sandbox"
)

const tierPriority = "priority"

var errStopped = errors.New("the run has stopped")

// Config configures the engine.
type Config struct {
	StateDir string
	// MaxDisk stops a run whose tool output exceeds this many bytes (0 disables).
	MaxDisk int64
	Logger  *slog.Logger
	// Provider is the session's provider; it decides whether /fast is offered.
	Provider string
	// Getenv reads credentials and SHELL (default os.Getenv).
	Getenv func(string) string
	// Providers replaces the runner's provider table, for tests.
	Providers []Provider
	// Hooks, when set, runs PreToolUse hooks before each tool call.
	Hooks *hooks.Runner
	// Sandbox, when set, is the policy Bash commands run under; its
	// Workspace is replaced by each request's. SandboxDir holds the
	// sandboxing shells.
	Sandbox    *sandbox.Policy
	SandboxDir string
	// Env is which environment variables commands get (the zero value is
	// all of them). It applies when Sandbox is set.
	Env sandbox.EnvPolicy
	// Compaction configures automatic compaction and the summary call; its
	// zero value never compacts automatically.
	Compaction compaction.Settings
	// ContextWindow overrides the model catalog's context window (tokens).
	ContextWindow int64
	// Models is the model catalog: context windows and whether a model
	// gets apply_patch (nil: the catalog shipped with uah).
	Models *models.Manager
	// BeforeCompact, when set, runs as each compaction starts; an error
	// cancels the compaction. A PreCompact hook attaches here.
	BeforeCompact func(ctx context.Context, sessionID string, trigger compaction.Trigger) error
	// MCP, when set, offers its servers' tools; the engine closes it.
	MCP *mcp.Manager
	// InstructionFiles are the instruction files in the host prompt, in
	// order, so /context can list them.
	InstructionFiles []string
	// ContextModules are where context preparation's modules come from
	// besides the built-ins.
	ContextModules contextprep.Settings
	// ContextPreparation starts each new session, subagents' included,
	// with the prepared context (internal/contextprep) before its first
	// message.
	ContextPreparation bool
	// EffortUpdates changes the effort with a configuration update in the
	// history, where the model takes one (adaptive.go); UAH_EFFORT_UPDATES=off
	// turns it off. A session whose backend rejects them falls back to the
	// request's effort (effortfallback.go).
	EffortUpdates bool
	// AutoReview puts the auto-reviewer in front of the user for actions
	// that need approval (approvals_reviewer = "auto_review"), with Review's
	// model, effort, and timeout.
	AutoReview bool
	Review     review.Config
	// Approver decides how each command runs when Sandbox is set: the
	// rules and the approval policy. Nil applies no rules and asks for
	// escalations.
	Approver *approval.Approver
	// WebSearch offers the provider's hosted web search tool to a run on a
	// provider that has it (Provider.WebSearch), subagents' runs included.
	WebSearch bool
	// Verbosity is model_verbosity: low, medium, or high in place of the
	// model's default_verbosity, for a model whose catalog entry supports
	// verbosity ("": the default), as Codex's key.
	Verbosity string
	// AskUser offers request_user_input, Codex's blocking question tool,
	// to the main agent (and, refused, to its forks, which keep its tools):
	// a user drives the sessions, as in the TUI. A run asks through
	// Options.AskUser.
	AskUser bool
	// Goals offers Codex's goal tools (get_goal, create_goal, update_goal)
	// to the main agent (and, refused, to its forks). A run applies them
	// through Options.Goal.
	Goals bool
	// Subagents, when set, offers its tools to the runs it attaches and
	// hears when the user interrupts a run; the engine closes it when it is
	// an io.Closer.
	Subagents engine.Subagents
}

// Engine runs the agent in process.
type Engine struct {
	cfg Config
	h   *harness.Harness
	// transcripts feed the auto-reviewer across a session's runs, by
	// session ID, so a subagent's review sees its own session.
	transcripts sync.Map
	// last are each session's latest model request, for /context.
	last lastRequests
	// models is cfg.Models, or a catalog of the bundled list.
	models *models.Manager
	// cacheKeys are the sessions whose prompt cache key is not their ID;
	// scopes the sessions with a Scope (engine.Scoper).
	cacheKeys, scopes sync.Map
	// transports are the model clients' connections, shared by every run.
	transports transports
}

// Forget drops what the engine kept for a session that closed: the
// auto-reviewer's transcript, the last request for /context, and a cache
// key or scope it may have (engine.Forgetter).
func (e *Engine) Forget(sessionID string) {
	e.transcripts.Delete(sessionID)
	e.cacheKeys.Delete(sessionID)
	e.scopes.Delete(sessionID)
	e.last.forget(sessionID)
}

func New(cfg Config) *Engine {
	if cfg.Getenv == nil {
		cfg.Getenv = os.Getenv
	}
	catalog := cfg.Models
	if catalog == nil {
		catalog = models.New(models.Options{})
	}
	if cfg.Providers == nil {
		cfg.Providers = DefaultProviders()
	}
	if cfg.Approver == nil {
		cfg.Approver = approval.New(approval.Config{})
	}
	e := &Engine{cfg: cfg, models: catalog}
	e.h = harness.New(harness.Config{
		Backend: backend{e}, StateDir: cfg.StateDir, MaxDisk: cfg.MaxDisk, Logger: cfg.Logger, Getenv: cfg.Getenv,
	})

	return e
}

func (e *Engine) Name() string { return "embedded" }

// MCPServers reports the MCP servers, starting them if needed.
func (e *Engine) MCPServers() []mcp.ServerStatus {
	if e.cfg.MCP == nil {
		return nil
	}

	return e.cfg.MCP.Status()
}

// MCP is the engine's MCP servers (engine.MCPClient); nil when none are
// configured.
func (e *Engine) MCP() *mcp.Manager { return e.cfg.MCP }

// StartMCP connects the MCP servers before the first run
// (engine.MCPStarter); runs then use the same connections.
func (e *Engine) StartMCP(ctx context.Context) []mcp.ServerStatus {
	if e.cfg.MCP == nil {
		return nil
	}

	return e.cfg.MCP.Started(ctx)
}

// Close stops the subagents and the MCP servers; a later run starts the
// servers again.
func (e *Engine) Close() error {
	var errs []error
	if c, ok := e.cfg.Subagents.(io.Closer); ok {
		errs = append(errs, c.Close())
	}
	if e.cfg.MCP != nil {
		errs = append(errs, e.cfg.MCP.Close())
	}
	e.transports.m.Range(func(_, tr any) bool { tr.(*http.Transport).CloseIdleConnections(); return true }) //nolint:forcetypeassert // as transports.get

	return errors.Join(errs...)
}

// Priority reports whether the configured provider accepts priority
// processing (Provider.Priority).
func (e *Engine) Priority() bool {
	p, err := e.provider(e.cfg.Provider)

	return err == nil && p.Priority
}

// startKey carries a run's options and event sink to the backend.
type (
	startKey   struct{}
	startValue struct {
		opts engine.Options
		emit func(core.Event)
	}
)

func (e *Engine) Start(ctx context.Context, req core.Request, opts engine.Options, sink core.Sink) (engine.Run, error) {
	// Before uagent's preflight, which blocks an expired token.
	if err := codexauth.BeforeRun(ctx, req.Provider, e.cfg.Getenv); err != nil {
		return nil, fmt.Errorf("failed to start run: %w", err)
	}
	ls := &lockedSink{sink: sink, tap: e.transcript(req.SessionID).observe}
	r, err := e.h.Start(context.WithValue(ctx, startKey{}, startValue{opts: opts, emit: ls.emit}), req, ls.emit)
	if err != nil {
		return nil, fmt.Errorf("failed to start run: %w", err)
	}
	a, ok := r.Process().(*agent)
	if !ok {
		r.Kill()

		return nil, fmt.Errorf("unexpected process %T", r.Process())
	}

	return &run{run: r, agent: a, subagents: e.cfg.Subagents, sessionID: req.SessionID}, nil
}

// transcript is the session's auto-review transcript.
func (e *Engine) transcript(sessionID string) *transcript {
	t, _ := e.transcripts.LoadOrStore(sessionID, newTranscript())

	return t.(*transcript) //nolint:forcetypeassert // the map holds only transcripts
}

func (e *Engine) provider(name string) (Provider, error) {
	for _, p := range e.cfg.Providers {
		if p.Name == name {
			return p, nil
		}
	}

	return Provider{}, fmt.Errorf("unsupported provider %q", name)
}

// run is a live embedded run.
type run struct {
	run       *harness.Run
	agent     *agent
	subagents engine.Subagents
	sessionID string
}

func (r *run) Send(in core.UserInput) error     { return r.agent.Send(in) }
func (r *run) SetEffort(effort string) error    { return r.agent.SetEffort(effort) }
func (r *run) SetModel(model string) error      { return r.agent.SetModel(model) }
func (r *run) SetServiceTier(tier string) error { return r.agent.SetServiceTier(tier) }
func (r *run) SetAdaptiveEffort(v string) error { return r.agent.SetAdaptiveEffort(v) }
func (r *run) SetMode(m approval.Mode) error    { return r.agent.SetMode(m) }
func (r *run) Compact(focus string) error       { return r.agent.Compact(focus) }
func (r *run) Clear() error                     { return r.agent.Clear() }

// Interrupt stops the run and its session's subagents' live runs, as the
// user expects of an interrupt.
func (r *run) Interrupt() {
	if r.subagents != nil {
		r.subagents.Interrupt(r.sessionID)
	}
	r.run.Interrupt()
}
func (r *run) Kill() { r.run.Kill() }

func (r *run) Wait() (core.Result, error) {
	result, err := r.run.Wait()
	if err != nil {
		return result, fmt.Errorf("failed to finish run: %w", err)
	}

	return result, nil
}

// lockedSink lets the engine add its own events to a run's stream: one
// goroutine at a time, and none after RunFinished.
type lockedSink struct {
	mu       sync.Mutex
	sink     core.Sink
	tap      func(core.Event) // sees every event first
	finished bool
}

func (s *lockedSink) emit(e core.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finished {
		return
	}
	if _, ok := e.(core.RunFinished); ok {
		s.finished = true
	}
	if s.tap != nil {
		s.tap(e)
	}
	s.sink(e)
}

// Subagents is the engine's subagents, for a view that follows one.
func (e *Engine) Subagents() engine.Subagents { return e.cfg.Subagents }
