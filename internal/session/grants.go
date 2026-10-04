package session

import (
	"slices"
	"time"

	"github.com/viktordanov/uah/internal/sandbox"
)

// evGrant is a directory the session's grants just made writable, from
// any of its runs or its subagents' runs (sandbox.Grants).
type evGrant struct{ grant sandbox.Grant }

// openGrants gives the session its grants: a subagent shares its parent's
// (shared non-nil), and a root session makes its own, which its sidecar
// keeps. A resumed root session gets back the grants of its sidecar that
// still hold (sandbox.Grants.Valid), with a notice for each kept or
// dropped one; Open holds room in Events for them (len(sc.Grants)).
func (s *Session) openGrants(shared *sandbox.Grants, workspace string, sc Sidecar) {
	if shared != nil {
		s.grants = shared

		return
	}
	s.ownGrants = true
	s.grants = sandbox.NewGrants(workspace, func(g sandbox.Grant) { s.post(evGrant{grant: g}) })
	var kept []sandbox.Grant
	for _, g := range sc.Grants {
		if s.grants.Keep(g) {
			kept = append(kept, g)
			s.out <- Notice{At: time.Now(), Level: LevelInfo, Message: grantLine(g) + ", kept from before the resume"}
		} else {
			s.out <- Notice{At: time.Now(), Level: LevelWarning, Message: droppedLine(g)}
		}
	}
	if s.sessionsDir != "" && len(kept) != len(sc.Grants) {
		s.warnIf(updateSidecar(s.sessionsDir, s.id, func(c *Sidecar) bool {
			c.Grants = kept

			return true
		}))
	}
}

// onGrant keeps a new grant in the sidecar, when the session owns its
// grants, and shows it.
func (s *Session) onGrant(g sandbox.Grant) {
	if s.ownGrants && s.sessionsDir != "" {
		s.warnIf(updateSidecar(s.sessionsDir, s.id, func(c *Sidecar) bool {
			if slices.Contains(c.Grants, g) {
				return false
			}
			c.Grants = append(c.Grants, g)

			return true
		}))
	}
	s.emit(Notice{At: time.Now(), Level: LevelInfo, Message: grantLine(g)})
}

// grantLine is the transcript's line for a grant.
func grantLine(g sandbox.Grant) string {
	return "writable for this session: " + g.Path + " (" + reasonText(g.Reason) + ")"
}

// droppedLine is the notice for a grant a resume did not keep.
func droppedLine(g sandbox.Grant) string {
	if g.Reason == sandbox.GrantWorktree {
		return "no longer writable for this session: " + g.Path + " is not a git worktree of this repository now"
	}

	return "no longer writable for this session: " + g.Path + ", which you allowed, is gone, moved, or not allowed now"
}

func reasonText(r sandbox.GrantReason) string {
	switch r {
	case sandbox.GrantWorktree:
		return "a git worktree of this repository"
	case sandbox.GrantApproved:
		return "your approval"
	}

	return string(r)
}
