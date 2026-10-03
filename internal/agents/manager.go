package agents

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/viktordanov/uah/internal/contextusage"
	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/instructions"
	"github.com/viktordanov/uah/internal/mcp"
	"github.com/viktordanov/uah/internal/session"
)

// Defaults, Codex's.
const (
	DefaultMaxThreads = 4
	DefaultMaxDepth   = 1
)

// MaxDepth is the deepest children nest: subagents never start
// subagents. It is a rule, not a setting; New clamps a higher MaxDepth.
const MaxDepth = 1

// Config configures the subagents of one uah process.
type Config struct {
	// MaxThreads is how many children a session tree keeps open at once.
	MaxThreads int
	// MaxDepth is how deep children nest: 1 (the most, see the MaxDepth
	// constant) means children cannot spawn, and 0 offers no tools at all
	// (subagents are off), while past calls still get an answer.
	MaxDepth int
	// Model and Effort are the configured defaults for children.
	Model  string
	Effort string
	// ReviewModel is /review's model, Codex's review_model ("": the
	// session's).
	ReviewModel string
	Roles       []Role
	// Validate refuses a model spawn_agent may not use, as Codex checks
	// the model against its catalog (internal/models.Validate); nil
	// accepts any.
	Validate func(ctx context.Context, model string) error
}

// Manager implements engine.Subagents. Children are ordinary sessions on the
// parent's engine, recorded with SourceSubagent and their parent's ID.
type Manager struct {
	cfg Config

	mu  sync.Mutex
	eng engine.Engine
	// tmpl is how the process opens its sessions; children open the same
	// way (see childOptions).
	tmpl     session.Options
	parents  map[string]engine.AgentParent
	children map[string]*child
	// parentIDs and forks remember the sidecars' parents and records' forks.
	parentIDs map[string]string
	forks     map[string]bool
	// outboxes deliver each parent's updates in order (notify, forward).
	outboxes map[string]*outbox
	// changed is closed and replaced whenever a child's status changes.
	changed chan struct{}
	// beforeSubmit, set by tests, runs before a message goes to a child.
	beforeSubmit func(message string)
}

// New returns a manager; Bind gives it the engine children run on.
func New(cfg Config) *Manager {
	if cfg.MaxThreads <= 0 {
		cfg.MaxThreads = DefaultMaxThreads
	}
	cfg.MaxDepth = min(cfg.MaxDepth, MaxDepth)

	return &Manager{
		cfg:     cfg,
		parents: map[string]engine.AgentParent{}, children: map[string]*child{}, parentIDs: map[string]string{}, forks: map[string]bool{}, outboxes: map[string]*outbox{}, changed: make(chan struct{}),
	}
}

// Bind sets the engine children run on, the parent's, and the options the
// process opens its sessions with; children open with the same, less what
// makes them children (see childOptions). It tells the hooks which
// sessions are children, so these fire subagent hooks only.
func (m *Manager) Bind(eng engine.Engine, template session.Options) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.eng, m.tmpl = childEngine{eng}, template
	template.Hooks.SetParents(m.parentID)
}

// parentID is a session's parent, "" for a root session.
func (m *Manager) parentID(id string) string {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.parentOf(id)
}

// template is the process's session options.
func (m *Manager) template() session.Options {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.tmpl
}

// childEngine is the parent's engine without Close, so closing a child
// leaves the shared engine and its MCP servers running.
type childEngine struct{ engine.Engine }

// MCPServers reports the shared engine's MCP servers.
func (e childEngine) MCPServers() []mcp.ServerStatus {
	if l, ok := e.Engine.(engine.MCPLister); ok {
		return l.MCPServers()
	}

	return nil
}

// ContextUsage is /context for a child: its own last request on the
// shared engine.
func (e childEngine) ContextUsage(sessionID string) (contextusage.Usage, bool) {
	if r, ok := e.Engine.(engine.ContextReporter); ok {
		return r.ContextUsage(sessionID)
	}

	return contextusage.Usage{}, false
}

// Forget passes a child session's close to the engine, which keeps
// per-session state (engine.Forgetter).
func (e childEngine) Forget(sessionID string) {
	if f, ok := e.Engine.(engine.Forgetter); ok {
		f.Forget(sessionID)
	}
}

// Attach records the parent's run and offers the tools when the session
// may spawn: its depth is below MaxDepth. A forked child is offered them at
// any depth, as its parent was, so its requests keep its parent's prefix;
// its spawn calls are refused at the limit, as Codex refuses them.
func (m *Manager) Attach(p engine.AgentParent) []engine.AgentTool {
	m.mu.Lock()
	m.parents[p.SessionID] = p
	offer := m.cfg.MaxDepth > 0 && (m.depth(p.SessionID) < m.cfg.MaxDepth || m.forked(p.SessionID))
	m.mu.Unlock()
	if !offer {
		return nil
	}

	return m.definitions()
}

// depth is how many ancestors a session has, from the live children and
// then the sidecars, so a resumed child keeps its depth. It holds m.mu.
func (m *Manager) depth(id string) int {
	n, _ := m.lineage(id)

	return n
}

// treeRoot is the top session of id's tree. It holds m.mu.
func (m *Manager) treeRoot(id string) string {
	_, root := m.lineage(id)

	return root
}

// lineage walks up from id to the top of its tree, through the live
// children and then the sidecars, and returns how many steps it took and
// where it ended. A sidecar is read once per session and remembered, since
// a session's parent never changes. It holds m.mu.
func (m *Manager) lineage(id string) (int, string) {
	n := 0
	for range 32 {
		parent := m.parentOf(id)
		if parent == "" {
			break
		}
		n, id = n+1, parent
	}

	return n, id
}

// parentOf is a session's parent, "" for a root. It holds m.mu.
func (m *Manager) parentOf(id string) string {
	if c, ok := m.children[id]; ok {
		return c.parent
	}
	if parent, ok := m.parentIDs[id]; ok {
		return parent
	}
	parent := ""
	if sc, found, err := session.ReadSidecar(m.tmpl.SessionsDir, id); err == nil && found && sc.Source == session.SourceSubagent {
		parent = sc.Parent
	}
	m.parentIDs[id] = parent

	return parent
}

// Forked reports whether a session is a child started with fork_context.
// The engine offers such a child its parent's request_user_input, which
// only the main agent may call, so the tools stay its parent's.
func (m *Manager) Forked(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.forked(id)
}

// forked reports whether a session is a child started with fork_context,
// from the live children and then the agent records. It holds m.mu.
func (m *Manager) forked(id string) bool {
	if c, ok := m.children[id]; ok {
		return c.forked
	}
	if fork, ok := m.forks[id]; ok {
		return fork
	}
	rec, err := readRecord(m.tmpl.SessionsDir, id)
	m.forks[id] = err == nil && rec.Fork

	return m.forks[id]
}

// openIn counts the open children in root's tree. It holds m.mu.
func (m *Manager) openIn(root string) int {
	n := 0
	for id, c := range m.children {
		if !c.closed && m.treeRoot(id) == root {
			n++
		}
	}

	return n
}

// role finds an agent type; "" and "default" are the default agent.
func (m *Manager) role(name string) (Role, error) {
	if name == "" || name == defaultRole {
		return Role{}, nil
	}
	i := slices.IndexFunc(m.cfg.Roles, func(r Role) bool { return r.Name == name })
	if i < 0 {
		names := []string{defaultRole}
		for _, r := range m.cfg.Roles {
			names = append(names, r.Name)
		}

		return Role{}, fmt.Errorf("unknown agent_type %q (want one of %s)", name, strings.Join(names, ", "))
	}

	return m.cfg.Roles[i], nil
}

// childOptions are a child's session options: the process's, as the root
// session opens with, and the parent's settings as they are now (see
// liveSettings). Only what makes it a child differs: its ID, its sidecar's
// source and parent, approvals asked through the parent, the parent's
// permission mode, and its model and effort: the spawn call's, else the
// role's, else the configured defaults, else the parent's. A fork takes
// the spawn call's, else the parent's: its request is to share the
// parent's prefix, which neither a role nor a default may change. A
// resumed child (saved, from its sidecar) gets back the model, effort,
// fast mode, and adaptive effort it last used, as a resumed root session
// does, and the parent's permission mode, as Codex gives a resumed agent
// its parent turn's approval policy and sandbox.
// Its system prompt is the parent's, then the role's instructions; Codex's
// note that its final answer reaches the parent follows its task instead
// (firstNote), so the prompt and the parent's share a cache.
// Hooks are the same, in a runner of its own. It holds m.mu.
func (m *Manager) childOptions(p engine.AgentParent, c *child, role Role, rec record, saved *session.Saved) session.Options {
	opts := m.tmpl
	opts.ID, opts.Resumed, opts.Source, opts.Parent = c.id, saved != nil, session.SourceSubagent, c.parent
	opts.Ask, opts.Hooks = m.askFor(c), opts.Hooks.Clone()
	// A child's text never streams: neither its parent nor its view shows
	// it as it arrives.
	opts.Stream = false
	if rec.Fork { // the parent's request already carries its role's settings
		role = Role{Name: role.Name, NicknameCandidates: role.NicknameCandidates}
	}
	s := opts.Settings.WithRequest(p.Request)
	live := liveSettings(p)
	s.Model, s.Effort, s.AdaptiveEffort = live.Model, live.Effort, live.AdaptiveEffort
	s.ServiceTier = m.serviceTier(live.ServiceTier, role)
	if live.Mode != "" {
		s = s.WithMode(live.Mode)
	}
	switch {
	case saved != nil && *saved != session.Saved{}:
		m.restore(&s, *saved)
	case rec.Fork:
		s.Model, s.Effort = first(rec.Model, s.Model), first(rec.Effort, s.Effort)
	default:
		s.Model = first(rec.Model, role.Model, m.cfg.Model, s.Model)
		s.Effort = first(rec.Effort, role.Effort, m.cfg.Effort, s.Effort)
	}
	s.SystemPrompt = first(s.SystemPrompt, instructions.DefaultPrompt)
	if role.DeveloperInstructions != "" {
		s.SystemPrompt += "\n\n" + strings.TrimSpace(role.DeveloperInstructions)
	}
	opts.Settings = s

	return opts
}

// liveSettings are the parent's settings now: its session's, with the
// changes made during its run, else its run's request's.
func liveSettings(p engine.AgentParent) engine.LiveSettings {
	if p.Settings != nil {
		return p.Settings()
	}

	return engine.LiveSettings{Model: p.Request.Model, Effort: p.Request.Effort}
}

// restore gives a resumed child the settings its sidecar saved, but its
// permission mode: the model when it is on the parent's provider (the
// child runs on the parent's engine), the effort, fast mode when the
// engine serves it, and adaptive effort. It holds m.mu.
func (m *Manager) restore(s *session.Settings, saved session.Saved) {
	if saved.Provider == "" || saved.Provider == s.Provider {
		s.Model = first(saved.Model, s.Model)
	}
	s.Effort = first(saved.Effort, s.Effort)
	s.ServiceTier = ""
	if saved.Fast && m.eng != nil && m.eng.Priority() {
		s.ServiceTier = TierPriority
	}
	s.AdaptiveEffort = first(saved.AdaptiveEffort, s.AdaptiveEffort)
}

// scope narrows a child's tools and pre-approves its actions as its role
// says, on an engine that can (engine.Scoper). A fork keeps its parent's
// tools, so it has none.
func (m *Manager) scope(id string, role Role, rec record) {
	m.mu.Lock()
	ce, ok := m.eng.(childEngine)
	m.mu.Unlock()
	if !ok {
		return
	}
	if s, ok := ce.Engine.(engine.Scoper); ok {
		if rec.Fork {
			role = Role{}
		}
		s.SetScope(id, engine.Scope{Tools: role.Tools, Approve: role.Approve})
	}
}

// serviceTier is the child's service tier: the role's when the engine can
// serve it, else the parent's now. It holds m.mu.
func (m *Manager) serviceTier(parent string, role Role) string {
	switch {
	case role.ServiceTier == TierDefault:
		return ""
	case role.ServiceTier == TierPriority && m.eng != nil && m.eng.Priority():
		return TierPriority
	}

	return parent
}

// subtree is c and its open descendants, parents first. It holds m.mu.
func (m *Manager) subtree(c *child) []*child {
	out := []*child{c}
	for i := 0; i < len(out); i++ {
		for _, k := range m.children {
			if k.parent == out[i].id && !k.closed {
				out = append(out, k)
			}
		}
	}

	return out
}

// Interrupt stops the live runs of the parent's children and their own
// children. They stay open: send_input starts them again.
func (m *Manager) Interrupt(parentID string) {
	m.mu.Lock()
	var stop []*child
	for _, c := range m.children {
		if c.parent == parentID && !c.closed {
			stop = append(stop, c)
		}
	}
	m.mu.Unlock()
	for _, c := range stop {
		m.interruptTree(c)
	}
}

// Close closes every child; the engine calls it when a session on it
// closes. The manager stays usable for the engine's next session.
func (m *Manager) Close() error {
	m.mu.Lock()
	var open []*child
	for _, c := range m.children {
		if !c.closed {
			c.closed = true
			c.cancelAsks()
			open = append(open, c)
		}
	}
	m.mu.Unlock()
	var errs []error
	for _, c := range open {
		if s := c.session(m); s != nil {
			if err := s.Close(); err != nil && !errors.Is(err, session.ErrClosed) {
				errs = append(errs, fmt.Errorf("failed to close agent %s: %w", c.nickname, err))
			}
		}
	}

	return errors.Join(errs...)
}

func first(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}

	return ""
}
