package app

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/viktordanov/uah/internal/models"
	"github.com/viktordanov/uah/internal/session"
)

// NewModels is the model catalog for the settings' provider and base URL,
// cached in the state directory.
func NewModels(stateDir string, s session.Settings, getenv func(string) string) *models.Manager {
	return models.New(models.Options{
		Dir: models.CacheDir(stateDir), Provider: s.Provider, BaseURL: s.BaseURL, Getenv: getenv,
	})
}

// checkModels asks the provider for its list, as a session would, settles
// the default model from it, and checks that the model is in it. An
// unknown model is a warning: the provider decides.
func checkModels(ctx context.Context, m *models.Manager, r *Resolved) Check {
	c := m.Catalog(ctx, m.Provider(), models.Online)
	r.SettleModel(c)
	model := r.Settings.Model
	if c.Err != nil {
		return warn("models", fmt.Sprintf("%s did not list its models (%v); using the %s list of %d", c.Provider, c.Err, c.Origin, len(c.Models)),
			"check the credentials and the network; unknown models are passed through")
	}
	if c.Origin == models.OriginNone || len(c.Models) == 0 {
		return warn("models", c.Provider+" lists no models", "for ollama, pull a model; otherwise check the account")
	}
	detail := fmt.Sprintf("%d models available to this login (%s list from %s)", len(c.Visible()), c.Origin, c.Provider)
	if model == "" {
		return ok("models", detail)
	}
	if err := c.Check(model); err != nil {
		return warn("models", detail+"; "+err.Error(), "pick one from `uah models`, or ignore this if the provider accepts it")
	}

	return ok("models", detail+"; "+model+" is in it")
}

// ModelLine is one line of `uah models`.
func ModelLine(m models.Model) string {
	parts := []string{m.ID}
	if m.ContextWindow > 0 {
		parts = append(parts, fmt.Sprintf("%dk context", m.ContextWindow/1000))
	}
	if m.DefaultEffort != "" {
		parts = append(parts, "effort "+m.DefaultEffort+" ("+strings.Join(m.ReasoningLevels, ", ")+")")
	}
	if m.SupportsPriority() {
		parts = append(parts, "fast")
	}
	if slices.Contains(m.ServiceTiers, "ultrafast") {
		parts = append(parts, "ultrafast")
	}
	if m.Hidden {
		parts = append(parts, "hidden")
	}

	return strings.Join(parts, " · ")
}

// ListModels resolves the provider as a session with these inputs would
// and returns its catalog: the cache while fresh, else the provider's list,
// or with refresh always the provider's. The model is the one such a
// session would use, the default settled from the list.
func ListModels(ctx context.Context, in Inputs, refresh bool, getenv func(string) string) (models.Catalog, string, error) {
	stateDir, r, err := resolveOnly(in)
	if err != nil {
		return models.Catalog{}, "", err
	}
	strategy := models.OnlineIfUncached
	if refresh {
		strategy = models.Online
	}
	m := NewModels(stateDir, r.Settings, getenv)
	c := m.Catalog(ctx, m.Provider(), strategy)
	r.SettleModel(c)

	return c, r.Settings.Model, nil
}
