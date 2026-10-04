package app

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/viktordanov/uah/internal/agents"
	"github.com/viktordanov/uah/internal/approval"
	"github.com/viktordanov/uah/internal/compaction"
	"github.com/viktordanov/uah/internal/config"
	"github.com/viktordanov/uah/internal/contextprep"
	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/engine/embedded"
	"github.com/viktordanov/uah/internal/hooks"
	"github.com/viktordanov/uah/internal/instructions"
	"github.com/viktordanov/uah/internal/mcp"
	"github.com/viktordanov/uah/internal/models"
	"github.com/viktordanov/uah/internal/review"
	"github.com/viktordanov/uah/internal/sandbox"
	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/internal/store"
	"github.com/viktordanov/uah/internal/toolpolicy"
	planusage "github.com/viktordanov/uah/internal/usage"
)

// Result is everything needed to open a session.
type Result struct {
	StateDir string
	Engine   engine.Engine
	Options  session.Options
	Config   config.Config
	// Models is the session provider's model catalog, loaded from the cache.
	Models *models.Manager
	// Usage reads the subscription's usage for the session's provider; it
	// sends nothing until asked.
	Usage planusage.Reader
	// Sandbox is the policy the session's commands run under, with
	// absolute paths.
	Sandbox sandbox.Policy
}

// Setup resolves the inputs against the resumed session (in.SessionRef) and
// the configuration, loads instructions and hooks, and builds the engine.
// Diagnostic logs go to logOutput.
func Setup(ctx context.Context, in Inputs, logOutput io.Writer) (Result, error) {
	stateDir, err := filepath.Abs(in.StateDir)
	if err != nil {
		return Result{}, fmt.Errorf("failed to resolve state dir: %w", err)
	}
	var resumed session.Info
	opts := session.Options{}
	if in.SessionRef != "" {
		info, err := findResumed(ctx, stateDir, in.SessionRef)
		if err != nil {
			return Result{}, err
		}
		resumed, opts.ID, opts.Resumed, opts.FirstPrompt = info, info.ID, true, info.FirstPrompt
	}
	if in.NewSessionID != "" {
		if opts.ID, err = newSessionID(stateDir, in); err != nil {
			return Result{}, err
		}
	}
	// Resolve then takes the absolute workspace as if it were the flag.
	if in.Workspace, err = filepath.Abs(workspaceFor(in, resumed)); err != nil {
		return Result{}, fmt.Errorf("failed to resolve workspace: %w", err)
	}
	cfg, _, err := config.Load(in.ConfigPath, in.Workspace)
	if err != nil {
		return Result{}, usage(err)
	}
	if notice := config.ProjectMoveNotice(in.Workspace); notice != "" {
		opts.Notices = append(opts.Notices, notice)
	}
	r, err := Resolve(in, resumed, cfg)
	if err != nil {
		return Result{}, err
	}
	if err := readCompactPrompt(&r); err != nil {
		return Result{}, err
	}
	if err := readReviewPolicy(&r); err != nil {
		return Result{}, err
	}
	base, err := readModelInstructions(cfg)
	if err != nil {
		return Result{}, err
	}
	var text string
	if r.Instructions {
		if opts.Instructions, text, err = loadInstructions(in.Workspace, cfg); err != nil {
			return Result{}, err
		}
	}
	// The date is fixed when the session opens, so every request of the
	// session sends the same system message and hits the prompt cache.
	env := instructions.LocalEnvironment(in.Workspace, RealShell(), time.Now(), os.Getenv)
	r.Settings.SystemPrompt = instructions.HostPrompt(questionText(base, r), text, env.String())
	catalog := NewModels(stateDir, r.Settings, os.Getenv)
	if r.DefaultModel {
		// As Codex picks its default: the provider's list, cached for five
		// minutes, with the refresh bounded to five seconds.
		r.SettleModel(catalog.Catalog(ctx, catalog.Provider(), models.OnlineIfUncached))
	} else {
		catalog.Catalog(ctx, catalog.Provider(), models.Offline) // the cache only, no network
	}
	opts.Settings, opts.Yolo, opts.Goals, opts.Tools = r.Settings, in.Yolo, r.Goals, r.Tools
	opts.Notices = append(opts.Notices, modelNotices(catalog.Cached(r.Settings.Provider), r)...)
	// The session's own files go to runDir; the model cache stays shared.
	runDir := cmp.Or(in.RunStateDir, stateDir)
	opts.SessionsDir = filepath.Join(runDir, "sessions")
	if opts.Hooks, err = loadHooks(cfg, in.Workspace, r.Tools.Restricted()); err != nil {
		return Result{}, err
	}
	r.Sandbox = absPolicy(r.Sandbox, in.Workspace)
	// The sandbox scripts run outside the sandbox, so they live in the
	// state directory, never in runDir (an ephemeral run's is under
	// $TMPDIR, which sandboxed commands write), and stay read-only to
	// sandboxed commands and patches wherever the state directory is.
	sandboxDir := filepath.Join(stateDir, "sandbox")
	r.Sandbox.ReadOnly = append(r.Sandbox.ReadOnly, sandboxDir)
	logger := slog.New(slog.NewTextHandler(logOutput, &slog.HandlerOptions{Level: LogLevels[in.LogLevel]}))
	servers, err := mcpManager(cfg, in.Workspace, logger, in.ConfigPath)
	if err != nil {
		return Result{}, err
	}
	approver, err := newApprover(r, cfg, in.Workspace)
	if err != nil {
		return Result{}, err
	}
	subagents := newAgents(r, cfg, in.Workspace, &opts, catalog)
	eng := newEngine(r, runDir, sandboxDir, logger, parts{servers: servers, approver: approver, subagents: subagents, models: catalog, context: ContextSettings(in.ConfigPath, cfg), askUser: in.Interactive && r.RequestUserInput}, &opts)
	subagents.Bind(eng, opts) // children open exactly as this session does
	opts.Shell = userShell(r, cfg, sandboxDir, approver)

	return Result{StateDir: stateDir, Engine: eng, Options: opts, Config: cfg, Models: catalog, Usage: NewUsage(r.Settings, os.Getenv), Sandbox: r.Sandbox}, nil
}

// loadHooks builds the hook runner for the configured hooks (nil when there
// are none). With failClosed, as in a session under a tool policy, a
// PreToolUse or PermissionRequest hook that fails blocks its call.
func loadHooks(cfg config.Config, workspace string, failClosed bool) (*hooks.Runner, error) {
	list, err := cfg.HookList()
	if err != nil {
		return nil, usage(err)
	}
	if len(list) == 0 {
		return nil, nil //nolint:nilnil // a nil runner runs no hooks
	}
	trust, err := hooks.LoadTrust(HookTrustFile())
	if err != nil {
		return nil, usage(err)
	}
	runner, err := hooks.New(list, trust, workspace)
	if err != nil {
		return nil, usage(err)
	}
	if failClosed {
		runner.FailClosed()
	}

	return runner, nil
}

// parts are what Setup builds for the engine.
type parts struct {
	servers   *mcp.Manager
	approver  *approval.Approver
	subagents *agents.Manager
	models    *models.Manager
	context   contextprep.Settings
	// askUser offers request_user_input (Inputs.Interactive).
	askUser bool
}

// newEngine builds the embedded engine for the resolved settings, with the
// run's state in stateDir and the sandbox scripts in sandboxDir.
func newEngine(r Resolved, stateDir, sandboxDir string, logger *slog.Logger, p parts, opts *session.Options) engine.Engine {
	ecfg := embedded.Config{
		StateDir: stateDir, MaxDisk: r.MaxDisk, Logger: logger, Provider: r.Settings.Provider, Hooks: opts.Hooks,
		Sandbox: &r.Sandbox, SandboxDir: sandboxDir, Env: r.Env, MCP: p.servers, Approver: p.approver, Models: p.models,
		ContextPreparation: r.ContextPreparation, ContextModules: p.context, EffortUpdates: r.EffortUpdates,
		AutoReview: r.ApprovalsReviewer == review.ReviewerAuto, Review: r.Review, WebSearch: r.WebSearch == WebSearchLive, Verbosity: r.Verbosity,
		InstructionFiles: instructionFiles(opts.Instructions), InstructionsOff: !r.Instructions,
		Compaction: r.Compaction, ContextWindow: r.Settings.ContextWindow,
		BeforeCompact: preCompactHook(opts.Hooks, r.Settings), Subagents: p.subagents, AskUser: p.askUser,
		Goals: !r.Goals.Disabled, ReturnMemory: true, Tools: r.Tools, NoSkills: r.NoSkills,
	}

	return embedded.New(ecfg)
}

// preCompactHook runs PreCompact hooks before each compaction; a block
// cancels it. It is nil without such hooks.
func preCompactHook(runner *hooks.Runner, s session.Settings) func(context.Context, string, compaction.Trigger) error {
	if !runner.Has(hooks.PreCompact, "") {
		return nil
	}

	return func(ctx context.Context, sessionID string, t compaction.Trigger) error {
		d := runner.Run(ctx, hooks.Input{
			Event: hooks.PreCompact, SessionID: sessionID, Cwd: s.Workspace, Model: s.Model, Effort: s.Effort, Trigger: string(t),
		})
		if d.Block {
			return fmt.Errorf("a PreCompact hook stopped the compaction: %s", d.Reason)
		}

		return nil
	}
}

// instructionFiles are the loaded instruction files' paths, for /context.
func instructionFiles(loaded *session.InstructionsLoaded) []string {
	if loaded == nil {
		return nil
	}

	return loaded.Files
}

// RealShell is the user's shell for commands: $SHELL, or /bin/sh.
func RealShell() string {
	if s := strings.TrimSpace(os.Getenv("SHELL")); s != "" {
		return s
	}

	return "/bin/sh"
}

// absPolicy makes the policy's paths absolute: the workspace, and writable
// roots with ~ for the home directory and others relative to the workspace.
func absPolicy(p sandbox.Policy, workspace string) sandbox.Policy {
	p.Workspace = workspace
	roots := make([]string, 0, len(p.WritableRoots))
	for _, r := range p.WritableRoots {
		if rest, ok := strings.CutPrefix(r, "~"); ok && (rest == "" || strings.HasPrefix(rest, "/")) {
			if home, err := os.UserHomeDir(); err == nil {
				r = home + rest
			}
		}
		if !filepath.IsAbs(r) {
			r = filepath.Join(workspace, r)
		}
		roots = append(roots, filepath.Clean(r))
	}
	p.WritableRoots = roots

	return p
}

// findResumed finds the session to resume, with the tool policy it ran
// under.
func findResumed(ctx context.Context, stateDir, ref string) (session.Info, error) {
	info, err := FindSession(ctx, stateDir, ref)
	if err != nil {
		return session.Info{}, err
	}
	info.Tools, err = savedTools(stateDir, info.ID)

	return info, err
}

// savedTools reads the tool policy a session ran under from its sidecar.
// The session list skips a sidecar it cannot read, but a resume must not:
// it would drop the policy and widen the session's tools. A session
// without a sidecar ran under none.
func savedTools(stateDir, id string) (*toolpolicy.Policy, error) {
	sc, _, err := session.ReadSidecar(filepath.Join(stateDir, "sessions"), id)
	if err != nil {
		return nil, fmt.Errorf("cannot resume session %s: its sidecar, which keeps its tool policy, is unreadable: %w", id, err)
	}

	return sc.Tools, nil
}

// HookTrustFile records the project hook commands the user approved.
func HookTrustFile() string { return filepath.Join(config.Dir(), "trusted-hooks.json") }

// FindSession finds a session by exact ID or unique prefix.
func FindSession(ctx context.Context, stateDir, ref string) (session.Info, error) {
	infos, err := store.List(ctx, stateDir)
	if err == nil {
		infos, err = session.WithUnused(stateDir, infos) // a session that never ran resumes under its ID
	}
	if err != nil {
		return session.Info{}, err
	}
	var matches []session.Info
	for _, info := range infos {
		if info.ID == ref {
			return info, nil
		}
		if strings.HasPrefix(info.ID, ref) {
			matches = append(matches, info)
		}
	}
	switch len(matches) {
	case 0:
		return session.Info{}, usage(fmt.Errorf("no session matches %q (see uah sessions)", ref))
	case 1:
		return matches[0], nil
	}

	return session.Info{}, usage(fmt.Errorf("%q matches %d sessions; use more of the ID", ref, len(matches)))
}

// readModelInstructions reads model_instructions_file, the base
// instructions in place of uah's default prompt, or returns "" when it
// questionText is the base instructions as the question tool's setting
// wants them: with the tool off, the default prompt (or a copy of it in
// model_instructions_file) says to ask in the final message, as before the
// tool ("": the default prompt as it is).
func questionText(base string, r Resolved) string {
	if r.RequestUserInput {
		return base
	}

	return instructions.WithoutQuestionTool(cmp.Or(base, instructions.DefaultPrompt))
}

// is not set. As in Codex, a missing or empty file is an error.
func readModelInstructions(cfg config.Config) (string, error) {
	if cfg.ModelInstructionsFile == "" {
		return "", nil
	}

	return readPrompt("model_instructions_file", cfg.ModelInstructionsFile)
}

// loadInstructions discovers and assembles instruction files, returning the
// event to report and the assembled files ("" when there are none).
func loadInstructions(workspace string, cfg config.Config) (*session.InstructionsLoaded, string, error) {
	fallbacks, markers, maxBytes := cfg.InstructionOptions()
	codexHome := os.Getenv("CODEX_HOME")
	if codexHome == "" {
		if home, err := os.UserHomeDir(); err == nil {
			codexHome = filepath.Join(home, ".codex")
		}
	}
	userFiles := []string{filepath.Join(config.Dir(), "AGENTS.md")}
	if codexHome != "" {
		userFiles = append(userFiles, filepath.Join(codexHome, "AGENTS.md"))
	}
	files, err := instructions.Discover(workspace, userFiles, instructions.Options{FallbackFilenames: fallbacks, RootMarkers: markers})
	if err != nil {
		return nil, "", fmt.Errorf("failed to find instructions: %w", err)
	}
	if len(files) == 0 {
		return nil, "", nil
	}
	text, used, truncated, err := instructions.Assemble(files, maxBytes)
	if err != nil {
		return nil, "", fmt.Errorf("failed to read instructions: %w", err)
	}
	paths := make([]string, 0, len(used))
	for _, f := range used {
		paths = append(paths, f.Path)
	}

	return &session.InstructionsLoaded{Files: paths, Bytes: len(text), Truncated: truncated}, text, nil
}
