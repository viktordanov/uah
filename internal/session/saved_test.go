package session_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/approval"
	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/session"
)

// TestSession_SavesSettingsInTheSidecar pins that a session keeps its
// settings in its sidecar from the start and after each change, keeping
// the source the first writer recorded, and that a live mode change
// reaches the run.
func TestSession_SavesSettingsInTheSidecar(t *testing.T) {
	dir := t.TempDir()
	eng := newFakeEngine(fakeCaps{LiveMode: true})
	s, err := session.Open(context.Background(), eng, session.Options{Settings: settings(), SessionsDir: dir, Source: session.SourceTUI})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	sc, found, err := session.ReadSidecar(dir, s.ID())
	require.NoError(t, err)
	require.True(t, found)
	require.NotNil(t, sc.Settings, "saved when the session opens")
	assert.Equal(t, session.Saved{Provider: settings().Provider, Model: settings().Model, Effort: settings().Effort, AdaptiveEffort: "off"}, *sc.Settings)

	_, err = s.Submit("go")
	require.NoError(t, err)
	run := <-eng.started
	next := settings().WithMode(approval.ModeAuto)
	next.Effort, next.ServiceTier, next.AdaptiveEffort = "low", "priority", session.AdaptiveTwoSteps
	applied, err := s.SetSettings(next)
	require.NoError(t, err)
	assert.Equal(t, session.AppliedNextRun, applied, "the fake run cannot change its effort live")
	assert.Equal(t, engine.Options{}.Mode, run.opts.Mode, "the run started in the engine's mode")

	sc, _, err = session.ReadSidecar(dir, s.ID())
	require.NoError(t, err)
	assert.Equal(t, session.SourceTUI, sc.Source)
	assert.Equal(t, session.Saved{Provider: next.Provider, Model: next.Model, Effort: "low", Fast: true, ServiceTier: "priority", AdaptiveEffort: "2-steps", Mode: approval.ModeAuto}, *sc.Settings)

	var info session.Info
	info.ApplySidecar(sc)
	assert.True(t, info.Saved)
	assert.Equal(t, "low", info.Effort)
	assert.Equal(t, approval.ModeAuto, info.Mode)
	assert.Equal(t, session.AdaptiveTwoSteps, info.AdaptiveEffort)
	require.NotNil(t, info.Fast)
	assert.True(t, *info.Fast)
}

// TestSession_ModeAppliesLive pins that a mode change alone reaches a live
// run when the engine can take it.
func TestSession_ModeAppliesLive(t *testing.T) {
	h := newHarness(t, fakeCaps{LiveMode: true})
	_, err := h.s.Submit("go")
	require.NoError(t, err)
	run := <-h.eng.started

	applied, err := h.s.SetSettings(settings().WithMode(approval.ModeReadOnly))

	require.NoError(t, err)
	assert.Equal(t, session.AppliedLive, applied)
	assert.Equal(t, approval.ModeReadOnly, run.mode)
}

func TestApplySidecar_WithoutSettingsKeepsTheRun(t *testing.T) {
	info := session.Info{Model: "from-run", Effort: "high"}
	info.ApplySidecar(session.Sidecar{Source: session.SourceRun})

	assert.Equal(t, session.SourceRun, info.Source)
	assert.Equal(t, "from-run", info.Model)
	assert.False(t, info.Saved)
	assert.Nil(t, info.Fast)
	assert.Empty(t, info.AdaptiveEffort, "the configuration's applies")
}
