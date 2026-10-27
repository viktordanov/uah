package embedded

import (
	"errors"
	"sync/atomic"

	"github.com/viktordanov/uah-core/harness/llm"
	"github.com/viktordanov/uah-core/harness/llm/clients/ollama"
	"github.com/viktordanov/uah-core/harness/llm/clients/openaicodex"
	"github.com/viktordanov/uah-core/harness/primitives"
)

// Client is a model client the engine can close.
type Client interface {
	llm.Adapter
	Close() error
}

// ClientConfig is what a provider needs to build a client.
type ClientConfig struct {
	APIKey      string
	BaseURL     string
	MaxAttempts int
	// Priority asks for priority processing (service_tier "priority").
	Priority bool
	// Ultra sends reasoning effort ultra (see variant).
	Ultra  bool
	Getenv func(string) string
	// transports are the engine's; nil gives the client its own.
	transports *transports
	// turnState is the run's, which its clients share (turnstate.go); nil
	// gives the client its own.
	turnState *atomic.Pointer[string]
}

// Provider mirrors unreal-agent-runner v0.1.1's provider table
// (cmd/internal/agentrunner/providers.go).
type Provider struct {
	Name         string
	BaseURL      string
	DefaultModel string
	// APIKeyEnv is empty when the client finds its own credentials.
	APIKeyEnv string
	// Priority says whether the provider accepts service_tier "priority".
	Priority bool
	// WebSearch says whether the provider runs the hosted web_search tool.
	WebSearch bool
	// RemoteCompaction says whether the provider compacts a history into an
	// encrypted item (remotecompact.go), as Codex's OpenAI providers do.
	RemoteCompaction bool
	NewClient        func(ClientConfig) (Client, error)
}

var errNoPriority = errors.New("this provider has no priority processing")

// Priority reports whether the named provider of DefaultProviders accepts
// priority processing, for checks made before an engine exists.
func Priority(name string) bool {
	for _, p := range DefaultProviders() {
		if p.Name == name {
			return p.Priority
		}
	}

	return false
}

// DefaultProviders are the runner's providers, with clients whose retries
// the engine can watch (clients.go).
func DefaultProviders() []Provider {
	return []Provider{
		{Name: "ollama", BaseURL: ollama.BaseURL, NewClient: noPriority(ollamaClient)},
		{
			Name: "openai", BaseURL: "https://api.openai.com/v1", DefaultModel: "gpt-6-astra",
			APIKeyEnv: "OPENAI_API_KEY", Priority: true, WebSearch: true, RemoteCompaction: true, NewClient: openaiClient,
		},
		{Name: "openai-codex", BaseURL: openaicodex.BaseURL, Priority: true, WebSearch: true, RemoteCompaction: true, NewClient: codexClient},
		{Name: "openrouter", BaseURL: "https://openrouter.ai/api/v1", APIKeyEnv: "OPENROUTER_API_KEY", NewClient: noPriority(openrouterClient)},
		{Name: "fireworks", BaseURL: "https://api.fireworks.ai/inference/v1", APIKeyEnv: "FIREWORKS_API_KEY", NewClient: noPriority(fireworksClient)},
	}
}

// noPriority refuses priority processing, for a provider without it.
func noPriority(build func(ClientConfig) (Client, error)) func(ClientConfig) (Client, error) {
	return func(c ClientConfig) (Client, error) {
		if c.Priority {
			return nil, errNoPriority
		}

		return build(c)
	}
}

// remoteAdapter is a Responses adapter and the remote client it closes.
type remoteAdapter struct {
	llm.Adapter

	remote *primitives.RemoteClient
}

func (a remoteAdapter) Close() error { return a.remote.Close() }
