package agents

import "context"

// checkModel refuses a model the provider does not offer, before the child
// starts. Codex checks the spawn call's model, else [agents]
// default_subagent_model, and names the available models; the check itself
// is internal/models.Validate, with the provider's live list. A fork
// never takes the default (childOptions), so only its call's is checked.
func (m *Manager) checkModel(ctx context.Context, requested string, fork bool) error {
	if !fork {
		requested = first(requested, m.cfg.Model)
	}
	if requested == "" || m.cfg.Validate == nil {
		return nil
	}

	return m.cfg.Validate(ctx, requested)
}
