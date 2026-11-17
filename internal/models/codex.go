package models

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"slices"
	"sync"
)

// bundledJSON is the offline fallback: the fields uah uses from Codex's
// codex-rs/models-manager/models.json at rust-v0.159.1, in Codex's shape so
// the ChatGPT backend's response and this file share one parser. The
// supports_reasoning_effort_updates flags are from later lists: Codex's
// models.json at main b741e48 (gpt-6-astra, gpt-6.1-sol) and the ChatGPT
// backend's list of 2026-10-03 (gpt-6-sol, gpt-6-luna).
//
//go:embed bundled.json
var bundledJSON []byte

// CodexClientVersion is the client_version uah sends to the ChatGPT backend's
// models endpoint. Keep it current independently of the bundled catalog:
// the backend can gate new models on this version. Codex sends its own
// version (client_version_to_whole in models-manager/src/lib.rs).
const CodexClientVersion = "0.169.0"

// codexModels is Codex's ModelsResponse (codex-rs/protocol/src/openai_models.rs).
type codexModels struct {
	Models []codexModel `json:"models"`
}

// codexModel is the part of Codex's ModelInfo uah keeps.
type codexModel struct {
	Slug             string `json:"slug"`
	DisplayName      string `json:"display_name"`
	Description      string `json:"description"`
	Visibility       string `json:"visibility"`
	Priority         int    `json:"priority"`
	ContextWindow    int64  `json:"context_window"`
	MaxContextWindow int64  `json:"max_context_window"`
	DefaultReasoning string `json:"default_reasoning_level"`
	SupportedLevels  []struct {
		Effort      string `json:"effort"`
		Description string `json:"description"`
	} `json:"supported_reasoning_levels"`
	AdditionalSpeedTiers []string `json:"additional_speed_tiers"`
	ServiceTiers         []struct {
		ID string `json:"id"`
	} `json:"service_tiers"`
	DefaultServiceTier string   `json:"default_service_tier"`
	AvailableInPlans   []string `json:"available_in_plans"`
	MinClientVersion   string   `json:"minimal_client_version"`
	ApplyPatchToolType string   `json:"apply_patch_tool_type"`
	SupportVerbosity   bool     `json:"support_verbosity"`
	DefaultVerbosity   string   `json:"default_verbosity"`
	EffortUpdates      bool     `json:"supports_reasoning_effort_updates"`
}

func (c codexModel) model() Model {
	m := Model{
		ID: c.Slug, DisplayName: c.DisplayName, Description: c.Description,
		// Codex's resolved_context_window: context_window, else max_context_window.
		ContextWindow: c.ContextWindow, MaxContextWindow: c.MaxContextWindow,
		DefaultEffort: c.DefaultReasoning, DefaultServiceTier: c.DefaultServiceTier,
		Priority: c.Priority, Hidden: c.Visibility != "" && c.Visibility != "list",
		Plans: c.AvailableInPlans, MinClientVersion: c.MinClientVersion, ApplyPatchTool: c.ApplyPatchToolType,
		SupportsVerbosity: c.SupportVerbosity, DefaultVerbosity: c.DefaultVerbosity, EffortUpdates: c.EffortUpdates,
	}
	if m.ContextWindow == 0 {
		m.ContextWindow = m.MaxContextWindow
	}
	for _, l := range c.SupportedLevels {
		m.ReasoningLevels = append(m.ReasoningLevels, l.Effort)
		if l.Description != "" {
			if m.ReasoningHelp == nil {
				m.ReasoningHelp = map[string]string{}
			}
			m.ReasoningHelp[l.Effort] = l.Description
		}
	}
	for _, t := range c.ServiceTiers {
		m.ServiceTiers = append(m.ServiceTiers, t.ID)
	}
	// Codex's deprecated additional_speed_tiers ["fast"] means priority.
	if len(m.ServiceTiers) == 0 && len(c.AdditionalSpeedTiers) > 0 {
		m.ServiceTiers = []string{"priority"}
	}

	return m
}

// parseCodex reads Codex's ModelsResponse.
func parseCodex(body []byte) ([]Model, error) {
	var resp codexModels
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("invalid models response: %w", err)
	}
	out := make([]Model, 0, len(resp.Models))
	for _, c := range resp.Models {
		if c.Slug != "" {
			out = append(out, c.model())
		}
	}
	sortModels(out)

	return out, nil
}

var bundledOnce = sync.OnceValue(func() []Model {
	models, err := parseCodex(bundledJSON)
	if err != nil {
		panic("the bundled model catalog is invalid: " + err.Error())
	}

	return models
})

// bundledProviders are the providers the bundled catalog describes: Codex's
// catalog is OpenAI's models.
var bundledProviders = map[string]bool{ProviderCodex: true, ProviderOpenAI: true}

// Bundled is the catalog shipped with uah for the provider: Codex's models
// for openai-codex and openai, and an empty list (OriginNone) for the rest.
func Bundled(provider string) Catalog {
	if !bundledProviders[provider] {
		return Catalog{Provider: provider, Origin: OriginNone}
	}

	return Catalog{Provider: provider, Origin: OriginBundled, Models: slices.Clone(bundledOnce())}
}
