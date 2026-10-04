package session

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/approval"
	"github.com/viktordanov/uah/internal/toolpolicy"
)

// Saved are the settings a session keeps in its sidecar whenever they
// change, so a resumed session starts with what it last used: its model
// (with the provider it belongs to), effort, fast mode, adaptive effort,
// and permission mode. A flag still wins over them, and they win over the
// configuration.
type Saved struct {
	Provider       string        `json:"provider,omitempty"`
	Model          string        `json:"model,omitempty"`
	Effort         string        `json:"effort,omitempty"`
	Fast           bool          `json:"fast"`
	AdaptiveEffort string        `json:"adaptive_effort,omitempty"`
	Mode           approval.Mode `json:"permission_mode,omitempty"`
}

// savedOf is what the sidecar keeps of the settings.
func savedOf(s Settings) Saved {
	return Saved{
		Provider: s.Provider, Model: s.Model, Effort: s.Effort, Fast: s.ServiceTier != "",
		AdaptiveEffort: cmp.Or(s.AdaptiveEffort, AdaptiveOff), Mode: s.Mode,
	}
}

// ApplySidecar adds what the sidecar records to the summary: the source,
// the parent, the last item (LastSequence, and LastActivity when later),
// and the saved settings, which replace the provider, model,
// and effort of the newest run. A session without saved settings (from
// before uah kept them) keeps its newest run's.
func (in *Info) ApplySidecar(sc Sidecar) {
	in.Source, in.Parent, in.LastSequence, in.Tools = sc.Source, sc.Parent, sc.LastSequence, sc.Tools
	if sc.LastActivity.After(in.LastActivity) {
		in.LastActivity = sc.LastActivity
	}
	if sc.Settings == nil {
		return
	}
	sv := sc.Settings
	in.Saved = true
	in.Provider, in.Model, in.Effort = cmp.Or(sv.Provider, in.Provider), cmp.Or(sv.Model, in.Model), cmp.Or(sv.Effort, in.Effort)
	fast := sv.Fast
	in.Fast, in.AdaptiveEffort, in.Mode = &fast, sv.AdaptiveEffort, sv.Mode
}

// saveSettings records the settings in the session's sidecar, creating the
// sidecar when the session has none.
func saveSettings(sessionsDir, id string, s Settings) error {
	saved := savedOf(s)

	return updateSidecar(sessionsDir, id, func(sc *Sidecar) bool {
		if sc.Settings != nil && *sc.Settings == saved {
			return false
		}
		sc.Settings = &saved

		return true
	})
}

// saveTools records a restricted tool policy in the session's sidecar.
func (s *Session) saveTools(p toolpolicy.Policy) {
	if !p.Restricted() {
		return
	}
	s.warnIf(updateSidecar(s.sessionsDir, s.id, func(sc *Sidecar) bool {
		if sc.Tools != nil && reflect.DeepEqual(*sc.Tools, p) {
			return false
		}
		sc.Tools = &p

		return true
	}))
}

// updateSidecar changes the session's sidecar with change, creating it when
// the session has none. It replaces the file whole, so a reader never sees
// half of it; change returns false when it changed nothing.
func updateSidecar(sessionsDir, id string, change func(*Sidecar) bool) error {
	sc, found, err := ReadSidecar(sessionsDir, id)
	if err != nil {
		return err
	}
	if !change(&sc) {
		return nil
	}
	if !found {
		sc.Created = time.Now().UTC()
	}
	data, err := json.Marshal(sc)
	if err != nil {
		return fmt.Errorf("failed to encode the session sidecar: %w", err)
	}
	if err := os.MkdirAll(sessionsDir, 0o700); err != nil {
		return fmt.Errorf("failed to create %s: %w", sessionsDir, err)
	}
	tmp, err := os.CreateTemp(sessionsDir, "."+id+".uah-*")
	if err != nil {
		return fmt.Errorf("failed to write the session sidecar: %w", err)
	}
	_, werr := tmp.Write(append(data, '\n'))
	if err := errors.Join(werr, tmp.Close()); err != nil {
		_ = os.Remove(tmp.Name())

		return fmt.Errorf("failed to write the session sidecar: %w", err)
	}
	if err := os.Rename(tmp.Name(), sidecarPath(sessionsDir, id)); err != nil {
		_ = os.Remove(tmp.Name())

		return fmt.Errorf("failed to write the session sidecar: %w", err)
	}

	return nil
}

// saveSettings keeps the session's settings in its sidecar, warning when
// it cannot. Sessions without a sessions directory keep nothing.
func (s *Session) saveSettings(settings Settings) {
	if s.sessionsDir == "" {
		return
	}
	if err := saveSettings(s.sessionsDir, s.id, settings); err != nil {
		s.emit(Notice{At: time.Now(), Level: LevelWarning, Message: err.Error()})
	}
}

// saveQueue keeps the unsent messages (queued, steers waiting for the run,
// waiting for hooks) in the sidecar when they change; the loop calls it.
func (s *Session) saveQueue() {
	var texts []string
	for _, in := range slices.Concat(s.queue, s.startSteers) {
		texts = append(texts, in.Text)
	}
	for _, p := range s.hooks.checking {
		texts = append(texts, p.input.Text)
	}
	if s.sessionsDir == "" || slices.Equal(texts, s.savedQueue) {
		return
	}
	s.savedQueue = texts
	s.warnIf(updateSidecar(s.sessionsDir, s.id, func(sc *Sidecar) bool {
		sc.Queued = texts

		return true
	}))
}

// restoreQueue queues again the messages the session kept when it closed
// (sc, read with err). They wait, as after an interrupt, for the next
// message or SteerQueued. Open calls it before the caller can read Events,
// so Open makes room for its events in the stream (queuedRoom).
func (s *Session) restoreQueue(sc Sidecar, err error) {
	s.warnIf(err)
	for _, text := range sc.Queued {
		in := core.UserInput{ID: uuid.NewString(), Text: text}
		s.queue = append(s.queue, in)
		s.emit(InputQueued{At: time.Now(), Input: in})
	}
	if s.savedQueue = sc.Queued; len(sc.Queued) > 0 {
		s.emit(Idle{At: time.Now()})
	}
}

// queuedRoom is the room restoreQueue needs in Events beyond eventBuffer:
// an InputQueued for each kept message and an Idle.
func queuedRoom(sc Sidecar) int {
	if len(sc.Queued) == 0 {
		return 0
	}

	return len(sc.Queued) + 1
}
