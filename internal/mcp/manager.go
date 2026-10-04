package mcp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"slices"
	"sync"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// State is where a server is in its lifecycle.
type State string

const (
	StateStarting State = "starting"
	StateReady    State = "ready"
	StateFailed   State = "failed"
	StateDisabled State = "disabled"
	// StateNeedsLogin is an HTTP server that asked for OAuth without a
	// usable login; `uah mcp login <name>` fixes it, and the next run or
	// /mcp reconnects it.
	StateNeedsLogin State = "needs_login"
	// StateRestarting is a server that stopped on its own and is being
	// started again; calls wait for it.
	StateRestarting State = "restarting"
)

var discard = slog.New(slog.DiscardHandler)

// ErrClosed is a manager used after Close.
var ErrClosed = errors.New("the MCP servers were closed")

// Options configure a Manager.
type Options struct {
	// Workspace is a stdio server's directory unless it sets cwd.
	Workspace string
	// Getenv reads the variables servers get (default os.Getenv).
	Getenv func(string) string
	// Logger receives stdio servers' standard error, one record per line,
	// and problems that do not fail a call (default: discarded).
	Logger *slog.Logger
	// Credentials holds OAuth logins; nil turns OAuth off, so a server
	// that asks for it fails.
	Credentials CredentialStore
	// HTTPClient makes OAuth discovery and token requests (default
	// http.DefaultClient).
	HTTPClient *http.Client
	// ServerFile names the configuration file AlwaysAllow saves a server's
	// tool approval to; nil or "" saves nothing.
	ServerFile func(server string) string
	// RestartDelay is the wait before the first restart of a server that
	// stopped (default DefaultRestartDelay); each later one doubles it.
	RestartDelay time.Duration
}

// Tool is an MCP tool as the model sees it.
type Tool struct {
	// Name is the qualified name, mcp__<server>__<tool>.
	Name        string
	Server      string
	Tool        string // the server's own name for it
	Description string
	InputSchema map[string]any
	ReadOnly    bool
	// AutoAsks is Codex's annotation rule for approval_mode auto: a
	// destructive tool asks, a read-only one does not, and otherwise it asks
	// unless marked both non-destructive and closed-world.
	AutoAsks bool
	Approval ApprovalMode
}

// NeedsApproval applies approval_mode as Codex does: prompt always asks,
// writes asks unless the tool is read-only, auto follows the tool's
// annotations, and approve never asks.
func (t Tool) NeedsApproval() bool {
	switch t.Approval {
	case ApprovalApprove:
		return false
	case ApprovalAuto:
		return t.AutoAsks
	case ApprovalWrites:
		return !t.ReadOnly
	case ApprovalPrompt:
	}

	return true
}

// Manager starts the configured servers on first use and keeps them until
// Close. It is safe for concurrent use.
type Manager struct {
	configs map[string]ServerConfig
	// unsupported holds the servers whose auth uah does not support: they
	// fail with the reason instead of starting.
	unsupported map[string]error
	opts        Options

	mu      sync.Mutex
	closed  bool            // Close was called: nothing starts again
	ctx     context.Context // the servers' lifetime, until Close
	cancel  context.CancelFunc
	servers map[string]*server // nil until started
	// tools are the tools offered, named when every server has started or
	// failed (started) and again when a server's list changes.
	tools   []Tool
	started bool
	done    chan struct{} // closed when started
}

// NewManager returns a manager for the servers, keyed by name. It starts
// nothing until Start, Tools, or Status. An invalid server fails it, except
// that a server with an auth uah does not support only fails to start.
func NewManager(servers map[string]ServerConfig, opts Options) (*Manager, error) {
	unsupported := map[string]error{}
	for _, name := range slices.Sorted(maps.Keys(servers)) {
		err := servers[name].Validate()
		switch {
		case errors.Is(err, ErrUnsupportedAuth):
			unsupported[name] = err
		case err != nil:
			return nil, fmt.Errorf("mcp_servers.%s: %w", name, err)
		}
	}
	if opts.Getenv == nil {
		opts.Getenv = os.Getenv
	}
	if opts.Logger == nil {
		opts.Logger = discard
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = http.DefaultClient
	}
	if opts.RestartDelay <= 0 {
		opts.RestartDelay = DefaultRestartDelay
	}

	return &Manager{configs: maps.Clone(servers), unsupported: unsupported, opts: opts}, nil
}

// Start begins starting the enabled servers, once, unless the manager
// closed.
func (m *Manager) Start() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.servers != nil || m.closed {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.ctx, m.cancel = ctx, cancel
	m.servers, m.done = map[string]*server{}, make(chan struct{})
	var wg sync.WaitGroup
	for name, cfg := range m.configs {
		s := newServer(name, cfg, m.opts)
		m.servers[name] = s
		if s.state == StateDisabled {
			continue
		}
		if err := m.unsupported[name]; err != nil {
			s.state, s.err = StateFailed, err

			continue
		}
		wg.Go(func() { m.connect(ctx, s) })
	}
	done := m.done
	go func() {
		wg.Wait()
		m.mu.Lock()
		if !m.closed {
			m.tools, m.started = m.qualify(), true
		}
		m.mu.Unlock()
		close(done)
	}()
}

// Tools starts the servers, waits until each has started or failed, and
// returns the tools to offer. A run calls it as it starts, so a run's tools
// stay the same while it runs and a changed list applies from the next
// one. A needs_login server whose stored login changed since (`uah mcp
// login` in another terminal) reconnects first, as Codex does before each
// step. It fails when a required server did not start.
func (m *Manager) Tools(ctx context.Context) ([]Tool, error) {
	m.Start() //nolint:contextcheck // servers outlive the caller's context
	m.mu.Lock()
	done, closed := m.done, m.closed
	m.mu.Unlock()
	if closed {
		return nil, ErrClosed
	}
	select {
	case <-done:
	case <-ctx.Done():
		return nil, fmt.Errorf("failed to start MCP servers: %w", ctx.Err())
	}
	for _, settled := range m.relogin() {
		select {
		case <-settled:
		case <-ctx.Done():
			return nil, fmt.Errorf("failed to reconnect MCP servers: %w", ctx.Err())
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, ErrClosed
	}
	for _, name := range slices.Sorted(maps.Keys(m.servers)) {
		s := m.servers[name]
		if s.cfg.Required && (s.state == StateFailed || s.state == StateNeedsLogin) {
			return nil, fmt.Errorf("the required MCP server %s failed to start: %w", name, s.err)
		}
	}

	return slices.Clone(m.tools), nil
}

// Close stops the servers for good: the closed state is set under the
// same lock Start checks, so nothing starts a server afterwards. Closing
// again does nothing.
func (m *Manager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()

		return nil
	}
	servers, cancel := m.servers, m.cancel
	m.closed = true
	m.servers, m.tools, m.cancel, m.done = nil, nil, nil, nil
	if cancel != nil {
		cancel()
	}
	for _, s := range servers {
		m.settleLocked(s) // calls waiting for a restart stop waiting
	}
	m.mu.Unlock()
	var errs []error
	for _, s := range servers {
		m.mu.Lock()
		session, state := s.session, s.state
		m.mu.Unlock()
		if session == nil {
			continue
		}
		// A server that stopped on its own was reported then; its exit
		// status is no news.
		if err := session.Close(); err != nil && state == StateReady && !errors.Is(err, sdk.ErrConnectionClosed) {
			errs = append(errs, fmt.Errorf("failed to close the MCP server %s: %w", s.name, err))
		}
	}

	return errors.Join(errs...)
}
