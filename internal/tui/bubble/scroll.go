package bubble

import (
	"github.com/viktordanov/uah/internal/tui/state"
)

// anchor reports the window to the state after each update while it is
// pinned (state/scroll.go): the renderer draws it from the anchor, so new
// output below it or a change above it does not move it, and the state
// learns the lines below it and whether output arrived. It lays out the
// frame the terminal draws next, whose items the cache then holds.
func (m Model) anchor() Model {
	st := m.st
	if v := m.st.View; v != nil {
		if len(m.st.Approvals) > 0 || len(m.st.Questions) > 0 {
			return m // the session's transcript shows: render/screen.go
		}
		st = *v.St
	}
	if m.st.Mode != state.ModeChat || m.st.Backtrack != nil || !st.Pinned() || m.w <= 0 || m.h <= 0 {
		return m
	}
	m.View()
	at, scroll := m.cache.Anchor()
	if at == st.Anchor && scroll == st.Scroll {
		return m
	}
	m.st, _ = state.Reduce(m.st, state.Anchored{At: at, Scroll: scroll, Width: m.w})

	return m
}
