package session

import (
	"errors"
	"io/fs"
	"path/filepath"
	"time"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/goal"
	"github.com/viktordanov/uah/internal/images"
	"github.com/viktordanov/uah/internal/sessionfile"
)

// FirstPromptMax is how many characters of the first message the sidecar
// keeps.
const FirstPromptMax = 200

// noteOpened fills in the sidecar's lookup fields when the session opens:
// its workspace and its last item, and for a resumed session from before
// uah kept them, its first message (Options.FirstPrompt).
func (s *Session) noteOpened(firstPrompt string) {
	s.noteLast(func(sc *Sidecar) bool {
		if sc.FirstPrompt != "" || firstPrompt == "" {
			return false
		}
		sc.FirstPrompt = cutPrompt(firstPrompt)

		return true
	})
}

// noteFirstPrompt records a new session's first message when its first run
// starts, without the injected messages that go before it.
func (s *Session) noteFirstPrompt(inputs []core.UserInput) {
	if !s.firstPromptPending || len(inputs) == 0 || s.sessionsDir == "" {
		return
	}
	s.firstPromptPending = false
	text := inputs[0].Text
	if goal.IsContext(text) && s.goal.g != nil {
		text = s.goal.g.Objective // a session that starts with /goal
	}
	text = cutPrompt(images.Display(text))
	s.warnIf(updateSidecar(s.sessionsDir, s.id, func(sc *Sidecar) bool {
		if sc.FirstPrompt != "" {
			return false
		}
		sc.FirstPrompt = text

		return true
	}))
}

// noteLast records the workspace, and the Sequence and time of the session
// file's last item, with any other change; it runs when the session opens
// and at the end of each run, so last_sequence never moves mid-turn.
func (s *Session) noteLast(other func(*Sidecar) bool) {
	if s.sessionsDir == "" {
		return
	}
	last, found, err := sessionfile.Last(filepath.Join(s.sessionsDir, s.id+".session.jsonl"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		s.warnIf(err)
	}
	s.warnIf(updateSidecar(s.sessionsDir, s.id, func(sc *Sidecar) bool {
		changed := other != nil && other(sc)
		if ws := s.settings.Workspace; ws != "" && sc.Workspace != ws {
			sc.Workspace, changed = ws, true
		}
		if found && (sc.LastSequence != last.Sequence || !sc.LastActivity.Equal(last.RecordedAt)) {
			sc.LastSequence, sc.LastActivity, changed = last.Sequence, last.RecordedAt, true
		}

		return changed
	}))
}

// warnIf shows a sidecar error as a warning; the session goes on.
func (s *Session) warnIf(err error) {
	if err != nil {
		s.emit(Notice{At: time.Now(), Level: LevelWarning, Message: err.Error()})
	}
}

// cutPrompt cuts a message to FirstPromptMax characters.
func cutPrompt(text string) string {
	if r := []rune(text); len(r) > FirstPromptMax {
		return string(r[:FirstPromptMax])
	}

	return text
}
