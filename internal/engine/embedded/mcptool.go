package embedded

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/json/jsontext"
	"fmt"
	"slices"
	"strings"

	"github.com/viktordanov/uah-core/harness/llm"
	"github.com/viktordanov/uah-core/harness/operation"
	"github.com/viktordanov/uah-core/harness/tool"

	"github.com/viktordanov/uah/internal/approval"
	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/mcp"
)

// The remote job plan an MCP call runs as (see docs/design/mcp.md).
const (
	mcpPlanType    operation.RemoteJobPlanType    = engine.MCPPlanType
	mcpPlanVersion operation.RemoteJobPlanVersion = 1
)

// mcpPlan is the remote job's plan: which tool to call, with what, or
// with Op set, a resource request. A plan stored before Op existed is a
// tool call.
type mcpPlan struct {
	Server    string          `json:"server"`
	Tool      string          `json:"tool,omitempty"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
	Op        string          `json:"op,omitempty"`
	Cursor    string          `json:"cursor,omitempty"`
	URI       string          `json:"uri,omitempty"`
}

// mcpOutputs is what a completed call keeps in the job's handle, which the
// runner stores untruncated: its images, as data: URLs.
type mcpOutputs struct {
	Images []string `json:"images,omitempty"`
}

// mcpRegistry offers the MCP tools besides the runner's and resolves every
// mcp__ name, so a session with past MCP calls resumes even when the server
// is gone: the stored results need no server.
type mcpRegistry struct {
	tool.Registry

	tools []mcp.Tool
	// resources offers Codex's resource tools.
	resources []tool.Definition
	gate      mcpGate
}

// mcpGate asks the user about calls whose approval_mode needs it, through
// the same prompt as sandbox escalations.
type mcpGate struct {
	// ctx bounds the approval of a call decided in Translate.
	ctx   context.Context
	ask   approval.Ask // nil: no one can answer (headless)
	never bool         // approval_policy "never"
	// m, when set, has the tools' live approval modes and saves "don't ask
	// again for this tool"; warn reports a save that failed.
	m    *mcp.Manager
	warn func(string)
	// changes, set with m, wakes the run's other open prompts when "don't
	// ask again" allows a tool, so those for the same tool stop asking.
	changes *approval.Changes
	// approved, when set, reports the tools the session's scope approved
	// in advance: they run as with approval_mode approve.
	approved func(name string) bool
	// yolo, when set, reports yolo mode, where every tool runs unasked.
	yolo func() bool
}

// withMCP adds the tools the request does not disallow, and with
// resources, the resource tools it does not disallow.
func withMCP(r tool.Registry, tools []mcp.Tool, resources bool, disallowed []string, gate mcpGate) tool.Registry {
	tools = slices.DeleteFunc(slices.Clone(tools), func(t mcp.Tool) bool { return slices.Contains(disallowed, t.Name) })
	var defs []tool.Definition
	if resources {
		defs = slices.DeleteFunc(resourceTools(), func(d tool.Definition) bool { return slices.Contains(disallowed, d.Tool.Name) })
	}

	return mcpRegistry{Registry: r, tools: tools, resources: defs, gate: gate}
}

func (r mcpRegistry) StaticDefinitions() []tool.Definition {
	defs := append(r.Registry.StaticDefinitions(), r.resources...)
	for _, t := range r.tools {
		description := t.Description
		if description == "" {
			description = fmt.Sprintf("The %s tool of the %s MCP server.", t.Tool, t.Server)
		}
		defs = append(defs, tool.Definition{Tool: llm.Tool{Type: llm.ToolFunction, Name: t.Name, Description: description, Parameters: t.InputSchema}})
	}

	return defs
}

func (r mcpRegistry) Resolve(name string) (tool.Translator, bool) {
	if t, ok := r.Registry.Resolve(name); ok {
		return t, ok
	}
	if mcp.IsResourceTool(name) {
		// Offered or not, it resolves, so stored results still read.
		offered := slices.ContainsFunc(r.resources, func(d tool.Definition) bool { return d.Tool.Name == name })

		return resourceTranslator{name: name, offered: offered}, true
	}
	if !strings.HasPrefix(name, mcp.Prefix) {
		return nil, false
	}
	i := slices.IndexFunc(r.tools, func(t mcp.Tool) bool { return t.Name == name })
	if i < 0 {
		return mcpTranslator{name: name}, true
	}

	return mcpTranslator{name: name, tool: &r.tools[i], gate: r.gate}, true
}

// mcpTranslator submits an MCP call as a remote job and reads its result.
type mcpTranslator struct {
	name string
	tool *mcp.Tool // nil when no running server offers the name
	gate mcpGate
}

func (t mcpTranslator) Translate(ctx tool.Context, call llm.ToolCall) tool.CallStatus {
	return t.decide(t.gate.ctx, call)(ctx)
}

// decide checks the arguments, and the call's approval under ctx.
func (t mcpTranslator) decide(ctx context.Context, call llm.ToolCall) submit {
	if t.tool == nil {
		return refuse(tool.ErrorStatus(fmt.Sprintf("tool %q is not available: no running MCP server offers it", t.name), 0))
	}
	args := bytes.TrimSpace([]byte(call.Arguments))
	if len(args) == 0 {
		args = []byte("{}")
	}
	// Checked before asking, so the user never approves a call that cannot run.
	if !json.Valid(args) || args[0] != '{' {
		return refuse(tool.ErrorStatus("the arguments must be a JSON object", 0))
	}
	if reason := t.gate.check(ctx, *t.tool, string(args)); reason != "" {
		return refuse(tool.ErrorStatus(reason, 0))
	}

	return submitMCP(mcpPlan{Server: t.tool.Server, Tool: t.tool.Tool, Arguments: args})
}

// check returns why the call may not run, or "". It blocks while the user
// decides, as a sandbox escalation does, at most until ctx ends.
func (g mcpGate) check(ctx context.Context, t mcp.Tool, args string) string {
	if g.m != nil {
		if mode, ok := g.m.ToolApproval(t.Name); ok {
			t.Approval = mode // "don't ask again" applies at once
		}
	}
	if g.approved != nil && g.approved(t.Name) {
		t.Approval = mcp.ApprovalApprove
	}
	if !t.NeedsApproval() || g.yolo != nil && g.yolo() || hookAllowed(ctx) {
		return ""
	}
	why := fmt.Sprintf("the MCP tool %s needs the user's approval (approval_mode %q)", t.Name, t.Approval)
	if g.never {
		return "not run: " + why + ", and approval_policy is never. Tell the user what you wanted to do with it."
	}
	if g.ask == nil || ctx == nil {
		return "not run: " + why + ", and no one can approve it in this run. Tell the user what you wanted to do with it."
	}
	p := approval.Prompt{Command: t.Name + " " + args, Justification: t.Description}
	if g.m != nil {
		p.MCPTool = t.Name
	}
	answer, settled := approval.AskUnless(ctx, g.ask, p, g.changes, func() bool { return g.alwaysAllowed(t.Name) })
	if settled {
		return ""
	}
	if answer == approval.ApproveTool && g.m != nil {
		if err := g.m.AlwaysAllow(t.Name); err != nil && g.warn != nil {
			g.warn(err.Error())
		}
		g.changes.Notify()
	}
	if reason, ok := answer.DeclineReason(); ok {
		return "not run: " + reason
	}
	if !answer.Approved() {
		return "not run: the user declined the MCP tool " + t.Name + ". Ask the user how to proceed."
	}

	return ""
}

// alwaysAllowed reports whether "don't ask again" has allowed the tool.
func (g mcpGate) alwaysAllowed(name string) bool {
	if g.m == nil {
		return false
	}
	mode, ok := g.m.ToolApproval(name)

	return ok && mode == mcp.ApprovalApprove
}

// resourceTranslator submits a resource request as an MCP remote job.
type resourceTranslator struct {
	name    string
	offered bool
}

func (t resourceTranslator) Translate(ctx tool.Context, call llm.ToolCall) tool.CallStatus {
	if !t.offered {
		return tool.ErrorStatus(fmt.Sprintf("tool %q is not available: no MCP server is configured", t.name), 0)
	}
	plan, err := resourcePlan(t.name, call.Arguments)
	if err != nil {
		return tool.ErrorStatus(err.Error(), 0)
	}

	return submitMCP(plan)(ctx)
}

func (t resourceTranslator) TranslateResult(callID string, status tool.CallStatus, ops []operation.Operation) (llm.ToolResult, error) {
	return mcpTranslator{name: t.name}.TranslateResult(callID, status, ops)
}

// submitMCP submits the plan as an MCP remote job.
func submitMCP(plan mcpPlan) submit {
	data, err := json.Marshal(plan)
	if err != nil {
		return refuse(tool.ErrorStatus(fmt.Sprintf("failed to encode the MCP call: %v", err), 0))
	}
	spec, err := operation.NewRemoteJobSpec(operation.RemoteJobPlan{Type: mcpPlanType, Version: mcpPlanVersion, Data: jsontext.Value(data)})
	if err != nil {
		return refuse(tool.ErrorStatus(fmt.Sprintf("failed to build the MCP call: %v", err), 0))
	}

	return func(tc tool.Context) tool.CallStatus {
		return tool.CallStatus{WaitingFor: []operation.ID{tc.Submit(spec)}}
	}
}

func (t mcpTranslator) TranslateResult(callID string, status tool.CallStatus, ops []operation.Operation) (llm.ToolResult, error) {
	result := llm.ToolResult{CallID: callID}
	text := func(s string) {
		result.Output = append(result.Output, llm.ToolResultOutput{Kind: llm.ToolResultText, Value: s})
	}
	if status.Error != "" {
		text("Error: " + status.Error)

		return result, nil
	}
	if len(ops) != 1 {
		return result, fmt.Errorf("MCP call %q has %d operations, want 1", callID, len(ops))
	}
	state, err := operation.DecodeRemoteJobState(ops[0])
	if err != nil {
		return result, fmt.Errorf("failed to decode MCP call %q: %w", callID, err)
	}
	switch ops[0].Status {
	case operation.StatusReady, operation.StatusAwaiting, operation.StatusCanceling:
		text("The MCP call is still running.")
	case operation.StatusFailed:
		text("Error: " + state.TerminalError)
	case operation.StatusCanceled:
		text("Error: the MCP call was canceled.")
	case operation.StatusCompleted:
		var outputs mcpOutputs
		if len(state.Handle) > 0 {
			if err := json.Unmarshal(state.Handle, &outputs); err != nil {
				return result, fmt.Errorf("failed to decode MCP call %q images: %w", callID, err)
			}
		}
		if state.TerminalResult != "" || len(outputs.Images) == 0 {
			text(state.TerminalResult)
		}
		for _, img := range outputs.Images {
			result.Output = append(result.Output, llm.ToolResultOutput{Kind: llm.ToolResultImage, Value: img})
		}
	}

	return result, nil
}
