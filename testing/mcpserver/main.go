// Command mcpserver is a stdio MCP server for tests, built with the official
// Go SDK. Its tools: echo, image, structured, fail, sleep, crash (the process
// exits), big (n bytes of text), stderr (writes the text to standard error),
// env (the named variable's value), overlap (sleeps and reports the most
// calls it saw running at once), and add_tool (adds a tool named by the
// text, so the server sends notifications/tools/list_changed).
// MCPSERVER_START_DELAY (a duration) delays its start; MCPSERVER_EXTRA_TOOLS
// (a count) adds that many tools named tool_0000 and up. MCPSERVER_LOG (a
// file) gets a line "<pid> <method>" for each request and notification the
// server receives, before it handles it, and "<pid> closed" when its input
// ends, so a test can count connections. MCPSERVER_LEGACY=1 refuses
// server/discover, so a client falls back to initialize, as with a server
// from before the 2026-07-28 protocol. MCPSERVER_FAIL_FILE (a path) makes
// the server exit with status 4 as it starts while that file exists, so a
// test can make restarts fail.
//
// Resources: test://greeting (text), test://pixel (a PNG blob), and the
// template test://items/{id}; add_resource adds test://<text>, so the
// server sends notifications/resources/list_changed. Prompts: review
// (arguments file, required, and focus) returns a user message with the
// arguments and an embedded resource, then an assistant message; add_prompt
// adds a prompt named by the text, so the server sends
// notifications/prompts/list_changed.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// png is a 1x1 PNG.
var png = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\b\x02\x00\x00\x00\x90wS\xde\x00\x00\x00\x11IDATx\x9c" +
	"\x00\x04\x00\xfb\xff\x02\xff\x00\x00\x03\x00\x03\t\x01\x02\xf9?c\xe3\x00\x00\x00\x00IEND\xaeB`\x82")

// MIME types and the user role.
const (
	mimePNG  = "image/png"
	mimeText = "text/plain"
	roleUser = "user"
)

type args struct {
	Text string `json:"text"`
	Ms   int    `json:"ms"`
	N    int    `json:"n"`
	// Mark, when set, is a file sleep creates as it starts, so a test can
	// wait for the call to be running instead of guessing with a delay.
	Mark string `json:"mark"`
}

// overlap counts calls to the overlap tool running at once.
var overlap struct {
	mu        sync.Mutex
	now, most int
}

func main() {
	if d, err := time.ParseDuration(os.Getenv("MCPSERVER_START_DELAY")); err == nil {
		time.Sleep(d)
	}
	if f := os.Getenv("MCPSERVER_FAIL_FILE"); f != "" {
		if _, err := os.Stat(f); err == nil {
			fmt.Fprintln(os.Stderr, "failing as asked")
			os.Exit(4)
		}
	}
	s := sdk.NewServer(&sdk.Implementation{Name: "mcpserver", Version: "1"}, nil)
	logTo, legacy := os.Getenv("MCPSERVER_LOG"), os.Getenv("MCPSERVER_LEGACY") == "1"
	s.AddReceivingMiddleware(func(next sdk.MethodHandler) sdk.MethodHandler {
		return func(ctx context.Context, method string, req sdk.Request) (sdk.Result, error) {
			if logTo != "" {
				logLine(logTo, method)
			}
			if legacy && method == "server/discover" {
				return nil, fmt.Errorf("method %q not found", method)
			}

			return next(ctx, method, req)
		}
	})
	add(s, "echo", "Echo the text.", func(a args) *sdk.CallToolResult {
		return text("echo: " + a.Text + " env=" + os.Getenv("MCPSERVER_GREETING"))
	})
	add(s, "image", "Return a 1x1 image.", func(args) *sdk.CallToolResult {
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "a pixel"}, &sdk.ImageContent{Data: png, MIMEType: mimePNG}}}
	})
	add(s, "structured", "Return structured content.", func(args) *sdk.CallToolResult {
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "ignored"}}, StructuredContent: map[string]any{"n": 42}}
	})
	add(s, "fail", "Fail as a tool.", func(args) *sdk.CallToolResult {
		r := text("it failed")
		r.IsError = true

		return r
	})
	add(s, "sleep", "Sleep for ms milliseconds.", func(a args) *sdk.CallToolResult {
		if a.Mark != "" {
			_ = os.WriteFile(a.Mark, nil, 0o600)
		}
		time.Sleep(time.Duration(a.Ms) * time.Millisecond)

		return text("slept")
	})
	add(s, "crash", "Exit the server.", func(args) *sdk.CallToolResult {
		os.Exit(3)

		return nil
	})
	add(s, "big", "Return n bytes of text.", func(a args) *sdk.CallToolResult {
		return text(strings.Repeat("x", a.N))
	})
	add(s, "stderr", "Write the text to standard error.", func(a args) *sdk.CallToolResult {
		fmt.Fprintln(os.Stderr, a.Text)

		return text("logged")
	})
	add(s, "env", "Return the named environment variable.", func(a args) *sdk.CallToolResult {
		v, ok := os.LookupEnv(a.Text)
		if !ok {
			return text("unset")
		}

		return text(v)
	})
	add(s, "overlap", "Sleep ms milliseconds; report the most calls running at once.", func(a args) *sdk.CallToolResult {
		overlap.mu.Lock()
		overlap.now++
		overlap.most = max(overlap.most, overlap.now)
		overlap.mu.Unlock()
		time.Sleep(time.Duration(a.Ms) * time.Millisecond)
		overlap.mu.Lock()
		defer overlap.mu.Unlock()
		overlap.now--

		return text(strconv.Itoa(overlap.most))
	})
	add(s, "add_tool", "Add a tool named by the text.", func(a args) *sdk.CallToolResult {
		add(s, a.Text, "Added at run time.", func(args) *sdk.CallToolResult { return text("new") })

		return text("added")
	})
	add(s, "add_resource", "Add a resource test://<text>.", func(a args) *sdk.CallToolResult {
		s.AddResource(&sdk.Resource{URI: "test://" + a.Text, Name: a.Text, MIMEType: mimeText}, textResource("added "+a.Text))

		return text("added")
	})
	add(s, "add_prompt", "Add a prompt named by the text.", func(a args) *sdk.CallToolResult {
		s.AddPrompt(&sdk.Prompt{Name: a.Text, Description: "Added at run time."}, func(context.Context, *sdk.GetPromptRequest) (*sdk.GetPromptResult, error) {
			return &sdk.GetPromptResult{Messages: []*sdk.PromptMessage{{Role: roleUser, Content: &sdk.TextContent{Text: "added prompt"}}}}, nil
		})

		return text("added")
	})
	resources(s)
	extra, _ := strconv.Atoi(os.Getenv("MCPSERVER_EXTRA_TOOLS"))
	for i := range extra {
		add(s, fmt.Sprintf("tool_%04d", i), "An extra tool.", func(args) *sdk.CallToolResult { return text("extra") })
	}
	err := s.Run(context.Background(), &sdk.StdioTransport{})
	if logTo != "" {
		logLine(logTo, "closed")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// logLine appends "<pid> <what>" to the file.
func logLine(file, what string) {
	f, err := os.OpenFile(file, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(f, "%d %s\n", os.Getpid(), what)
	_ = f.Close()
}

func add(s *sdk.Server, name, description string, fn func(args) *sdk.CallToolResult) {
	const typ = "type"
	schema := map[string]any{typ: "object", "properties": map[string]any{
		"text": map[string]any{typ: "string"}, "ms": map[string]any{typ: "integer"}, "n": map[string]any{typ: "integer"},
	}}
	// Read-only, so approval_mode auto runs them without asking, as Codex does.
	annotations := &sdk.ToolAnnotations{ReadOnlyHint: true}
	s.AddTool(&sdk.Tool{Name: name, Description: description, InputSchema: schema, Annotations: annotations}, func(_ context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		var a args
		if len(req.Params.Arguments) > 0 {
			if err := json.Unmarshal(req.Params.Arguments, &a); err != nil {
				return nil, fmt.Errorf("bad arguments: %w", err)
			}
		}

		return fn(a), nil
	})
}

func text(s string) *sdk.CallToolResult {
	return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: s}}}
}

// resources adds the test resources, the template, and the review prompt.
func resources(s *sdk.Server) {
	s.AddResource(&sdk.Resource{URI: "test://greeting", Name: "greeting", Title: "Greeting", Description: "A friendly greeting.", MIMEType: mimeText},
		textResource("hello from the resource"))
	s.AddResource(&sdk.Resource{URI: "test://pixel", Name: "pixel", MIMEType: mimePNG},
		func(_ context.Context, req *sdk.ReadResourceRequest) (*sdk.ReadResourceResult, error) {
			return &sdk.ReadResourceResult{Contents: []*sdk.ResourceContents{{URI: req.Params.URI, MIMEType: mimePNG, Blob: png}}}, nil
		})
	s.AddResourceTemplate(&sdk.ResourceTemplate{URITemplate: "test://items/{id}", Name: "item", Description: "An item by ID."},
		func(_ context.Context, req *sdk.ReadResourceRequest) (*sdk.ReadResourceResult, error) {
			return &sdk.ReadResourceResult{Contents: []*sdk.ResourceContents{{URI: req.Params.URI, MIMEType: mimeText, Text: "item " + strings.TrimPrefix(req.Params.URI, "test://items/")}}}, nil
		})
	s.AddPrompt(&sdk.Prompt{
		Name: "review", Description: "Review a file.",
		Arguments: []*sdk.PromptArgument{{Name: "file", Description: "The file to review.", Required: true}, {Name: "focus", Description: "What to look at."}},
	}, func(_ context.Context, req *sdk.GetPromptRequest) (*sdk.GetPromptResult, error) {
		a := req.Params.Arguments
		return &sdk.GetPromptResult{Description: "A review", Messages: []*sdk.PromptMessage{
			{Role: roleUser, Content: &sdk.TextContent{Text: "Review " + a["file"] + " for " + a["focus"] + "."}},
			{Role: roleUser, Content: &sdk.EmbeddedResource{Resource: &sdk.ResourceContents{URI: "test://greeting", MIMEType: mimeText, Text: "hello from the resource"}}},
			{Role: "assistant", Content: &sdk.TextContent{Text: "I will review it."}},
		}}, nil
	})
}

func textResource(body string) sdk.ResourceHandler {
	return func(_ context.Context, req *sdk.ReadResourceRequest) (*sdk.ReadResourceResult, error) {
		return &sdk.ReadResourceResult{Contents: []*sdk.ResourceContents{{URI: req.Params.URI, MIMEType: mimeText, Text: body}}}, nil
	}
}
