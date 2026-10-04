package mcp

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// implementation is how uah introduces itself to servers.
var implementation = &sdk.Implementation{Name: "uah", Version: "1"}

// Restarts of a server that stopped on its own: at most MaxRestarts in a
// row, the first after Options.RestartDelay (default 1 s) and each later one
// after twice the last, at most 30 s, as Codex backs off reconnecting its
// apps server. A server that ran for stableAfter before it stopped starts
// the count again.
const (
	MaxRestarts         = 5
	DefaultRestartDelay = time.Second
	maxRestartDelay     = 30 * time.Second
	stableAfter         = time.Minute
)

// server is one configured server and its connection.
type server struct {
	name  string
	cfg   ServerConfig
	calls chan struct{} // one slot unless the server takes parallel calls

	// Guarded by Manager.mu.
	state   State
	err     error
	session *sdk.ClientSession
	// raw is the last tool list the server sent. It stays while the server
	// restarts or after it failed, so the tools offered stay the same and
	// the prompt cache holds; calls to them fail with the reason.
	raw     []*sdk.Tool
	prompts []*sdk.Prompt
	caps    *sdk.ServerCapabilities
	auth    *storedAuth // HTTP servers with OAuth
	// readyAt is when the session became ready; restarts counts the
	// restarts since the server last ran for stableAfter.
	readyAt  time.Time
	restarts int
	// settled is closed when a restart or a reconnect ends; nil while none
	// runs. Calls wait on it.
	settled chan struct{}
}

func newServer(name string, cfg ServerConfig, opts Options) *server {
	s := &server{name: name, cfg: cfg, state: StateStarting}
	if cfg.URL != "" && !cfg.usesBearer() && opts.Credentials != nil {
		s.auth = newStoredAuth(name, cfg.URL, opts)
	}
	if !cfg.SupportsParallelToolCalls {
		s.calls = make(chan struct{}, 1)
	}
	if !cfg.IsEnabled() {
		s.state = StateDisabled
	}

	return s
}

// conn is a session that connected and listed what the server offers.
type conn struct {
	session *sdk.ClientSession
	tools   []*sdk.Tool
	prompts []*sdk.Prompt
	caps    *sdk.ServerCapabilities
}

// connect starts one server and lists its tools within the startup timeout.
func (m *Manager) connect(ctx context.Context, s *server) {
	c, err := m.dial(ctx, s)
	m.mu.Lock()
	defer m.mu.Unlock()
	var login *LoginError
	switch {
	case errors.As(err, &login):
		s.state, s.err = StateNeedsLogin, login
	case err != nil:
		s.state, s.err = StateFailed, err
	case ctx.Err() != nil: // closed while starting
		_ = c.session.Close()
	default:
		m.up(ctx, s, c)
	}
}

// dial opens a session within the startup timeout.
func (m *Manager) dial(ctx context.Context, s *server) (conn, error) {
	timeout := s.cfg.StartupTimeout()
	startCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	c, err := m.open(startCtx, ctx, s)
	if errors.Is(startCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
		err = fmt.Errorf("did not start within %s; a slow server needs a larger startup_timeout_sec", timeout)
	}

	return c, err
}

// up makes the server ready with the session and watches it. The tools
// offered change when the server lists other tools than before, from the
// next run on. The caller holds mu.
func (m *Manager) up(ctx context.Context, s *server, c conn) {
	if old := s.session; old != nil && old != c.session {
		go func() { _ = old.Close() }() // it stopped, expired, or was refused; its watch sees it replaced
	}
	changed := !sameTools(s.raw, c.tools)
	s.state, s.err, s.session, s.raw, s.prompts, s.caps, s.readyAt = StateReady, nil, c.session, c.tools, c.prompts, c.caps, time.Now()
	if changed {
		m.retool()
	}
	go m.watch(ctx, s, c.session)
}

// watch notices a connection that ended while the server was ready: a
// stdio server exited, or an HTTP server went away. The server restarts
// with backoff, at most MaxRestarts times in a row; Codex leaves it failed.
func (m *Manager) watch(ctx context.Context, s *server, session *sdk.ClientSession) {
	m.stopped(ctx, s, session, session.Wait())
}

// stopped handles a session that ended, once: from watch, or from a call
// that found the connection gone first.
func (m *Manager) stopped(ctx context.Context, s *server, session *sdk.ClientSession, werr error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s.session != session || s.state != StateReady || ctx.Err() != nil {
		return
	}
	if time.Since(s.readyAt) >= stableAfter {
		s.restarts = 0
	}
	stopped := fmt.Errorf("the server stopped: %w", errOrEOF(werr))
	m.opts.Logger.LogAttrs(ctx, slog.LevelWarn, "MCP server stopped",
		slog.String("server", s.name),
		slog.Int("restarts", s.restarts),
		slog.Any("err", stopped))
	if s.restarts >= MaxRestarts {
		s.state, s.err = StateFailed, fmt.Errorf("%w; it stopped after %d restarts in a row, so it stays stopped until /new", stopped, s.restarts)

		return
	}
	s.state, s.err, s.settled = StateRestarting, stopped, make(chan struct{})
	go m.restart(ctx, s)
}

// restart reconnects a server that stopped, waiting longer before each
// attempt. A server that now asks for a login becomes needs_login.
func (m *Manager) restart(ctx context.Context, s *server) {
	for {
		m.mu.Lock()
		s.restarts++
		n := s.restarts
		m.mu.Unlock()
		if !sleep(ctx, m.restartDelay(n)) {
			m.settle(s)

			return
		}
		c, err := m.dial(ctx, s)
		m.mu.Lock()
		var login *LoginError
		switch {
		case ctx.Err() != nil: // closed meanwhile
			if err == nil {
				_ = c.session.Close()
			}
		case errors.As(err, &login):
			s.state, s.err = StateNeedsLogin, login
		case err == nil:
			m.opts.Logger.LogAttrs(ctx, slog.LevelInfo, "MCP server restarted",
				slog.String("server", s.name),
				slog.Int("restart", n))
			m.up(ctx, s, c)
		case n >= MaxRestarts:
			s.state, s.err = StateFailed, fmt.Errorf("the server stopped and did not restart after %d attempts: %w", n, err)
		default:
			s.err = fmt.Errorf("the server stopped; restart %d of %d failed: %w", n, MaxRestarts, err)
			m.mu.Unlock()

			continue
		}
		m.settleLocked(s)
		m.mu.Unlock()

		return
	}
}

// restartDelay is the wait before restart n (from 1).
func (m *Manager) restartDelay(n int) time.Duration {
	d := m.opts.RestartDelay
	for range min(n-1, 10) {
		d *= 2
	}

	return min(d, maxRestartDelay)
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func (m *Manager) settle(s *server) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.settleLocked(s)
}

// settleLocked wakes the calls waiting for a restart or reconnect; the
// caller holds mu.
func (*Manager) settleLocked(s *server) {
	if s.settled != nil {
		close(s.settled)
		s.settled = nil
	}
}

// reconnect opens a new session for an HTTP server whose session expired
// (a 404 for its session ID), as Codex re-initializes it.
func (m *Manager) reconnect(ctx context.Context, s *server, old *sdk.ClientSession) (*sdk.ClientSession, error) {
	m.mu.Lock()
	serversCtx := m.ctx
	m.mu.Unlock()
	if serversCtx == nil {
		return nil, fmt.Errorf("the MCP server %s is not running", s.name)
	}
	startCtx, cancel := context.WithTimeout(ctx, s.cfg.StartupTimeout())
	defer cancel()
	c, err := m.open(startCtx, serversCtx, s)
	if err != nil {
		return nil, fmt.Errorf("failed to reconnect to %s after its session expired: %w", s.name, err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	switch {
	case serversCtx.Err() != nil:
		_ = c.session.Close()

		return nil, fmt.Errorf("the MCP server %s is not running", s.name)
	case s.session != old: // another call reconnected first
		_ = c.session.Close()

		return s.session, nil
	}
	m.up(serversCtx, s, c) //nolint:contextcheck // the servers' lifetime, not the call's

	return c.session, nil
}

// relogin reconnects each needs_login server whose stored login changed
// since it was refused, so a `uah mcp login` in another terminal takes
// effect without /new. It returns a channel per reconnect, closed when it
// ends.
func (m *Manager) relogin() []chan struct{} {
	m.mu.Lock()
	ctx := m.ctx
	auths := map[*server]*storedAuth{}
	for _, s := range m.servers {
		if s.state == StateNeedsLogin && s.auth != nil && s.settled == nil {
			auths[s] = s.auth
		}
	}
	m.mu.Unlock()
	var out []chan struct{}
	for s, a := range auths {
		if !a.loginChanged() {
			continue
		}
		m.mu.Lock()
		if m.closed || s.state != StateNeedsLogin || s.auth != a {
			m.mu.Unlock()

			continue
		}
		s.auth = newStoredAuth(s.name, s.cfg.URL, m.opts)
		s.state, s.err, s.settled = StateStarting, nil, make(chan struct{})
		out = append(out, s.settled)
		m.mu.Unlock()
		go m.redial(ctx, s)
	}

	return out
}

// redial connects a server again after a new login.
func (m *Manager) redial(ctx context.Context, s *server) {
	c, err := m.dial(ctx, s)
	m.mu.Lock()
	defer m.mu.Unlock()
	var login *LoginError
	switch {
	case ctx.Err() != nil:
		if err == nil {
			_ = c.session.Close()
		}
	case errors.As(err, &login):
		s.state, s.err = StateNeedsLogin, login
	case err != nil:
		s.state, s.err = StateFailed, err
	default:
		m.opts.Logger.LogAttrs(ctx, slog.LevelInfo, "MCP server reconnected after a new login", slog.String("server", s.name))
		m.up(ctx, s, c)
	}
	m.settleLocked(s)
}

// open connects and lists the tools and prompts under ctx; life is the
// servers' lifetime, which bounds listing again when the server says a
// list changed.
func (m *Manager) open(ctx, life context.Context, s *server) (conn, error) {
	t, err := m.transport(s)
	if err != nil {
		return conn{}, err
	}
	client := sdk.NewClient(implementation, &sdk.ClientOptions{
		// Codex only logs a changed list. uah lists the tools again and
		// offers them from the next run, so a run's tools never change
		// under it.
		ToolListChangedHandler: func(_ context.Context, req *sdk.ToolListChangedRequest) {
			go m.relist(life, s, req.Session, listTools)
		},
		PromptListChangedHandler: func(_ context.Context, req *sdk.PromptListChangedRequest) {
			go m.relist(life, s, req.Session, listPrompts)
		},
	})
	session, err := client.Connect(ctx, t, nil)
	if err != nil {
		return conn{}, fmt.Errorf("failed to connect: %w", err)
	}
	c := conn{session: session}
	if r := session.InitializeResult(); r != nil {
		c.caps = r.Capabilities
	}
	if c.tools, err = all(session.Tools(ctx, nil)); err != nil {
		_ = session.Close()

		return conn{}, fmt.Errorf("failed to list tools: %w", err)
	}
	if c.caps != nil && c.caps.Prompts != nil {
		// A server whose prompts fail still offers its tools.
		if c.prompts, err = all(session.Prompts(ctx, nil)); err != nil {
			m.opts.Logger.LogAttrs(ctx, slog.LevelWarn, "failed to list MCP prompts",
				slog.String("server", s.name),
				slog.Any("err", err))
		}
	}

	return c, nil
}

// all collects every page of a list.
func all[T any](seq func(func(T, error) bool)) ([]T, error) {
	var out []T
	for v, err := range seq {
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}

	return out, nil
}

// A list the server can change: tools or prompts.
type list int

const (
	listTools list = iota
	listPrompts
)

// relist lists the tools or prompts again after the server said they
// changed. New tools are offered from the next run on.
func (m *Manager) relist(life context.Context, s *server, session *sdk.ClientSession, which list) {
	ctx, cancel := context.WithTimeout(life, s.cfg.StartupTimeout())
	defer cancel()
	var (
		tools   []*sdk.Tool
		prompts []*sdk.Prompt
		err     error
	)
	if which == listTools {
		tools, err = all(session.Tools(ctx, nil))
	} else {
		prompts, err = all(session.Prompts(ctx, nil))
	}
	if err != nil {
		m.opts.Logger.LogAttrs(ctx, slog.LevelWarn, "failed to list the MCP server's changed list",
			slog.String("server", s.name),
			slog.String("list", [...]string{"tools", "prompts"}[which]),
			slog.Any("err", err))

		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if s.session != session || m.closed {
		return
	}
	if which == listPrompts {
		s.prompts = prompts

		return
	}
	if sameTools(s.raw, tools) {
		return
	}
	s.raw = tools
	m.retool()
	m.opts.Logger.LogAttrs(ctx, slog.LevelInfo, "MCP server tool list changed; the new list applies from the next run",
		slog.String("server", s.name),
		slog.Int("tools", len(tools)))
}

// sameTools reports whether two tool lists offer the same tools.
func sameTools(a, b []*sdk.Tool) bool {
	if len(a) != len(b) {
		return false
	}
	ja, erra := json.Marshal(a)
	jb, errb := json.Marshal(b)

	return erra == nil && errb == nil && string(ja) == string(jb)
}

func errOrEOF(err error) error {
	if err == nil {
		return io.EOF
	}

	return err
}

// retool names the tools again after a server's list changed; runs that
// start later offer them. Until every server has started, Start's
// goroutine names them. The caller holds mu.
func (m *Manager) retool() {
	if m.started {
		m.tools = m.qualify()
	}
}

// qualify names the allowed tools of every server that listed some, in
// server and tool order so names are stable. A server that stopped keeps
// its tools: calls to them fail with the reason. The caller holds mu.
func (m *Manager) qualify() []Tool {
	var candidates []Tool
	for _, s := range m.servers {
		if s.state == StateDisabled {
			continue
		}
		cfg := m.configs[s.name] // AlwaysAllow may have changed its approvals
		for _, t := range s.raw {
			if !cfg.Allows(t.Name) {
				continue
			}
			candidates = append(candidates, Tool{
				Server: s.name, Tool: t.Name, Description: t.Description, InputSchema: schema(t.InputSchema),
				ReadOnly: t.Annotations != nil && t.Annotations.ReadOnlyHint, AutoAsks: autoAsks(t.Annotations), Approval: cfg.ApprovalFor(t.Name),
			})
		}
	}

	return Qualify(candidates)
}

// Qualify gives the tools their qualified names, in server and tool order
// so names are stable: mcp__<server>__<tool>, hashed when that is too long
// or taken (namer).
func Qualify(tools []Tool) []Tool {
	slices.SortFunc(tools, func(a, b Tool) int {
		return cmp.Or(cmp.Compare(a.Server, b.Server), cmp.Compare(a.Tool, b.Tool))
	})
	n := newNamer()
	for i := range tools {
		tools[i].Name = n.name(tools[i].Server, tools[i].Tool)
	}

	return tools
}

// schema returns the input schema as a JSON object, defaulting to an
// object with no properties.
func schema(v any) map[string]any {
	out := map[string]any{}
	if b, err := json.Marshal(v); err == nil {
		_ = json.Unmarshal(b, &out)
	}
	if out["type"] == nil {
		out["type"] = "object"
	}
	if out["properties"] == nil {
		out["properties"] = map[string]any{}
	}

	return out
}

// autoAsks ports Codex's requires_mcp_tool_approval
// (codex-rs/core/src/mcp_tool_call.rs): unset hints default to asking.
func autoAsks(a *sdk.ToolAnnotations) bool {
	if a == nil {
		return true
	}
	if a.DestructiveHint != nil && *a.DestructiveHint {
		return true
	}
	if a.ReadOnlyHint {
		return false
	}
	destructive := a.DestructiveHint == nil || *a.DestructiveHint
	openWorld := a.OpenWorldHint == nil || *a.OpenWorldHint

	return destructive || openWorld
}
