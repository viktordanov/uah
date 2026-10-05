package render

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/internal/tui/state"
)

// Cache keeps the rendered lines of finished items per width.
type Cache struct {
	// styles are the theme's styles the frames draw with.
	styles  *Styles
	entries map[string]cacheEntry
	// maxScroll is how far the last frame could scroll up, or -1 when the
	// frame did not reach the first item (the limit is not known yet).
	maxScroll int
	// view caches the items of the agent view, viewID's.
	view    *Cache
	viewID  string
	viewGen int
	// rows are the transcript lines the last frame's window showed, one
	// per row from screen row top, with window its lines (selection.go).
	rows   []state.TextPos
	window []string
	top    int
	// bottom is the transcript line on the window's bottom row and scrolled
	// the lines below it, which the shell reports back while the window is
	// pinned (Anchor); pill is where the new-output pill was drawn
	// (overlay.go).
	bottom   state.TextPos
	scrolled int
	pill     cells
}

// MaxScroll is how far the last frame's transcript could scroll up, or -1
// when unknown.
func (c *Cache) MaxScroll() int { return c.maxScroll }

type cacheEntry struct {
	version, width, links int
	reasoning, details    bool
	lines                 []string
}

// NewCache is a render cache that draws with the theme's styles. A new
// theme means a new cache, since cached lines carry the old colors.
func NewCache(t Theme) *Cache {
	return &Cache{styles: NewStyles(t), entries: map[string]cacheEntry{}, maxScroll: -1}
}

// Styles are the styles the cache's frames draw with.
func (c *Cache) Styles() *Styles { return c.styles }

// view is how items are drawn: the compact default or the detailed view.
type view struct {
	reasoning, details bool
}

// lines returns an item's lines, from the cache when the item is not live.
func (c *Cache) lines(it state.Item, width int, now time.Time, v view) []string {
	if it.Live() {
		return c.styles.itemLines(it, width, now, v)
	}
	if e, ok := c.entries[it.Key]; ok && e.version == it.Version && e.width == width && e.reasoning == v.reasoning && e.details == v.details && e.links == c.styles.linkGen {
		return e.lines
	}
	lines := c.styles.itemLines(it, width, now, v)
	c.entries[it.Key] = cacheEntry{version: it.Version, width: width, reasoning: v.reasoning, details: v.details, links: c.styles.linkGen, lines: lines}

	return lines
}

func (st *Styles) itemLines(it state.Item, w int, now time.Time, v view) []string { //nolint:gocyclo // a dispatch switch over a closed set; see docs/documentation/architecture.md
	if !v.details {
		if lines, ok := st.compactLines(it, w, now); ok {
			return lines
		}
	}
	reasoning := v.reasoning
	switch it.Kind {
	case state.KindUser:
		suffix := ""
		switch it.Input {
		case state.InputSent:
			suffix = st.dim.Render("  sending…")
		case state.InputFailed:
			suffix = st.bad.Render("  not delivered")
		case state.InputQueued, state.InputDelivered:
		}

		return st.userLines(it.Text, suffix, w)
	case state.KindRun:
		return []string{st.dim.Render(runRule(it, w, now))}
	case state.KindTurn:
		if it.Pending {
			return []string{st.dim.Render(fmt.Sprintf("  turn %d  ", it.Turn)) + st.tool.Render(spin(now)) + st.dim.Render(" thinking "+clock(now.Sub(it.Started)))}
		}

		return []string{st.dim.Render(fmt.Sprintf("  turn %d  %s in · %s out · %s", it.Turn, tokens(it.In), tokens(it.Out), secs(it.Duration)))}
	case state.KindTool:
		if len(it.Diff) > 0 {
			return append([]string{st.toolLine(it, w, now), diffIndent + st.diffSummary(it.Diff)}, st.diffBlock(it.Diff, w, 0)...)
		}

		return append([]string{st.toolLine(it, w, now)}, st.detailedNotes(it, w)...)
	case state.KindAgent:
		lines := st.agentLines(it, w, now)
		if v.details {
			for _, sub := range it.Sub {
				lines = append(lines, "    "+st.toolLine(sub, w-4, now))
			}
		}

		return lines
	case state.KindFinish:
		return nil // the run divider has it
	case state.KindContext:
		return st.contextLines(it.Context, w)
	case state.KindMCP:
		return st.mcpLines(it, w, v.details)
	case state.KindShell:
		return st.shellLines(it, w, now, v.details)
	case state.KindDiff:
		return st.gitDiffLines(it.GitDiff, w)
	case state.KindReview:
		return st.reviewLines(it, w, now, v.details)
	case state.KindAssistant:
		if it.Final {
			return append([]string{"", st.accent.Render("● answer")}, st.linkWords(st.markdownLines(it.Text, w, "  ", "  "), it.Links)...)
		}

		return st.linkWords(st.markdownLines(it.Text, w, st.dim.Render("  · "), "    "), it.Links)
	case state.KindReasoning:
		if !reasoning {
			return nil
		}

		return styleLines(wrapPrefixed(it.Text, w, "  ~ ", "    "), st.italic)
	case state.KindNotice:
		// Information is plain gray text; only warnings and errors get a mark.
		style, mark, rest := st.notice, "  ", "  "
		switch it.Level {
		case session.LevelWarning:
			style, mark, rest = st.warn, "  ! ", "    "
		case session.LevelError:
			style, mark, rest = st.bad, "  ✗ ", "    "
		}
		var out []string
		i := 0
		for part := range strings.SplitSeq(it.Text, "\n") {
			prefix := rest
			if i == 0 {
				prefix = mark
			}
			out = append(out, styleLines(wrapPrefixed(part, w, prefix, rest), style)...)
			i++
		}

		return out
	}

	return nil
}

func runRule(it state.Item, w int, now time.Time) string {
	var text string
	if it.Status == core.StatusRunning {
		text = fmt.Sprintf("── run %s · running %s ", it.RunID, clock(now.Sub(it.Started)))
	} else {
		text = fmt.Sprintf("── run %s · %s · %s · %s tokens ", it.RunID, it.Status, secs(it.Wall), tokens(it.Tokens))
	}
	if pad := w - ansi.StringWidth(text); pad > 0 {
		text += strings.Repeat("─", pad)
	}

	return ansi.Truncate(text, w, "")
}

func (st *Styles) toolLine(it state.Item, w int, now time.Time) string {
	var mark, detail string
	switch it.Tool {
	case state.ToolCalled:
		mark, detail = st.tool.Render(spin(now)), st.dim.Render("starting")
	case state.ToolRunning:
		mark, detail = st.tool.Render(spin(now)), st.dim.Render("running "+clock(now.Sub(it.Started)))
	case state.ToolOK:
		mark, detail = st.ok.Render("✓"), st.dim.Render(it.Detail+" · "+secs(it.Duration))
	case state.ToolFailed:
		mark, detail = st.bad.Render("✗"), st.bad.Render(it.Detail)+st.dim.Render(" · "+secs(it.Duration))
	case state.ToolStopped:
		mark, detail = st.warn.Render("■"), st.warn.Render("stopped")
	}
	head := fmt.Sprintf("  %s %s  ", mark, st.tool.Render(it.Name))
	room := w - ansi.StringWidth(head) - ansi.StringWidth(detail) - 2
	label := it.Label
	if it.Command != "" {
		label = oneLine(untab(it.Command)) // whole, where the runner's label stops at 120 characters
	}

	return head + ansi.Truncate(label, max(room, 8), "…") + "  " + detail
}

// detailedNotes are a call's second lines in the detailed view: as in the
// compact view, under the call's text, but whole, wrapped to the width.
func (st *Styles) detailedNotes(it state.Item, w int) []string {
	indent := strings.Repeat(" ", 2+1+1+ansi.StringWidth(it.Name)+2) // "  ✓ Bash  "
	var out []string
	for _, n := range st.toolNotes(it) {
		out = append(out, styleLines(wrapPrefixed(n.text, w, indent, indent), n.style)...)
	}

	return out
}

// wrapPrefixed wraps text to width w with first and continuation prefixes.
func wrapPrefixed(text string, w int, first, rest string) []string {
	text = untab(text)
	width := max(w-ansi.StringWidth(first), 10)
	var out []string
	for para := range strings.SplitSeq(strings.TrimRight(text, "\n"), "\n") {
		for line := range strings.SplitSeq(ansi.Wrap(para, width, ""), "\n") {
			prefix := rest
			if len(out) == 0 {
				prefix = first
			}
			out = append(out, prefix+line)
		}
	}
	if len(out) == 0 {
		out = []string{first}
	}

	return out
}

func styleLines(lines []string, style interface{ Render(...string) string }) []string {
	for i, l := range lines {
		lines[i] = style.Render(l)
	}

	return lines
}

func spin(now time.Time) string { return spinner[(now.UnixMilli()/100)%int64(len(spinner))] }

func clock(d time.Duration) string {
	d = max(d, 0).Round(time.Second)
	if d >= time.Hour {
		return fmt.Sprintf("%d:%02d:%02d", int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60)
	}

	return fmt.Sprintf("%d:%02d", int(d.Minutes()), int(d.Seconds())%60)
}

func secs(d time.Duration) string {
	if d >= 2*time.Minute {
		return clock(d)
	}

	return fmt.Sprintf("%.1fs", d.Seconds())
}

// tokens formats a count as 834, 12.4k, or 1.2M.
func tokens(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	}

	return strconv.FormatInt(n, 10)
}
