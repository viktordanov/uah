package bubble

import (
	"github.com/viktordanov/uah/internal/gitdiff"
	"github.com/viktordanov/uah/internal/tui/state"
	"github.com/viktordanov/uah/internal/tui/term"
)

// runReview runs /diff's and /review's effects: git reads off the update
// loop, and the review through the session, whose events report it.
func (m Model) runReview(e state.Effect) (term.Cmd, bool) {
	ctx, sess := m.ctx, m.sess
	switch e := e.(type) {
	case state.EffDiff:
		return func() term.Msg {
			d, err := gitdiff.Collect(ctx, e.Dir)

			return state.DiffShown{Diff: d, Err: err}
		}, true
	case state.EffLoadReviewTargets:
		return func() term.Msg {
			branches, err := gitdiff.ListBranches(ctx, e.Dir)
			if err != nil {
				return state.ReviewTargetsLoaded{Err: err}
			}
			commits, err := gitdiff.ListCommits(ctx, e.Dir, gitdiff.RecentCommits)

			return state.ReviewTargetsLoaded{Branches: branches, Commits: commits, Err: err}
		}, true
	case state.EffReview:
		return func() term.Msg {
			if sess == nil {
				return state.Failed{Err: errNoSession}
			}
			if err := sess.Review(ctx, e.Target); err != nil {
				return state.Failed{Err: err}
			}

			return nil
		}, true
	}

	return nil, false
}
