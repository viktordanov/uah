package state

import (
	"fmt"

	"github.com/viktordanov/uah/internal/approval"
	"github.com/viktordanov/uah/internal/session"
)

// CycleMode is shift+tab: the next permission mode, read only, workspace,
// and auto in turn, then yolo when the session started with --yolo.
type CycleMode struct{}

func (s *State) cycleMode() (State, []Effect) {
	if s.SessionID == "" {
		return *s, nil // the settings come with the session
	}
	next := s.Settings.WithMode(s.Settings.Mode.Next(s.Yolo))

	return *s, []Effect{EffSetSettings{Settings: next}}
}

// modeHelp says what a permission mode lets the agent do.
func modeHelp(m approval.Mode) string {
	switch m {
	case approval.ModeReadOnly:
		return "commands read but write nothing; you approve anything more"
	case approval.ModeAuto:
		return "commands write the workspace; the auto-reviewer approves or declines the rest without asking you"
	case approval.ModeYolo:
		return "no sandbox and no approvals: commands can do anything your user can, and nothing asks first"
	case approval.ModeWorkspace:
	}

	return "commands write the workspace; you approve anything more (the auto-reviewer first)"
}

// settingsChanged shows what changed: the permission mode or adaptive
// effort when it did, else the model, effort, and fast mode.
func (s *State) settingsChanged(e session.SettingsChanged) {
	prev := s.Settings
	s.Settings = e.Settings
	when := "from the next run"
	if e.Applied == session.AppliedLive {
		when = "now"
	}
	if e.Settings.Mode != prev.Mode {
		s.notice(session.LevelInfo, fmt.Sprintf("%s mode: %s. Applies %s.", e.Settings.Mode.Label(), modeHelp(e.Settings.Mode), when))

		return
	}
	if e.Settings.AdaptiveEffort != prev.AdaptiveEffort {
		s.notice(session.LevelInfo, fmt.Sprintf("adaptive effort: %s, applies %s", AdaptiveLabel(e.Settings), when))

		return
	}
	fast := ""
	if e.Settings.ServiceTier != "" {
		fast = " · fast"
		if e.Settings.ServiceTier == "ultrafast" {
			fast = " · ultrafast"
		}
	}
	s.notice(session.LevelInfo, fmt.Sprintf("%s/%s · effort %s%s, applies %s", e.Settings.Provider, e.Settings.Model, e.Settings.Effort, fast, when))
}
