// Package models is uah's model catalog: which models the provider offers to
// this login, with their context windows, reasoning levels, and service
// tiers. As in Codex, the list comes from the provider at runtime, is cached
// for five minutes, and falls back to a catalog bundled with uah only when
// the provider cannot be asked.
package models

import (
	"cmp"
	"math"
	"slices"
	"strings"
	"time"
)

// Model is one catalog entry. Only the ID is always set; a provider list
// without metadata leaves the rest empty unless the bundled catalog knows the
// model.
type Model struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name,omitempty"`
	Description string `json:"description,omitempty"`
	// ContextWindow is the window in tokens (0: unknown).
	ContextWindow int64 `json:"context_window,omitempty"`
	// MaxContextWindow caps a model_context_window override, as in Codex.
	MaxContextWindow int64 `json:"max_context_window,omitempty"`
	// ReasoningLevels are the efforts the model accepts, lowest first.
	ReasoningLevels []string `json:"reasoning_levels,omitempty"`
	// ReasoningHelp describes each level, when the provider does.
	ReasoningHelp map[string]string `json:"reasoning_help,omitempty"`
	// DefaultEffort is the effort used when none is chosen.
	DefaultEffort string `json:"default_effort,omitempty"`
	// ServiceTiers are the service tier IDs the model accepts, e.g. "priority".
	ServiceTiers []string `json:"service_tiers,omitempty"`
	// DefaultServiceTier is the catalog's default tier ("" for standard).
	DefaultServiceTier string `json:"default_service_tier,omitempty"`
	// Priority orders the list: lower first, as in Codex.
	Priority int `json:"priority,omitempty"`
	// Hidden models work but are not offered in menus (Codex's visibility "hide").
	Hidden bool `json:"hidden,omitempty"`
	// Plans are the ChatGPT plans the model is available in.
	Plans []string `json:"plans,omitempty"`
	// MinClientVersion is the lowest Codex client version that may use it.
	MinClientVersion string `json:"min_client_version,omitempty"`
	// ApplyPatchTool is Codex's apply_patch_tool_type ("freeform"): the
	// model is trained on the apply_patch tool (empty: not known).
	ApplyPatchTool string `json:"apply_patch_tool,omitempty"`
	// SupportsVerbosity is Codex's support_verbosity: the model takes the
	// Responses API's text.verbosity.
	SupportsVerbosity bool `json:"support_verbosity,omitempty"`
	// DefaultVerbosity is Codex's default_verbosity: low, medium, or high,
	// sent when model_verbosity is unset ("": none).
	DefaultVerbosity string `json:"default_verbosity,omitempty"`
	// EffortUpdates is Codex's supports_reasoning_effort_updates: the model
	// takes a configuration_update input item that changes the effort from
	// its place in the history on, so the request keeps its effort and its
	// prompt cache.
	EffortUpdates bool `json:"supports_reasoning_effort_updates,omitempty"`
}

// SupportsPriority reports whether the model accepts service_tier
// "priority" (uah's /fast), as Codex's service_tiers say.
func (m Model) SupportsPriority() bool { return slices.Contains(m.ServiceTiers, "priority") }

// Origin is where a catalog came from.
type Origin string

const (
	// OriginLive is a list the provider returned in this process.
	OriginLive Origin = "live"
	// OriginCached is a list the provider returned earlier, read from the cache.
	OriginCached Origin = "cached"
	// OriginBundled is the catalog shipped with uah (Codex's models.json).
	OriginBundled Origin = "bundled"
	// OriginNone means no list is known for the provider.
	OriginNone Origin = "none"
)

// Catalog is one provider's model list.
type Catalog struct {
	Provider string  `json:"provider"`
	Origin   Origin  `json:"origin"`
	Models   []Model `json:"models"`
	// FetchedAt is when the provider returned the list (zero for bundled).
	FetchedAt time.Time `json:"fetched_at,omitzero"`
	// Err is why the last refresh failed, if it did; the catalog is then the
	// cache or the bundled list.
	Err error `json:"-"`
}

// Authoritative reports whether the list came from the provider, so a model
// missing from it is really unavailable. The bundled list is not: a model
// newer than uah must still work.
func (c Catalog) Authoritative() bool {
	return (c.Origin == OriginLive || c.Origin == OriginCached) && len(c.Models) > 0
}

// OffersTier checks the exact model in a catalog returned for this login.
func (c Catalog) OffersTier(id, tier string) bool {
	if !c.Authoritative() || c.Err != nil {
		return false
	}
	m, ok, _ := c.Lookup(id)
	return ok && slices.Contains(m.ServiceTiers, tier)
}

// Visible are the models menus offer: not hidden, in priority order.
func (c Catalog) Visible() []Model {
	out := make([]Model, 0, len(c.Models))
	for _, m := range c.Models {
		if !m.Hidden {
			out = append(out, m)
		}
	}

	return out
}

// IDs are the visible models' IDs, in priority order.
func (c Catalog) IDs() []string {
	visible := c.Visible()
	ids := make([]string, 0, len(visible))
	for _, m := range visible {
		ids = append(ids, m.ID)
	}

	return ids
}

// Lookup finds the model by exact ID. Unknown, it returns up to three near
// misses: the same words in another order (gpt-luna-6 → gpt-6-luna), then
// small edit distances.
func (c Catalog) Lookup(id string) (Model, bool, []string) {
	for _, m := range c.Models {
		if m.ID == id {
			return m, true, nil
		}
	}
	ids := make([]string, 0, len(c.Models))
	for _, m := range c.Models {
		ids = append(ids, m.ID)
	}

	return Model{}, false, Suggest(id, ids)
}

// Metadata finds the entry whose metadata applies to id, as Codex's
// construct_model_info_from_candidates does: an exact ID, else the longest
// ID that prefixes it (gpt-5.5-2026-01-01 → gpt-5.5), else the same after
// one simple namespace (openai/gpt-5.5 → gpt-5.5).
func (c Catalog) Metadata(id string) (Model, bool) {
	if m, ok, _ := c.Lookup(id); ok {
		return m, true
	}
	if m, ok := longestPrefix(id, c.Models); ok {
		return m, true
	}
	namespace, rest, found := strings.Cut(id, "/")
	if !found || strings.Contains(rest, "/") || !simpleNamespace(namespace) {
		return Model{}, false
	}

	return longestPrefix(rest, c.Models)
}

// Verbosity is the text.verbosity a request to the model carries, as
// Codex's ModelClient picks it (core/src/client.rs): for a model whose entry
// supports verbosity, override (model_verbosity) else the entry's default;
// for any other model, or one no entry describes, none. ignored reports an
// override the model does not take, which Codex warns about.
func (c Catalog) Verbosity(model, override string) (verbosity string, ignored bool) {
	if m, ok := c.Metadata(model); ok && m.SupportsVerbosity {
		return cmp.Or(override, m.DefaultVerbosity), false
	}

	return "", override != ""
}

func longestPrefix(id string, models []Model) (Model, bool) {
	var best Model
	for _, m := range models {
		if strings.HasPrefix(id, m.ID) && len(m.ID) > len(best.ID) {
			best = m
		}
	}

	return best, best.ID != ""
}

func simpleNamespace(s string) bool {
	return s != "" && !strings.ContainsFunc(s, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' && r != '-'
	})
}

// sortModels orders by priority, as Codex's picker does, then by ID; a
// model without a priority comes after those with one.
func sortModels(models []Model) {
	rank := func(m Model) int {
		if m.Priority <= 0 {
			return math.MaxInt
		}

		return m.Priority
	}
	slices.SortStableFunc(models, func(a, b Model) int {
		if c := cmp.Compare(rank(a), rank(b)); c != 0 {
			return c
		}

		return strings.Compare(a.ID, b.ID)
	})
}
