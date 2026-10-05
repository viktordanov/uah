package state

// Scrolling back while output streams in, as Codex's transcript does: the
// window scrolled up stays on the text it shows, however much arrives
// below it or changes above it, and a pill over its last row says new
// output came ("New activity · ↓ Back to bottom") until it follows the
// bottom again. The renderer finds Anchor, the line on the window's bottom
// row, and draws the window from it; the shell reports the window it drew
// (Anchored), so Scroll stays the lines below it. A drag that selects
// text pins the window too, so the text under the mouse holds still.

// Anchored reports the window the last frame drew while pinned: At is the
// transcript line on its bottom row, Scroll the lines below it, and Width
// the window's width.
type Anchored struct {
	At     TextPos
	Scroll int
	Width  int
}

// layout is what a window's lines depend on besides the items: a resize,
// ctrl+t, or /reasoning draws the same items in more or fewer lines.
type layout struct {
	width              int
	details, reasoning bool
}

// Pinned reports whether the window holds its text in place: scrolled up,
// or while a drag selects text.
func (s State) Pinned() bool {
	return s.Scroll > 0 || (s.Selection != nil && s.Selection.Dragging && s.Selection.moved)
}

// anchored takes the window the shell drew. More lines below the same
// anchor, in the same layout, are new output.
func (s *State) anchored(e Anchored) {
	if !s.Pinned() {
		return
	}
	l := layout{width: e.Width, details: s.Details, reasoning: s.ShowReasoning}
	if s.Anchor.Key != "" && e.At == s.Anchor && l == s.anchorLayout && e.Scroll > s.Scroll {
		s.NewBelow = true
	}
	s.Anchor, s.Scroll, s.anchorLayout = e.At, e.Scroll, l
}

// follow drops the anchor and the new-output pill once the window follows
// the bottom again.
func (s *State) follow() {
	if !s.Pinned() {
		s.Anchor, s.NewBelow = TextPos{}, false
	}
}
