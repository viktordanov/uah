package embedded

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/viktordanov/uah-core/harness/llm"
	"github.com/viktordanov/uah-core/harness/tool"

	"github.com/viktordanov/uah/internal/mcp"
)

// Codex's MCP resource tools (codex-rs/core/src/tools/handlers/
// mcp_resource_spec.rs), offered whenever an MCP server is configured. They
// run as MCP remote jobs, with the plan's op naming the request, and never
// ask: reading a resource changes nothing.
const (
	opListResources = "resources/list"
	opListTemplates = "resources/templates/list"
	opReadResource  = "resources/read"
)

// argServer is the resource tools' server argument.
const argServer = "server"

// resourceTools are the tools' definitions in Codex's order.
func resourceTools() []tool.Definition {
	object := func(props map[string]any, required ...string) map[string]any {
		out := map[string]any{schemaType: "object", "properties": props, "additionalProperties": false}
		if len(required) > 0 {
			out["required"] = required
		}

		return out
	}
	def := func(name, description string, params map[string]any) tool.Definition {
		return tool.Definition{Tool: llm.Tool{Type: llm.ToolFunction, Name: name, Description: description, Parameters: params}}
	}

	return []tool.Definition{
		def(mcp.ListResourcesTool, "Lists resources provided by MCP servers. Resources allow servers to share data that provides context to language models, such as files, database schemas, or application-specific information. Prefer resources over web search when possible.",
			object(map[string]any{
				argServer: property(jsonString, "MCP server name. Omit to list resources from every configured server."),
				"cursor":  property(jsonString, "Opaque cursor from a previous list_mcp_resources call; omit for the first page."),
			})),
		def(mcp.ListResourceTemplatesTool, "Lists resource templates provided by MCP servers. Parameterized resource templates allow servers to share data that takes parameters and provides context to language models, such as files, database schemas, or application-specific information. Prefer resource templates over web search when possible.",
			object(map[string]any{
				argServer: property(jsonString, "MCP server name. Omit to list resource templates from every configured server."),
				"cursor":  property(jsonString, "Opaque cursor from a previous list_mcp_resource_templates call; omit for the first page."),
			})),
		def(mcp.ReadResourceTool, "Read a specific resource from an MCP server given the server name and resource URI.",
			object(map[string]any{
				argServer: property(jsonString, "MCP server name exactly as configured. Must match the 'server' field returned by list_mcp_resources."),
				"uri":     property(jsonString, "Resource URI to read. Must be one of the URIs returned by list_mcp_resources."),
			}, argServer, "uri")),
	}
}

// resourcePlan reads a resource tool's arguments into its job's plan,
// with Codex's checks and messages.
func resourcePlan(name, arguments string) (mcpPlan, error) {
	var args struct {
		Server *string `json:"server"`
		Cursor *string `json:"cursor"`
		URI    *string `json:"uri"`
	}
	raw := bytes.TrimSpace([]byte(arguments))
	if len(raw) > 0 && !bytes.Equal(raw, []byte("null")) {
		if err := json.Unmarshal(raw, &args); err != nil {
			return mcpPlan{}, fmt.Errorf("failed to parse function arguments: %w", err)
		}
	}
	trim := func(p *string) string {
		if p == nil {
			return ""
		}

		return strings.TrimSpace(*p)
	}
	plan := mcpPlan{Server: trim(args.Server), Cursor: trim(args.Cursor), URI: trim(args.URI)}
	switch name {
	case mcp.ListResourcesTool, mcp.ListResourceTemplatesTool:
		plan.Op = opListResources
		if name == mcp.ListResourceTemplatesTool {
			plan.Op = opListTemplates
		}
		if plan.Server == "" && plan.Cursor != "" {
			return mcpPlan{}, mcp.ErrCursorWithoutServer
		}
	case mcp.ReadResourceTool:
		plan.Op = opReadResource
		switch {
		case args.Server == nil:
			return mcpPlan{}, errors.New("failed to parse function arguments: missing field `server`")
		case args.URI == nil:
			return mcpPlan{}, errors.New("failed to parse function arguments: missing field `uri`")
		case plan.Server == "":
			return mcpPlan{}, errors.New("server must be provided")
		case plan.URI == "":
			return mcpPlan{}, errors.New("uri must be provided")
		}
	}

	return plan, nil
}

// resourceJob runs a resource request and returns its JSON text.
func resourceJob(ctx context.Context, m *mcp.Manager, plan mcpPlan) (string, error) {
	var (
		v   any
		err error
	)
	switch plan.Op {
	case opListResources:
		v, err = m.ListResources(ctx, plan.Server, plan.Cursor)
	case opListTemplates:
		v, err = m.ListResourceTemplates(ctx, plan.Server, plan.Cursor)
	case opReadResource:
		v, _, err = m.ReadResource(ctx, plan.Server, plan.URI)
	default:
		return "", fmt.Errorf("unknown MCP request %q", plan.Op)
	}
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("failed to serialize MCP resource response: %w", err)
	}

	return string(b), nil
}
