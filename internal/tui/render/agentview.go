package render

import (
	"github.com/charmbracelet/x/ansi"

	"github.com/viktordanov/uah/internal/tui/state"
)

// agentScreen draws a subagent's transcript, as the session's own is
// drawn, under one header line. The agent's items have a cache of their
// own, since their keys are the agent's.
func agentScreen(s state.State, c *Cache, f Frame) (string, int) {
	v := s.View
	if c.view == nil || c.viewID != v.ID || c.viewGen != v.Gen {
		c.view, c.viewID, c.viewGen = &Cache{styles: c.styles, entries: map[string]cacheEntry{}, maxScroll: -1}, v.ID, v.Gen
	}
	f.Height = max(f.Height-1, 1)
	st := *v.St
	st.History = s.History // the composer and its ctrl+r search are the session's
	st.FileLinks, st.Host = s.FileLinks, s.Host
	out, row := screen(st, c.view, f)
	c.maxScroll, c.bottom, c.scrolled = c.view.maxScroll, c.view.bottom, c.view.scrolled
	c.pill, c.top = c.view.pill, c.view.top+1 // under the header line; the agent view takes no selection
	header := " " + c.styles.accent.Render("agent "+v.Nickname) + c.styles.dim.Render(" · alt+← alt+→ switch agents · esc esc interrupts")

	return ansi.Truncate(header, f.Width, "") + "\n" + out, row + 1
}
