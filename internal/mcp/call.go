package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Call calls a server's tool with JSON arguments. A server that does not
// take parallel calls runs one at a time; the tool timeout covers the wait
// for its turn too, and the wait while the server restarts. An error
// means the call did not complete; a tool that ran and failed returns a
// Result with IsError.
func (m *Manager) Call(ctx context.Context, serverName, tool string, args json.RawMessage) (Result, error) {
	s, err := m.find(serverName)
	if err != nil {
		return Result{}, err
	}
	timeout := s.cfg.ToolTimeout()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if s.calls != nil {
		select {
		case s.calls <- struct{}{}:
			defer func() { <-s.calls }()
		case <-ctx.Done():
			return Result{}, fmt.Errorf("the call was canceled while waiting for the server: %w", ctx.Err())
		}
	}
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	var r *sdk.CallToolResult
	err = m.send(ctx, s, func(cs *sdk.ClientSession) (err error) {
		r, err = cs.CallTool(ctx, &sdk.CallToolParams{Name: tool, Arguments: args})

		return err
	})
	switch {
	case errors.Is(err, ErrNeedsLogin), errors.Is(err, errUnavailable):
		return Result{}, err
	case err != nil && ctx.Err() != nil:
		return Result{}, callError(ctx, timeout)
	case err != nil:
		return Result{}, fmt.Errorf("failed to call %s on %s: %w", tool, serverName, err)
	}

	return convert(r), nil
}

// errUnavailable marks an error from await: the request was not sent.
var errUnavailable = errors.New("unavailable")

type unavailable struct{ error }

func (u unavailable) Unwrap() []error { return []error{u.error, errUnavailable} }

// send sends one request once the server can take it. When the server
// never got the request, it is sent once more: after a new session when an
// HTTP server forgot the session (a 404), as Codex re-initializes, and
// after the restart when the connection had already ended. A 401 makes the
// server needs_login.
func (m *Manager) send(ctx context.Context, s *server, fn func(*sdk.ClientSession) error) error {
	session, err := m.await(ctx, s)
	if err != nil {
		return unavailable{err}
	}
	err = fn(session)
	switch {
	case ctx.Err() != nil:
	case errors.Is(err, sdk.ErrSessionMissing):
		var next *sdk.ClientSession
		if next, err = m.reconnect(ctx, s, session); err == nil {
			session = next
			err = fn(session)
		}
	case notSent(err):
		m.mu.Lock()
		life := m.ctx
		m.mu.Unlock()
		if life != nil {
			m.stopped(life, s, session, session.Wait()) //nolint:contextcheck // a restart outlives the call
		}
		if session, err = m.await(ctx, s); err != nil {
			return unavailable{err}
		}
		err = fn(session)
	}
	if login, ok := errors.AsType[*LoginError](err); ok {
		m.needsLogin(ctx, s, session, login)

		return login
	}

	return err
}

// notSent reports a request refused because its connection had already
// ended: the SDK's ErrConnectionClosed from jsonrpc2's "client is closing",
// which it returns before writing anything.
func notSent(err error) bool {
	return errors.Is(err, sdk.ErrConnectionClosed) && strings.Contains(err.Error(), "client is closing")
}

// needsLogin marks a running server needs_login when it answered a call
// with a 401, as a 401 at startup does, so /mcp and `uah doctor` say to log
// in and later calls fail at once.
func (m *Manager) needsLogin(ctx context.Context, s *server, session *sdk.ClientSession, login *LoginError) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s.state != StateReady || s.session != session {
		return
	}
	s.state, s.err = StateNeedsLogin, login
	m.opts.Logger.LogAttrs(ctx, slog.LevelWarn, "MCP server needs a login",
		slog.String("server", s.name),
		slog.Any("err", login))
}

// find returns the configured server, or why it can never take a call.
func (m *Manager) find(name string) (*server, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s := m.servers[name]; s != nil {
		return s, nil
	}

	return nil, fmt.Errorf("the MCP server %s is not running", name)
}

// await returns the server's session once it can take requests: at once
// when it is ready, after the restart or reconnect under way, or never
// when it failed or needs a login. ctx bounds the wait.
func (m *Manager) await(ctx context.Context, s *server) (*sdk.ClientSession, error) {
	for {
		m.mu.Lock()
		state, serr, session, settled := s.state, s.err, s.session, s.settled
		closed := m.closed
		m.mu.Unlock()
		switch {
		case closed:
			return nil, fmt.Errorf("the MCP server %s is not running", s.name)
		case settled != nil:
			select {
			case <-settled:
				continue
			case <-ctx.Done():
				if serr != nil {
					return nil, fmt.Errorf("the MCP server %s is %s after %w; the call was not sent: %w", s.name, state, serr, ctx.Err())
				}

				return nil, fmt.Errorf("the MCP server %s is %s; the call was not sent: %w", s.name, state, ctx.Err())
			}
		case state == StateReady:
			return session, nil
		case state == StateFailed:
			return nil, fmt.Errorf("the MCP server %s failed: %w", s.name, serr)
		case state == StateNeedsLogin:
			return nil, serr
		}

		return nil, fmt.Errorf("the MCP server %s is %s", s.name, state)
	}
}

func callError(ctx context.Context, timeout time.Duration) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("the tool did not finish within %s", timeout)
	}

	return fmt.Errorf("the call was canceled: %w", ctx.Err())
}
