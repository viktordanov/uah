package app

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
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
	"github.com/viktordanov/uah/internal/shellenv"
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
	_ = writeStateIgnore(stateDir) // best effort: searches from ~ skip the sessions
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
	layers, err := config.LoadLayers(in.ConfigPath, in.Workspace)
	if err != nil {
		return Result{}, usage(err)
	}
	cfg := layers.Merged()
	opts.Notices = append(opts.Notices, layers.Notices...)
	opts.Notices = append(opts.Notices, openNotices(in.Workspace)...)
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
	if err := loadCatalog(ctx, &r, catalog); err != nil {
		return Result{}, err
	}
	opts.Settings, opts.Yolo, opts.Goals, opts.Tools = r.Settings, in.Yolo, r.Goals, r.Tools
	cached := catalog.Cached(r.Settings.Provider)
	opts.Notices = append(opts.Notices, modelNotices(cached, r)...)
	opts.Notices = append(opts.Notices, advisorNotices(cached, r)...)
	// The session's own files go to runDir; the model cache stays shared.
	runDir := cmp.Or(in.RunStateDir, stateDir)
	opts.SessionsDir = filepath.Join(runDir, "sessions")
	if opts.Hooks, err = loadHooks(cfg, in.Workspace, r.Tools.Restricted()); err != nil {
		return Result{}, err
	}
	r.Sandbox = absPolicy(r.Sandbox, in.Workspace)
	var refused string
	if r.Advisor, refused = pinAdvisorProgram(r.Advisor, r.Sandbox); refused != "" {
		opts.Notices = append(opts.Notices, refused)
	}
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
		InstructionFiles: instructionFiles(opts.Instructions), InstructionsOff: !r.Instructions, MaxWait: r.MaxWait,
		Compaction: r.Compaction, ContextWindow: r.Settings.ContextWindow, StreamIdleTimeout: r.StreamIdle, ResponsesWebSocket: r.WebSocket,
		BeforeCompact: preCompactHook(opts.Hooks, r.Settings), Subagents: p.subagents, AskUser: p.askUser,
		Goals: !r.Goals.Disabled, ReturnMemory: true, Tools: r.Tools, NoSkills: r.NoSkills, NoSkillMentions: r.NoSkillMentions, NoToolBridge: r.NoToolBridge,
		Advisor: r.Advisor,
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

// RealShell is the user's shell for commands: $SHELL, else the login
// shell, else /bin/sh (shellenv.Resolve).
func RealShell() string { return shellenv.Current(os.Getenv).Path }

// openNotices are the warnings a session opens with about the workspace
// and the environment: a project file to move, and the shell picked when
// $SHELL could not say.
func openNotices(workspace string) []string {
	var out []string
	for _, n := range []string{config.ProjectMoveNotice(workspace), shellNotice(shellenv.Current(os.Getenv))} {
		if n != "" {
			out = append(out, n)
		}
	}

	return out
}

// shellNotice tells the user which shell runs commands when $SHELL could
// not say, as when a service starts uah without the user's environment;
// "" when $SHELL did.
func shellNotice(sh shellenv.Shell) string {
	what := "SHELL is unset"
	if sh.Env != "" {
		what = fmt.Sprintf("SHELL (%s) is not an executable file", sh.Env)
	}
	switch sh.Source {
	case shellenv.FromLogin:
		return fmt.Sprintf("%s; commands run in %s (your login shell)", what, sh.Path)
	case shellenv.FromDefault:
		return fmt.Sprintf("%s and the login shell could not be read; commands run in %s", what, sh.Path)
	}

	return ""
}

// pinAdvisorProgram is the advisor with Program, the path its command's
// program runs as: found in PATH once, here, so a later PATH lookup cannot
// pick another, and with its symlinks resolved, so no link on the way can
// be swapped later. Command[0] stays the name the command sees. A program
// the agent could change (sandbox.Policy.Changeable: in the workspace, a
// writable root, or the temporary directory, as the workspace-write mode
// has it, also when the session starts in another), checked by the path
// found and by its real path, turns the command backend off, since the
// command runs outside the sandbox: a link the agent could change picks
// the program as surely as the file does. refused is the notice that says
// so. The engine checks the real path again at each call, with the
// session's grants. A program PATH lacks has no Program, and the engine
// runs nothing (advisorCommandNotices warns of it).
func pinAdvisorProgram(a embedded.Advisor, p sandbox.Policy) (pinned embedded.Advisor, refused string) {
	if a.Off || len(a.Command) == 0 {
		return a, ""
	}
	program := a.Command[0]
	if !filepath.IsAbs(program) {
		found, err := exec.LookPath(program)
		if err != nil {
			return a, "" // the notice warns of it, and the engine runs no name
		}
		if program, err = filepath.Abs(found); err != nil {
			return a, ""
		}
	}
	resolved := sandbox.ResolvePath(program)
	p.Mode = sandbox.WorkspaceWrite
	for _, path := range []string{program, resolved} {
		if p.Changeable(path) {
			a.Command = nil
			a.Refused = fmt.Sprintf("its program, %s, is where the agent's commands can write", path)

			return a, fmt.Sprintf("advisor.command: %s is where the agent's commands can write, and the advisor runs outside the sandbox, "+
				"so the advisor is off; put the program, and any link to it, outside the workspace and the writable roots", path)
		}
	}
	a.Program = resolved

	return a, ""
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

func loadCatalog(ctx context.Context, r *Resolved, catalog *models.Manager) error {
	if r.Settings.ServiceTier == "ultrafast" {
		c := catalog.Catalog(ctx, catalog.Provider(), models.OnlineIfUncached)
		if r.DefaultModel {
			r.SettleModel(c)
		}
		if !c.OffersTier(r.Settings.Model, "ultrafast") {
			return fmt.Errorf("ultrafast is not offered for %s by the current login", r.Settings.Model)
		}
	} else if r.DefaultModel {
		// As Codex picks its default: the provider's list, cached for five
		// minutes, with the refresh bounded to five seconds.
		r.SettleModel(catalog.Catalog(ctx, catalog.Provider(), models.OnlineIfUncached))
	} else {
		catalog.Catalog(ctx, catalog.Provider(), models.Offline) // the cache only, no network
	}

	return nil
}
