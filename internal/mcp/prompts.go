package mcp

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Prompt is an MCP prompt offered as a slash command, as Claude Code
// offers them: /mcp__<server>__<prompt>, its arguments in order after it.
// Codex has no MCP prompts.
type Prompt struct {
	// Command is the command without its slash: mcp__<server>__<prompt>,
	// named as tools are.
	Command     string
	Server      string
	Prompt      string // the server's own name for it
	Description string
	Arguments   []PromptArgument
}

// PromptArgument is one of a prompt's arguments.
type PromptArgument struct {
	Name        string
	Description string
	Required    bool
}

// Usage is the prompt's arguments as /help shows a command's: "<a> [b]".
func (p Prompt) Usage() string {
	words := make([]string, 0, len(p.Arguments))
	for _, a := range p.Arguments {
		if a.Required {
			words = append(words, "<"+a.Name+">")
		} else {
			words = append(words, "["+a.Name+"]")
		}
	}

	return strings.Join(words, " ")
}

// Prompts lists the prompts of the servers that are ready or restarting,
// in command order. It waits for nothing: a server still starting adds
// its prompts once it is ready.
func (m *Manager) Prompts() []Prompt {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.prompts()
}

// prompts is Prompts; the caller holds mu.
func (m *Manager) prompts() []Prompt {
	var out []Prompt
	for _, s := range m.servers {
		if s.state != StateReady && s.state != StateRestarting {
			continue
		}
		for _, p := range s.prompts {
			if p == nil {
				continue
			}
			args := make([]PromptArgument, 0, len(p.Arguments))
			for _, a := range p.Arguments {
				args = append(args, PromptArgument{Name: a.Name, Description: a.Description, Required: a.Required})
			}
			out = append(out, Prompt{Server: s.name, Prompt: p.Name, Description: cmp.Or(p.Description, p.Title), Arguments: args})
		}
	}
	slices.SortFunc(out, func(a, b Prompt) int {
		return cmp.Or(cmp.Compare(a.Server, b.Server), cmp.Compare(a.Prompt, b.Prompt))
	})
	n := newNamer()
	for i := range out {
		out[i].Command = n.name(out[i].Server, out[i].Prompt)
	}

	return out
}

// GetPrompt fills a prompt's arguments, given in the prompt's order;
// a missing required one is an error that shows the usage.
func (m *Manager) GetPrompt(ctx context.Context, p Prompt, values []string) (*sdk.GetPromptResult, error) {
	if len(values) > len(p.Arguments) && len(p.Arguments) > 0 {
		// The last argument takes the rest, so a sentence needs no quotes.
		last := len(p.Arguments) - 1
		values = append(values[:last:last], strings.Join(values[last:], " "))
	}
	args := map[string]string{}
	for i, a := range p.Arguments {
		if i < len(values) {
			args[a.Name] = values[i]
		} else if a.Required {
			return nil, fmt.Errorf("/%s needs %s: /%s %s", p.Command, a.Name, p.Command, p.Usage())
		}
	}
	var r *sdk.GetPromptResult
	err := m.request(ctx, p.Server, func(ctx context.Context, cs *sdk.ClientSession) (err error) {
		r, err = cs.GetPrompt(ctx, &sdk.GetPromptParams{Name: p.Prompt, Arguments: args})

		return err
	})
	if err != nil {
		return nil, fmt.Errorf("prompts/get failed: %w", err)
	}

	return r, nil
}
