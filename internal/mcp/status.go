package mcp

import (
	"context"
	"maps"
	"slices"
	"strings"
)

// Transports, as Codex names them.
const (
	TransportStdio = "stdio"
	TransportHTTP  = "streamable_http"
)

// ServerStatus is one server's state for /mcp and `uah doctor`.
type ServerStatus struct {
	Name  string
	State State
	Error string
	// Transport is stdio or streamable_http; Target is the command line or
	// the URL.
	Transport string
	Target    string
	Auth      AuthStatus
	// Required is the server's required key: a run fails while it is not
	// ready.
	Required bool
	// Tools are the tools offered to the model, in name order.
	Tools []Tool
	// Prompts are the server's prompts, offered as slash commands.
	Prompts []Prompt
	// HasResources reports whether the server offers resources;
	// Resources lists them when the caller asked (/mcp verbose).
	HasResources bool
	Resources    []ResourceRef
	// Restarts counts the server's restarts in a row; see MaxRestarts.
	Restarts int
}

// Transport is the server's transport name.
func (c ServerConfig) Transport() string {
	if c.URL != "" {
		return TransportHTTP
	}

	return TransportStdio
}

// Target is the command line or the URL.
func (c ServerConfig) Target() string {
	if c.URL != "" {
		return c.URL
	}

	return strings.Join(append([]string{c.Command}, c.Args...), " ")
}

// Status starts the servers if needed and reports each one, or none once
// the manager closed. While some are still starting, the ready ones' tools
// are named as if the others fail.
func (m *Manager) Status() []ServerStatus {
	m.Start()
	m.relogin() // a server that now has a login shows as starting, then ready
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.status()
}

// Started starts the servers if needed, waits until each has started or
// failed, and reports them. It reports nothing when ctx ends or the manager
// closes first; a closed manager never starts its servers again.
func (m *Manager) Started(ctx context.Context) []ServerStatus {
	m.Start() //nolint:contextcheck // servers outlive the caller's context
	m.mu.Lock()
	done, closed := m.done, m.closed
	m.mu.Unlock()
	if closed {
		return nil
	}
	select {
	case <-done:
	case <-ctx.Done():
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}

	return m.status()
}

// status reports each server; the caller holds m.mu.
func (m *Manager) status() []ServerStatus {
	tools := m.tools
	if tools == nil {
		tools = m.qualify()
	}
	prompts := m.prompts()
	out := make([]ServerStatus, 0, len(m.servers))
	for _, name := range slices.Sorted(maps.Keys(m.servers)) {
		s := m.servers[name]
		st := ServerStatus{
			Name: name, State: s.state, Transport: s.cfg.Transport(), Target: s.cfg.Target(), Auth: s.authStatus(), Required: s.cfg.Required,
		}
		if s.err != nil {
			st.Error = s.err.Error()
		}
		for _, t := range tools {
			if t.Server == name {
				st.Tools = append(st.Tools, t)
			}
		}
		for _, p := range prompts {
			if p.Server == name {
				st.Prompts = append(st.Prompts, p)
			}
		}
		st.HasResources = s.caps != nil && s.caps.Resources != nil
		st.Restarts = s.restarts
		out = append(out, st)
	}

	return out
}

// authStatus is the server's auth status as far as starting it showed.
// The caller holds Manager.mu.
func (s *server) authStatus() AuthStatus {
	switch {
	case s.cfg.URL == "":
		return AuthUnsupported
	case s.cfg.usesBearer():
		return AuthBearerToken
	case s.state == StateNeedsLogin:
		return AuthNotLoggedIn
	case s.auth != nil && s.auth.loggedIn():
		return AuthOAuth
	}

	return AuthUnsupported
}

func (a *storedAuth) loggedIn() bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	return a.hasLogin
}
