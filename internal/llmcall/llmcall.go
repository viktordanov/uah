// Package llmcall makes one model call outside the agent loop: items in, text
// (or, when the call offers tools, tool calls) out. It runs over any runner llm.Adapter, so every provider the embedded
// engine supports works, and each call picks its model, effort, and timeout.
// Compaction summaries use it, and so can other one-shot calls such as a
// command reviewer.
package llmcall

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/viktordanov/uah-core/harness/llm"
)

// DefaultTimeout bounds a call whose Request sets no timeout.
const DefaultTimeout = 5 * time.Minute

// ErrNoText means the model answered without any text.
var ErrNoText = errors.New("the model answered without text")

// ErrContextWindow means the input did not fit the model's context window.
// Call wraps the provider's error with it, so a caller can send less.
var ErrContextWindow = errors.New("the input exceeds the model's context window")

// Request is one call.
type Request struct {
	// Model is the model ID; the adapter may fill in its own when empty.
	Model  string
	Effort llm.ReasoningEffort
	// Instructions, when set, goes first as the system message.
	Instructions string
	// Input is the conversation: messages, and optionally tool calls,
	// results, and reasoning from an earlier exchange.
	Input []llm.Item
	// Timeout bounds the call (DefaultTimeout when zero).
	Timeout time.Duration
	// CacheKey is passed to the provider for prompt caching.
	CacheKey string
	// Tools, when set, are offered to the model; the caller runs the calls
	// it makes and calls again with their results.
	Tools []llm.Tool
}

// Result is the model's answer.
type Result struct {
	// Text is the assistant's messages, joined by blank lines.
	Text  string
	Usage llm.Usage
	// Output is the response's items, to keep in a conversation, and Calls
	// its tool calls.
	Output []llm.Item
	Calls  []llm.ToolCall
}

// Call sends the request and returns the assistant's text, or the tool
// calls when the request offered tools.
func Call(ctx context.Context, adapter llm.Adapter, req Request) (Result, error) {
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	input := make([]llm.Item, 0, len(req.Input)+1)
	if req.Instructions != "" {
		input = append(input, Message(llm.RoleSystem, req.Instructions))
	}
	input = append(input, req.Input...)
	resp, err := adapter.Respond(ctx, llm.Request{
		Model: llm.Model{ID: req.Model, ReasoningEffort: req.Effort},
		Input: input, Tools: req.Tools,
	}, llm.RequestOptions{CacheKey: req.CacheKey})
	if err != nil {
		if overflow(err.Error()) {
			return Result{}, fmt.Errorf("failed to call the model: %w: %w", ErrContextWindow, err)
		}

		return Result{}, fmt.Errorf("failed to call the model: %w", err)
	}
	if f := resp.Failure; f != nil {
		if overflow(f.Code + " " + f.Message) {
			return Result{}, fmt.Errorf("failed to call the model: %w: %s: %s", ErrContextWindow, f.Code, f.Message)
		}

		return Result{}, fmt.Errorf("failed to call the model: %s: %s", f.Code, f.Message)
	}
	res := Result{Text: Text(resp), Usage: resp.Usage, Output: resp.Output}
	for _, item := range resp.Output {
		if call, ok := item.Data.(llm.ToolCall); ok && item.Type == llm.ItemToolCall {
			res.Calls = append(res.Calls, call)
		}
	}
	if res.Text == "" && (len(res.Calls) == 0 || len(req.Tools) == 0) {
		return Result{Usage: resp.Usage}, ErrNoText
	}

	return res, nil
}

// overflowSigns are how providers report an input over the window: the
// Responses API's code, and the wording of OpenAI-compatible servers.
var overflowSigns = []string{"context_length_exceeded", "maximum context length", "context window", "prompt is too long"}

// overflow reports whether a provider's error text says the input did not
// fit the context window.
func overflow(text string) bool {
	text = strings.ToLower(text)
	for _, sign := range overflowSigns {
		if strings.Contains(text, sign) {
			return true
		}
	}

	return false
}

// Text joins the assistant messages of a response.
func Text(resp llm.Response) string {
	var parts []string
	for _, item := range resp.Output {
		if m, ok := item.Data.(llm.Message); ok && m.Role == llm.RoleAssistant && strings.TrimSpace(m.Text) != "" {
			parts = append(parts, strings.TrimSpace(m.Text))
		}
	}

	return strings.Join(parts, "\n\n")
}

// Message is a message item.
func Message(role llm.Role, text string) llm.Item {
	return llm.Item{Type: llm.ItemMessage, Data: llm.Message{Role: role, Text: text}}
}
