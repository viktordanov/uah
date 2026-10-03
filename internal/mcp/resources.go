package mcp

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Resources, as Codex's list_mcp_resources, list_mcp_resource_templates,
// and read_mcp_resource tools return them
// (codex-rs/core/src/tools/handlers/mcp_resource.rs): JSON with each entry's
// server beside the server's own fields.

// The model's resource tools, with Codex's names.
const (
	ListResourcesTool         = "list_mcp_resources"
	ListResourceTemplatesTool = "list_mcp_resource_templates"
	ReadResourceTool          = "read_mcp_resource"
)

// IsResourceTool reports whether a tool name is one of the resource tools.
func IsResourceTool(name string) bool {
	return name == ListResourcesTool || name == ListResourceTemplatesTool || name == ReadResourceTool
}

// HasServers reports whether any server is enabled, as Codex offers the
// resource tools whenever it has an MCP server.
func (m *Manager) HasServers() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.configs {
		if c.IsEnabled() {
			return true
		}
	}

	return false
}

// Names are the enabled servers' names, in order: what "@server:uri" can
// name.
func (m *Manager) Names() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for name, c := range m.configs {
		if c.IsEnabled() {
			out = append(out, name)
		}
	}
	slices.Sort(out)

	return out
}

// ErrCursorWithoutServer is a page cursor given for every server at once.
var ErrCursorWithoutServer = errors.New("cursor can only be used when a server is specified")

// ResourceList is a page of one server's resources, or every server's
// resources (Server empty, no cursor).
type ResourceList struct {
	Server     string           `json:"server,omitempty"`
	Resources  []map[string]any `json:"resources"`
	NextCursor string           `json:"nextCursor,omitempty"`
}

// TemplateList is ResourceList for resource templates.
type TemplateList struct {
	Server            string           `json:"server,omitempty"`
	ResourceTemplates []map[string]any `json:"resourceTemplates"`
	NextCursor        string           `json:"nextCursor,omitempty"`
}

// ResourceRef is a resource for "@" in the composer.
type ResourceRef struct {
	Server      string
	URI         string
	Name        string
	Description string
}

// ListResources lists one server's resources a page at a time, or with
// server empty every ready server's, all pages, leaving out a server that
// fails (logged), as Codex does.
func (m *Manager) ListResources(ctx context.Context, server, cursor string) (ResourceList, error) {
	entries, next, err := listPage(ctx, m, server, cursor, "resources/list", listAllResources,
		func(ctx context.Context, cs *sdk.ClientSession, cursor string) ([]*sdk.Resource, string, error) {
			r, err := cs.ListResources(ctx, &sdk.ListResourcesParams{Cursor: cursor})
			if err != nil {
				return nil, "", err
			}

			return r.Resources, r.NextCursor, nil
		})
	if err != nil {
		return ResourceList{}, err
	}

	return ResourceList{Server: server, Resources: entries, NextCursor: next}, nil
}

// ListResourceTemplates is ListResources for resource templates.
func (m *Manager) ListResourceTemplates(ctx context.Context, server, cursor string) (TemplateList, error) {
	entries, next, err := listPage(ctx, m, server, cursor, "resources/templates/list", listAllTemplates,
		func(ctx context.Context, cs *sdk.ClientSession, cursor string) ([]*sdk.ResourceTemplate, string, error) {
			r, err := cs.ListResourceTemplates(ctx, &sdk.ListResourceTemplatesParams{Cursor: cursor})
			if err != nil {
				return nil, "", err
			}

			return r.ResourceTemplates, r.NextCursor, nil
		})
	if err != nil {
		return TemplateList{}, err
	}

	return TemplateList{Server: server, ResourceTemplates: entries, NextCursor: next}, nil
}

// listPage lists one server's page from cursor, or with server empty
// every server's whole list: each entry with its server, and the next
// cursor.
func listPage[T any](ctx context.Context, m *Manager, server, cursor, method string,
	every func(context.Context, *sdk.ClientSession) ([]T, error),
	page func(context.Context, *sdk.ClientSession, string) ([]T, string, error),
) ([]map[string]any, string, error) {
	entries := []map[string]any{}
	if server == "" {
		if cursor != "" {
			return nil, "", ErrCursorWithoutServer
		}
		byServer := everyServer(ctx, m, method, every)
		for _, name := range sortedKeys(byServer) {
			for _, v := range byServer[name] {
				entries = append(entries, withServer(name, v))
			}
		}

		return entries, "", nil
	}
	var (
		items []T
		next  string
	)
	err := m.request(ctx, server, func(ctx context.Context, cs *sdk.ClientSession) (err error) {
		items, next, err = page(ctx, cs, cursor)

		return err
	})
	if err != nil {
		return nil, "", fmt.Errorf("%s failed: %w", method, err)
	}
	for _, v := range items {
		entries = append(entries, withServer(server, v))
	}

	return entries, next, nil
}

// ReadResource reads a resource: its contents with the server and the
// URI beside them, as Codex's read_mcp_resource returns them, and the
// server's whole result.
func (m *Manager) ReadResource(ctx context.Context, server, uri string) (map[string]any, *sdk.ReadResourceResult, error) {
	var r *sdk.ReadResourceResult
	err := m.request(ctx, server, func(ctx context.Context, cs *sdk.ClientSession) (err error) {
		r, err = cs.ReadResource(ctx, &sdk.ReadResourceParams{URI: uri})

		return err
	})
	if err != nil {
		return nil, nil, fmt.Errorf("resources/read failed: %w", err)
	}
	// Codex flattens the whole result; the newer protocol's result also
	// carries caching hints and the server's info, which the model has no
	// use for, so only the contents go beside the server and URI.
	contents := r.Contents
	if contents == nil {
		contents = []*sdk.ResourceContents{}
	}

	return map[string]any{"server": server, "uri": uri, "contents": contents}, r, nil
}

// Resources lists every ready server's resources for "@" mentions, in
// server and URI order.
func (m *Manager) Resources(ctx context.Context) []ResourceRef {
	var out []ResourceRef
	byServer := everyServer(ctx, m, "resources/list", listAllResources)
	for _, name := range sortedKeys(byServer) {
		for _, r := range byServer[name] {
			out = append(out, ResourceRef{Server: name, URI: r.URI, Name: cmp.Or(r.Title, r.Name), Description: r.Description})
		}
	}
	slices.SortStableFunc(out, func(a, b ResourceRef) int {
		return cmp.Or(cmp.Compare(a.Server, b.Server), cmp.Compare(a.URI, b.URI))
	})

	return out
}

func listAllResources(ctx context.Context, cs *sdk.ClientSession) ([]*sdk.Resource, error) {
	return all(cs.Resources(ctx, nil))
}

func listAllTemplates(ctx context.Context, cs *sdk.ClientSession) ([]*sdk.ResourceTemplate, error) {
	return all(cs.ResourceTemplates(ctx, nil))
}

// everyServer runs list on every ready server that offers resources, at
// once, each within its tool timeout. A server that fails is logged and
// left out, so one broken server does not hide the others.
func everyServer[T any](ctx context.Context, m *Manager, method string, list func(context.Context, *sdk.ClientSession) ([]T, error)) map[string][]T {
	m.mu.Lock()
	sessions := map[string]*sdk.ClientSession{}
	timeouts := map[string]time.Duration{}
	for name, s := range m.servers {
		if s.state == StateReady && s.caps != nil && s.caps.Resources != nil {
			sessions[name], timeouts[name] = s.session, s.cfg.ToolTimeout()
		}
	}
	m.mu.Unlock()
	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		out = map[string][]T{}
	)
	for name, cs := range sessions {
		wg.Go(func() {
			ctx, cancel := context.WithTimeout(ctx, timeouts[name])
			defer cancel()
			items, err := list(ctx, cs)
			if err != nil {
				m.opts.Logger.LogAttrs(ctx, slog.LevelWarn, "failed to list MCP resources",
					slog.String("server", name),
					slog.String("method", method),
					slog.Any("err", err))

				return
			}
			mu.Lock()
			out[name] = items
			mu.Unlock()
		})
	}
	wg.Wait()

	return out
}

// request runs a request other than a tool call on a server once it can
// take one, within its tool timeout, as Codex bounds resource requests.
func (m *Manager) request(ctx context.Context, name string, fn func(context.Context, *sdk.ClientSession) error) error {
	s, err := m.find(name)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, s.cfg.ToolTimeout())
	defer cancel()
	err = m.send(ctx, s, func(cs *sdk.ClientSession) error { return fn(ctx, cs) })
	if err != nil && ctx.Err() != nil && !errors.Is(err, errUnavailable) {
		return callError(ctx, s.cfg.ToolTimeout())
	}

	return err
}

// withServer is v's JSON fields with "server" first, as Codex flattens
// a resource beside its server.
func withServer(server string, v any) map[string]any {
	out := map[string]any{}
	if b, err := json.Marshal(v); err == nil {
		_ = json.Unmarshal(b, &out)
	}
	out["server"] = server

	return out
}

// sortedKeys returns a map's keys in order.
func sortedKeys[V any](m map[string]V) []string { return slices.Sorted(maps.Keys(m)) }
