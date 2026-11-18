package state

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/viktordanov/uah/internal/engine/codexauth"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/internal/usage"
)

// UsageReason says why the usage is read, which decides what the answer
// shows.
type UsageReason int

const (
	// UsageAfterRun refreshes the footer and the warnings after a run.
	UsageAfterRun UsageReason = iota
	// UsageStatus shows a row per window in /status.
	UsageStatus
	// UsageLimit says when to try again after a run hit the limit.
	UsageLimit
)

// usageLink is where ChatGPT shows the usage, as Codex's /status points.
const usageLink = "https://chatgpt.com/codex/settings/usage"

type (
	// EffLoadUsage reads the subscription's usage, from a snapshot no older
	// than MaxAge.
	EffLoadUsage struct {
		Reason UsageReason
		MaxAge time.Duration
	}
	// UsageLoaded is the usage read for Reason. Err wraps
	// usage.ErrUnsupported when the provider has none; with another error,
	// Snapshot is the last one read, if any.
	UsageLoaded struct {
		Reason   UsageReason
		Snapshot usage.Snapshot
		Err      error
		// At is when it was read, for reset times and staleness.
		At time.Time
	}
)

func (EffLoadUsage) effect() {}

// cmdUsage is /usage: the plan's usage, read fresh, as uah usage prints it,
// and the open session's prompt cache.
func cmdUsage(s *State, _ string) []Effect {
	return append([]Effect{EffLoadUsage{Reason: UsageStatus}}, s.loadCache()...)
}

// Usage is what the TUI knows of the subscription's usage.
type Usage struct {
	// Snapshot is the last one read; its CapturedAt is zero before any.
	Snapshot usage.Snapshot
	// Unavailable is set when the provider has no usage.
	Unavailable bool
	// warned holds, per window, the highest threshold already warned about.
	warned map[string]float64
	// limitRun is the run whose usage limit was already reported.
	limitRun string
}

// UsageLeft is the footer's tightest window, such as "weekly 78% left";
// ok is false without usage.
func (s State) UsageLeft() (string, bool) {
	if s.Usage.Unavailable || s.Usage.Snapshot.CapturedAt.IsZero() {
		return "", false
	}
	r, ok := s.Usage.Snapshot.Tightest()
	if !ok {
		return "", false
	}

	return r.Label + " " + r.Left(), true
}

// usageAfter asks for the usage after a session event: after each run, and
// when a run failed on the usage limit.
func (s *State) usageAfter(ev core.Event) []Effect {
	switch e := ev.(type) {
	case core.RunFinished:
		if s.Usage.Unavailable {
			return nil
		}

		return []Effect{EffLoadUsage{Reason: UsageAfterRun, MaxAge: usage.CacheFor}}
	case core.RunnerError:
		return s.limitReached(e.Message)
	case core.ModelResponded:
		return s.limitReached(e.Failure)
	}

	return nil
}

// limitReached reports the usage limit once per run: with the reset time
// when the failure kept it, else after reading the usage.
func (s *State) limitReached(text string) []Effect {
	r, ok := usage.LimitReachedIn(text)
	run := s.live().RunID
	if !ok || s.Usage.limitRun == run && run != "" {
		return nil
	}
	s.Usage.limitRun = run
	if !r.ResetsAt.IsZero() {
		s.notice(session.LevelError, "Usage limit reached; try again at "+usage.ResetLabel(r.ResetsAt, s.Now)+".")

		return nil
	}

	return []Effect{EffLoadUsage{Reason: UsageLimit}}
}

// onUsage handles UsageLoaded and reports whether ev was one.
func (s *State) onUsage(ev any) bool {
	e, ok := ev.(UsageLoaded)
	if !ok {
		return false
	}
	now := e.At
	if now.IsZero() {
		now = s.Now
	}
	if errors.Is(e.Err, usage.ErrUnsupported) {
		s.Usage.Unavailable = true
		if e.Reason == UsageStatus {
			s.notice(session.LevelInfo, "usage is not available for "+s.Settings.Provider)
		}

		return true
	}
	s.Usage.Unavailable = false
	if !e.Snapshot.CapturedAt.IsZero() {
		s.Usage.Snapshot = e.Snapshot
	}
	switch e.Reason {
	case UsageStatus:
		s.showUsage(e.Err, now)
		s.warnUsage(now, false)
	case UsageLimit:
		s.notice(session.LevelError, limitText(s.Usage.Snapshot, now))
	default:
		if e.Err != nil {
			s.notice(LevelDebug, "usage: "+e.Err.Error())

			return true
		}
		s.warnUsage(now, true)
	}

	return true
}

// limitText says when to try again, when the usage knows.
func limitText(snap usage.Snapshot, now time.Time) string {
	if at, ok := snap.BlockedUntil(); ok {
		return "Usage limit reached; try again at " + usage.ResetLabel(at, now) + "."
	}

	return "Usage limit reached; see " + usageLink + "."
}

// showUsage is /status's rows: a bar per window, "N% left", and the reset.
func (s *State) showUsage(err error, now time.Time) {
	snap := s.Usage.Snapshot
	if err != nil {
		s.notice(session.LevelWarning, "usage: "+err.Error())
	}
	if snap.CapturedAt.IsZero() {
		return
	}
	var b strings.Builder
	b.WriteString("usage")
	if snap.Plan != "" {
		b.WriteString(" · " + snap.Plan + " plan")
		if kind := codexauth.AccountKind(snap.Plan); kind != "" {
			b.WriteString(" · ChatGPT " + kind)
		}
	}
	if snap.Stale(now) {
		fmt.Fprintf(&b, " · stale, read %s ago", now.Sub(snap.CapturedAt).Round(time.Minute))
	}
	rows := snap.Rows()
	width := 0
	for _, r := range rows {
		width = max(width, len(r.Label))
	}
	for _, r := range rows {
		fmt.Fprintf(&b, "\n%-*s %s %s", width, r.Label, usage.Bar(r.Window.LeftPercent()), r.Left())
		if !r.Window.ResetsAt.IsZero() {
			b.WriteString(" (resets " + usage.ResetLabel(r.Window.ResetsAt, now) + ")")
		}
	}
	if len(rows) == 0 {
		b.WriteString("\nno usage windows")
	}
	s.notice(session.LevelInfo, b.String())
}

// warnUsage warns once per window per threshold crossed (usage.WarnAt);
// with show false it only records them. A window that falls back below a
// threshold, after its reset, warns again when it next crosses it.
func (s *State) warnUsage(now time.Time, show bool) {
	if s.Usage.warned == nil {
		s.Usage.warned = map[string]float64{}
	}
	for _, r := range s.Usage.Snapshot.Rows() {
		key := r.LimitID + "/" + r.Label
		crossed := r.Window.Crossed()
		if show && crossed > s.Usage.warned[key] {
			text := fmt.Sprintf("Heads up, you have less than %.0f%% of your %s limit left", 100-crossed, r.Label)
			if !r.Window.ResetsAt.IsZero() {
				text += " (resets " + usage.ResetLabel(r.Window.ResetsAt, now) + ")"
			}
			s.notice(session.LevelWarning, text)
		}
		s.Usage.warned[key] = crossed
	}
}
