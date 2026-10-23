package render

import (
	"cmp"
	"fmt"
	"math"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/viktordanov/uah/internal/images"
	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/internal/tui/state"
)

// Frame is what the shell provides for one screen.
type Frame struct {
	Width, Height int
	// Composer is the text input's rendered view and ComposerHeight its lines.
	Composer       string
	ComposerHeight int
	// Draft is the composer's text, for command completion.
	Draft string
	// Version is uah's version, for the banner.
	Version string
}

// Screen draws the whole screen and returns the row where the composer
// starts; a file peeked at is drawn over it (peek.go).
func Screen(s state.State, c *Cache, f Frame) (string, int) {
	out, row := screen(s, c, f)
	if s.Peek == nil || s.Mode == state.ModePicker || out == "" {
		return out, row
	}

	return strings.Join(c.styles.overlayPeek(strings.Split(out, "\n"), s, f.Width, f.Height), "\n"), row
}

func screen(s state.State, c *Cache, f Frame) (string, int) {
	if f.Width <= 0 || f.Height <= 0 {
		return "", 0
	}
	c.styles.setLinks(s)
	if len(s.Items) == 0 && len(c.entries) > 0 {
		c.entries = map[string]cacheEntry{} // /clear, /new, or a reload: the old lines go
	}
	if s.Mode == state.ModePicker {
		return c.styles.picker(s, f), -1
	}
	if s.View != nil && len(s.Approvals) == 0 && len(s.Questions) == 0 { // an approval or a question is the session's: it shows there
		return agentScreen(s, c, f)
	}
	var top []string
	if s.Details {
		top = append(top, c.styles.headerLine(s, f.Width))
	}
	panel := c.styles.panelLines(s, f)
	bottom := make([]string, 0, len(panel)+f.ComposerHeight+3)
	if !s.Details {
		bottom = append(bottom, c.styles.activeAgents(s, f.Width)...)
		if line := c.styles.statusLine(s, f.Width); line != "" {
			bottom = append(bottom, "", line)
		}
	}
	bottom = append(bottom, panel...)
	// The composer sits on the band, with a band row above and below.
	bottom = append(bottom, "", c.styles.band("", f.Width))
	composerTop := len(bottom)
	for l := range strings.SplitSeq(f.Composer, "\n") {
		bottom = append(bottom, c.styles.band(l, f.Width))
	}
	bottom = append(bottom, c.styles.band("", f.Width), c.styles.footerLine(s, f.Width))

	height := max(f.Height-len(top)-len(bottom), 1)
	var head []string
	if !s.Details && s.SessionID != "" {
		head = c.styles.banner(s, f.Version, f.Width)
	}
	body := transcript(s, c, f.Width, height, head)
	lines := make([]string, 0, f.Height)
	lines = append(lines, top...)
	lines = append(lines, body...)
	composerRow := len(lines) + composerTop
	lines = append(lines, bottom...)
	c.top = len(top)
	if excess := len(lines) - f.Height; excess > 0 {
		lines = lines[excess:]
		composerRow -= excess
		c.top -= excess
	}

	return strings.Join(lines, "\n"), composerRow
}

// transcript returns exactly height lines ending at the scroll position,
// rendering items from the bottom up and stopping once the window is full.
// head, when the whole transcript fits above the scroll position, comes
// first: the banner. Only the window's lines are copied; the items below
// it are counted.
func transcript(s state.State, c *Cache, w, height int, head []string) []string {
	if scroll, ok := backtrackScroll(s, c, w, height); ok {
		s.Scroll = scroll
	}
	d := c.gather(s, w, height)
	total := d.count // the transcript's lines: head's, then the items'
	c.maxScroll = -1
	if d.whole {
		if n := len(head); n > 0 && head[n-1] == "" && d.count > 0 && d.first == "" {
			head = head[:n-1] // one blank line between the banner and an item that starts with one
		}
		total += len(head)
		c.maxScroll = max(total-height, 0)
		if n := len(head); d.banner >= 0 && n > 0 {
			d.scroll = total - 1 - min(d.banner, n-1) // anchored in the banner
		}
	} else {
		head = nil
	}
	end := total - min(d.scroll, max(total-height, 0))
	start := max(end-height, 0)
	window, refs := c.windowLines(s, w, d, head, total, start, end)
	out := make([]string, 0, height)
	for range height - len(window) {
		out = append(out, "")
	}
	for _, l := range window {
		out = append(out, ansi.Truncate(l, w, ""))
	}
	switch {
	case s.Find != nil:
		c.styles.fade(out[height-len(window):], start, 0, 0)
	case s.Backtrack != nil:
		c.styles.fade(out[height-len(window):], start, total-d.under-d.selected, d.selected)
	}
	c.scrolled, c.bottom = total-end, state.TextPos{}
	if len(refs) > 0 {
		c.bottom = refs[len(refs)-1]
	}
	c.selectWindow(s, out, refs)
	c.markMatches(s, out)
	c.notes(s, out, w)

	return out
}

// gathered are the items a window draws, from the bottom up.
type gathered struct {
	rev  [][]string // the lines of the items in the window, bottom item first
	keys []string   // each of rev's items
	// count is the lines of the items walked, scroll how many of them lie
	// below the window, and below the lines of the walked items under rev's,
	// which are counted, not kept. whole says every item was walked, and
	// first is the top one's first line.
	count, scroll, below int
	whole                bool
	first                string
	// banner is the anchor's line when it is in the banner, else -1.
	banner int
	// under and selected place the message selected to go back to: the
	// lines under it and its own.
	under, selected int
}

// gather walks the items from the bottom up until the window is full,
// keeping the lines of the items in it. A pinned window ends at its anchor
// (state/scroll.go): the lines below the anchor are the scroll, however
// many arrived since.
func (c *Cache) gather(s state.State, w, height int) gathered {
	anchor := -2 // the anchor's item, or -2 for none
	if focusKey(s) == "" && s.Anchor.Key != "" && s.Pinned() {
		anchor = s.Order(s.Anchor.Key)
	}
	d := gathered{scroll: s.Scroll, under: -1, banner: -1}
	need := height + d.scroll
	if anchor == -1 {
		d.banner = s.Anchor.Line
	}
	if anchor >= -1 {
		need = math.MaxInt // until the anchor is found, the banner after every item
	}
	i := len(s.Items) - 1
	for ; i >= 0 && d.count < need; i-- {
		own, blank := c.spacedLines(&s, i, w)
		n := len(own)
		if blank {
			n++
		}
		if n == 0 {
			continue
		}
		if i <= anchor {
			// The anchor's line; an item the view does not draw anchors at
			// the end of the drawn item above it.
			line := n - 1
			if i == anchor {
				line = min(s.Anchor.Line, line)
			}
			d.scroll, anchor = d.count+n-1-line, -2
			need = d.scroll + height
		}
		if s.Backtrack != nil && s.Items[i].Key == s.Backtrack.Key {
			d.under, d.selected = d.count, n
		}
		d.first = ""
		if !blank {
			d.first = own[0]
		}
		if anchor >= -1 || d.count+n <= d.scroll {
			d.below += n // under the window, or under the anchor not found yet
		} else {
			d.rev, d.keys = append(d.rev, withBlank(own, blank)), append(d.keys, s.Items[i].Key)
		}
		d.count += n
	}
	d.whole = i < 0

	return d
}

// windowLines are the transcript's lines [start, end) and the line each one is,
// in a transcript of total lines that starts with head. They are the
// gathered items' lines, unless the window moved down from the scroll (a
// transcript shorter than the scroll, an anchor in the banner, or one not
// found): then the items are walked again for it.
func (c *Cache) windowLines(s state.State, w int, d gathered, head []string, total, start, end int) ([]string, []state.TextPos) {
	kept := linesIn(d.rev)
	if end > len(head) && (total-end < d.below || max(start, len(head)) < total-d.below-kept) {
		d.rev, d.keys, d.below = c.itemsIn(s, w, total-end, total-start)
		kept = linesIn(d.rev)
	}
	lines := make([]string, 0, end-start)
	refs := make([]state.TextPos, 0, end-start)
	for l := start; l < min(end, len(head)); l++ {
		lines = append(lines, head[l])
		refs = append(refs, state.TextPos{Key: state.BannerKey, Line: l})
	}
	at := total - d.below - kept // the transcript line rev's top item starts on
	for j, item := range slices.Backward(d.rev) {
		for l := max(start-at, 0); l < min(end-at, len(item)); l++ {
			lines = append(lines, item[l])
			refs = append(refs, state.TextPos{Key: d.keys[j], Line: l})
		}
		at += len(item)
	}

	return lines, refs
}

// itemsIn are the lines, bottom item first, and the keys of the items with
// lines in [from, to) counted from the transcript's bottom, and the lines
// of the items below them.
func (c *Cache) itemsIn(s state.State, w, from, to int) (rev [][]string, keys []string, below int) {
	count := 0
	for i := len(s.Items) - 1; i >= 0 && count < to; i-- {
		own, blank := c.spacedLines(&s, i, w)
		n := len(own)
		if blank {
			n++
		}
		switch {
		case n == 0:
		case count+n <= from:
			below += n
		default:
			rev, keys = append(rev, withBlank(own, blank)), append(keys, s.Items[i].Key)
		}
		count += n
	}

	return rev, keys, below
}

func linesIn(items [][]string) int {
	n := 0
	for _, lines := range items {
		n += len(lines)
	}

	return n
}

func (st *Styles) headerLine(s state.State, w int) string {
	full, short, _ := effortLabel(s)
	head := func(effort string) string {
		return fmt.Sprintf(" uah · %s · %s/%s · %s", session.ShortID(s.SessionID), s.Settings.Provider, s.Settings.Model, effort)
	}
	header := func(effort string) string {
		return head(effort) + fmt.Sprintf(" · %s · %s", cmp.Or(modeText(s), "sandbox none"), home(s.Home, s.Settings.Workspace))
	}
	left := header(full)
	var right string
	switch {
	case s.SessionID == "":
		right = "opening… "
	case s.Live != nil:
		right = fmt.Sprintf("● running %s ", clock(s.Now.Sub(s.Live.Started)))
	case s.Busy:
		right = "● starting "
	case s.Quitting:
		right = "stopping "
	default:
		right = "idle "
	}
	gap := w - ansi.StringWidth(left) - ansi.StringWidth(right)
	// The end is cut first; the live →low part goes only when the cut
	// would reach the effort.
	if gap < 1 && ansi.StringWidth(head(full)) >= w-ansi.StringWidth(right) {
		left = header(short)
		gap = w - ansi.StringWidth(left) - ansi.StringWidth(right)
	}
	if gap < 1 {
		left = ansi.Truncate(left, max(w-ansi.StringWidth(right)-1, 0), "…")
		gap = max(w-ansi.StringWidth(left)-ansi.StringWidth(right), 0)
	}

	return markYolo(left+strings.Repeat(" ", gap)+right, st.header, st.yoloChip)
}

// panelChrome is the screen's rows around the panel: the status line and
// the row above it, the composer's band rows and the row above them, the
// footer, and one row of the transcript.
const panelChrome = 7

// panelLines shows a pending approval, the agent's questions, the /config
// panel, the /model picker, the suggestion menu while typing a command or
// an "@" mention, or else the queue.
func (st *Styles) panelLines(s state.State, f Frame) []string {
	if a, ok := s.PendingApproval(); ok {
		return st.approvalLines(a, f.Width)
	}
	if q, ok := s.PendingQuestions(); ok {
		return st.questionLines(q, f.Width, f.Height-f.ComposerHeight-panelChrome)
	}
	if s.Config != nil {
		return st.configLines(s, f.Width)
	}
	if s.ModelPicker != nil {
		return st.modelPickerLines(s, f.Width)
	}
	if items := s.Suggestions(f.Draft); len(items) > 0 {
		var out []string
		for i, it := range items[:min(len(items), 6)] {
			label := fmt.Sprintf("%-16s", it.Label)
			line := "  " + st.bold.Render(label) + "  " + st.dim.Render(it.Help)
			if i == s.Menu.Index {
				line = st.selected.Render("› "+label) + "  " + st.dim.Render(it.Help)
			}
			out = append(out, ansi.Truncate(line, f.Width, "…"))
		}

		return out
	}
	if len(s.Queue) == 0 {
		return nil
	}
	var out []string
	if s.Details {
		out = append(out, st.dim.Render(ansi.Truncate("queued · sent when the run ends · enter on an empty prompt sends now · ↑ edits the last", f.Width, "…")))
	}
	for i, q := range s.Queue {
		if i == 3 {
			out = append(out, st.dim.Render(fmt.Sprintf("  … %d more", len(s.Queue)-3)))

			break
		}
		label, held := "queued", ""
		if q.AfterTool { // held for the tool call, not the run's end (enter)
			label, held = "after tool", "(after tool) "
		}
		if s.Details {
			out = append(out, ansi.Truncate(fmt.Sprintf("  %d. %s%s", i+1, held, oneLine(images.Display(q.Text))), f.Width, "…"))
		} else {
			out = append(out, st.dim.Render(ansi.Truncate("  ↳ "+label+": ", f.Width, ""))+ansi.Truncate(oneLine(images.Display(q.Text)), max(f.Width-len(label)-6, 8), "…"))
		}
	}
	if !s.Details {
		out = append(out, st.dim.Render("    enter sends now · ↑ edits"))
	}

	return out
}

// statusLine is the compact view's activity line above the composer: the
// breathing λ and what the run waits on, as Codex's "Working (12s • esc to
// interrupt)".
func (st *Styles) statusLine(s state.State, w int) string {
	wait, live := s.CurrentWait()
	switch {
	case s.Status != "":
		return st.warn.Render(ansi.Truncate(s.Status, w, "…"))
	case s.SessionID == "":
		return st.workingLine(s.Now, state.Wait{What: "Opening the session"}, time.Time{}, w)
	case live:
		return st.workingLine(s.Now, wait, s.Live.Started, w)
	case s.Busy:
		return st.workingLine(s.Now, state.Wait{What: "Starting"}, time.Time{}, w)
	}

	return ""
}

// compactHint is the compact footer's key hint: while the agent works, the
// send keys that work in this terminal, in place of the commands' hint;
// none while an approval or the agent's questions show their own.
func compactHint(s state.State) string {
	if panelShown(s) {
		return "" // the panel names its keys
	}
	if s.Working() {
		return s.Keys.SendHint(true) + " "
	}

	return "ctrl+t details · / commands "
}

func (st *Styles) footerLine(s state.State, w int) string {
	if s.History.Search != nil {
		return st.searchLine(s.History.Search, w)
	}
	if !s.Details {
		return st.compactFooter(s, w)
	}
	if s.Status != "" {
		return st.warn.Render(ansi.Truncate(" "+s.Status, w, "…"))
	}
	if wait, _ := s.CurrentWait(); wait.Warn {
		return st.warn.Render(ansi.Truncate(" "+wait.Text(), w, "…"))
	}
	t := s.Totals
	text := fmt.Sprintf(" %s in (%s cached) · %s out · %s · %s (∥%d)", tokens(t.Tokens.InputTokens), tokens(t.Tokens.CachedInputTokens),
		tokens(t.Tokens.OutputTokens), plural(t.Runs, "run"), plural(t.ToolCalls, "tool"), t.MaxParallel)
	if t.ToolBusy > 0 {
		text += fmt.Sprintf(" · overlap %d%%", int(100*t.Overlap/t.ToolBusy))
	}
	hint := s.Keys.SendHint(s.Working()) + " · / commands "
	if left, ok := s.ContextLeft(); ok {
		text += fmt.Sprintf(" · %d%% context left", left)
		hint = "/ commands "
	}
	if u, ok := s.UsageLeft(); ok {
		text += " · " + u
	}
	if g := s.GoalIndicator(); g != "" {
		text += " · " + g
	}
	if n := s.RunningShells(); n > 0 {
		text += " · " + plural(n, "shell") + " running"
	}
	if s.Shell {
		hint = shellHint
	}
	if panelShown(s) {
		hint = "" // the panel names its keys
	}
	if gap := w - ansi.StringWidth(text) - ansi.StringWidth(hint); gap > 0 {
		text += strings.Repeat(" ", gap) + hint
	}

	return st.dim.Render(ansi.Truncate(text, w, ""))
}

// compactFooter is the compact view's footer: the model and effort, the
// rest of the settings, and the hint on the right.
func (st *Styles) compactFooter(s state.State, w int) string {
	full, short, accent := effortLabel(s)
	model := " " + s.Settings.Model
	if s.Settings.Model != "" {
		model += " "
	}
	line := func(effort string) string {
		if model+effort == " " { // no settings yet
			return " " + strings.TrimPrefix(footerRest(s), " · ")
		}

		return model + effort + footerRest(s)
	}
	left := line(full)
	hint := compactHint(s)
	if s.Scroll > 0 {
		hint = "scrolled up · end returns "
	}
	if s.Shell {
		hint = shellHint
	}
	before := func(text string) { // text, then the hint
		if hint == "" {
			hint = text + " "
		} else {
			hint = text + " · " + hint
		}
	}
	if pct, ok := s.ContextLeft(); ok {
		before(fmt.Sprintf("%d%% context left", pct))
	}
	if u, ok := s.UsageLeft(); ok {
		before(u) // the plan's tightest window
	}
	if n := s.RunningShells(); n > 0 {
		before(plural(n, "shell") + " running · /ps") // stays on screen while they run
	}
	if g := s.GoalIndicator(); g != "" {
		before(g) // Codex's goal indicator: "Pursuing goal (12.5K / 50K)"
	}
	// The hint wins over the left side, which is cut when the line is
	// full: from the end, the live →low part only when the model and the
	// effort alone do not fit, since it shows only while a request is out.
	room := w - ansi.StringWidth(hint)
	if room > 0 {
		if ansi.StringWidth(model+full) > room-1 {
			full = short
			left = line(full)
		}
		left = ansi.Truncate(left, room-1, "…")
		left += strings.Repeat(" ", room-ansi.StringWidth(left)) + hint
	}
	text := ansi.Truncate(left, w, "")
	var spans []span
	if accent && strings.HasPrefix(text[min(len(model), len(text)):], full) { // not when cut
		spans = append(spans, span{from: len(model), to: len(model) + len(full), style: st.accent})
	}
	if i := strings.Index(text, yoloText); i >= 0 {
		spans = append(spans, span{from: i, to: i + len(yoloText), style: st.warn})
	}

	return paint(text, st.dim, spans...)
}

// footerRest is the compact footer after the effort: fast mode, the
// permission mode, the directory, and the queue.
func footerRest(s state.State) string {
	var b strings.Builder
	if s.Settings.ServiceTier != "" {
		b.WriteString(" · fast")
	}
	for _, p := range []string{modeText(s), home(s.Home, s.Settings.Workspace)} {
		if p != "" {
			b.WriteString(" · " + p)
		}
	}
	if len(s.Queue) > 0 {
		b.WriteString(" · " + plural(len(s.Queue), "queued message"))
	}

	return b.String()
}

// shellHint replaces the footer's hint in shell mode.
const shellHint = "! shell mode · enter runs the command · esc leaves "

func (st *Styles) picker(s state.State, f Frame) string {
	scope := "this directory · tab: all"
	if s.Picker.All {
		scope = "all directories · tab: this directory"
	}
	lines := []string{st.header.Render(ansi.Truncate(fmt.Sprintf(" Resume a session · %s · filter: %s▏", scope, s.Picker.Filter)+strings.Repeat(" ", f.Width), f.Width, ""))}
	list := s.Picker.Filtered()
	if len(list) == 0 {
		empty := "  no sessions match"
		if !s.Picker.All && s.Picker.Filter == "" {
			empty = "  no sessions in this directory · tab shows all · esc starts a new one"
		}
		lines = append(lines, "", st.dim.Render(ansi.Truncate(empty, f.Width, "…")))
	}
	room := f.Height - 3
	start := max(0, min(s.Picker.Selected-room/2, len(list)-room))
	for i := start; i < len(list) && i < start+room; i++ {
		in := list[i]
		row := fmt.Sprintf(" %s  %-9s %7s  %-11s %-12s ", session.ShortID(in.ID), age(s.Now, in.LastActivity), plural(in.Runs, "run"), in.Status, in.Model)
		if s.Picker.All {
			row += fmt.Sprintf("%-24s ", ansi.Truncate(home(s.Home, in.Workspace), 24, "…"))
		}
		row += oneLine(in.FirstPrompt)
		row = ansi.Truncate(row, f.Width, "…")
		if i == s.Picker.Selected {
			row = st.selected.Render(row + strings.Repeat(" ", max(f.Width-ansi.StringWidth(row), 0)))
		}
		lines = append(lines, row)
	}
	for len(lines) < f.Height-1 {
		lines = append(lines, "")
	}
	lines = append(lines, st.dim.Render(ansi.Truncate(" type to filter · ↑↓ choose · enter resume · tab this directory/all · esc back", f.Width, "…")))

	return strings.Join(lines, "\n")
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}

	return fmt.Sprintf("%d %ss", n, noun)
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// home shows path under the home directory h as ~: h itself, or a path
// below it, never a sibling that only starts with its name.
func home(h, path string) string {
	h = strings.TrimSuffix(h, string(filepath.Separator))
	switch {
	case h == "":
		return path
	case path == h:
		return "~"
	}
	if rest, ok := strings.CutPrefix(path, h+string(filepath.Separator)); ok {
		return "~" + string(filepath.Separator) + rest
	}

	return path
}

func age(now, t time.Time) string {
	d := now.Sub(t)
	switch {
	case t.IsZero():
		return "-"
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}

	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

// panelShown reports whether an approval or the agent's questions wait, in
// the panel that names its own keys.
func panelShown(s state.State) bool {
	_, approval := s.PendingApproval()
	_, questions := s.PendingQuestions()

	return approval || questions
}
