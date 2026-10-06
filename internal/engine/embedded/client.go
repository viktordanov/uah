package embedded

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/viktordanov/uah-core/harness/llm"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/engine"
)

// client resolves the provider, model, and credentials as the runner does
// and returns the model and the switching adapter.
func (w *wiring) client(req core.Request, opts engine.Options) (string, *switcher, error) {
	p, err := w.e.provider(req.Provider)
	if err != nil {
		return "", nil, err
	}
	baseURL := strings.TrimSpace(req.BaseURL)
	if baseURL == "" {
		baseURL = p.BaseURL
	}
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = p.DefaultModel
	}
	if model == "" {
		return "", nil, errors.New("the model must be set for provider " + p.Name)
	}
	apiKey, err := w.apiKey(p)
	if err != nil {
		return "", nil, err
	}
	maxAttempts, err := w.maxAttempts(req)
	if err != nil {
		return "", nil, err
	}
	start := variant{priority: opts.ServiceTier == tierPriority, ultra: req.Effort == effortUltra}
	sw, err := newSwitcher(model, start, maxAttempts, func(v variant) (Client, error) {
		if v.priority && !p.Priority {
			return nil, errNoPriority
		}
		c, err := p.NewClient(ClientConfig{APIKey: apiKey, BaseURL: baseURL, MaxAttempts: maxAttempts, Priority: v.priority, Ultra: v.ultra, Getenv: w.getenv, transports: &w.e.transports})
		if err != nil {
			return nil, fmt.Errorf("failed to create the %s client: %w", p.Name, err)
		}

		return c, nil
	})
	if err == nil {
		sw.guardianMarkers = guardianMarkers(p.Name, w.e.cfg.Review.GuardianMarkers)
		sw.verbosity = func(model string) llm.Verbosity {
			v, _ := w.e.models.Verbosity(p.Name, model, w.e.cfg.Verbosity)

			return llm.Verbosity(v)
		}
	}

	return model, sw, err
}

// apiKey returns UAH_LLM_API_KEY, else the provider's key
// variable. A provider without a key variable needs no key.
func (w *wiring) apiKey(p Provider) (string, error) { return providerKey(p, w.getenv) }

func providerKey(p Provider, getenv func(string) string) (string, error) {
	if p.APIKeyEnv == "" {
		return "", nil
	}
	key := strings.TrimSpace(getenv("UAH_LLM_API_KEY"))
	if key == "" {
		key = strings.TrimSpace(getenv(p.APIKeyEnv))
	}
	if key == "" {
		return "", fmt.Errorf("UAH_LLM_API_KEY or %s must be set", p.APIKeyEnv)
	}

	return key, nil
}

// CheckCredentials builds the provider's client the way a run does, which
// reads and checks its credentials, and closes it without calling the
// model. Errors never include a secret.
func CheckCredentials(provider string, getenv func(string) string) error {
	for _, p := range DefaultProviders() {
		if p.Name != provider {
			continue
		}
		key, err := providerKey(p, getenv)
		if err != nil {
			return err
		}
		c, err := p.NewClient(ClientConfig{APIKey: key, BaseURL: p.BaseURL, MaxAttempts: 1, Getenv: getenv})
		if err != nil {
			return fmt.Errorf("failed to create the %s client: %w", p.Name, err)
		}

		return c.Close() // closing an unused client
	}

	return fmt.Errorf("unsupported provider %q", provider)
}

// maxAttempts returns the request's attempt limit, else the environment's,
// else uah's default (the runner's is lower).
func (w *wiring) maxAttempts(req core.Request) (int, error) {
	n := engine.DefaultMaxAttempts
	if req.MaxAttempts > 0 {
		n = req.MaxAttempts
	} else if v := strings.TrimSpace(w.getenv("UAH_LLM_MAX_ATTEMPTS")); v != "" {
		parsed, err := strconv.Atoi(v)
		if err != nil || parsed <= 0 {
			return 0, fmt.Errorf("invalid UAH_LLM_MAX_ATTEMPTS %q", v)
		}
		n = parsed
	}

	return n, nil
}
