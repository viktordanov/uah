package render

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// A panel is what waits for the user above the composer, an approval or
// the agent's questions, drawn as one family: a frame in the accent across
// the width, its title in the top edge, the body on the band, the choices
// as rows with their key, label, and help, and the keys in the bottom
// edge. The frame and the tint set it apart from the transcript.

// panelMin is the narrowest frame; a narrower screen gets the rows alone.
const panelMin = 16

// panel frames body under title, with hint in the bottom edge.
func (st *Styles) panel(title string, titleStyle lipgloss.Style, body []string, hint string, w int) []string {
	if w < panelMin {
		out := []string{titleStyle.Render(ansi.Truncate(title, w, "…"))}
		for _, l := range body {
			out = append(out, ansi.Truncate(l, w, "…"))
		}

		return out
	}
	inner := w - 4 // "│ " and " │"
	out := []string{st.edge("╭", "╮", titleStyle.Render(ansi.Truncate(title, inner-2, "…")), w)}
	for _, l := range body {
		out = append(out, st.tool.Render("│")+onBackground(st.bandOn, " "+ansi.Truncate(l, inner, "…")+" ", inner+2)+st.tool.Render("│"))
	}

	return append(out, st.edge("╰", "╯", st.dim.Render(ansi.Truncate(hint, inner-2, "…")), w))
}

// edge is the frame's top or bottom edge with text near its left end:
// "╭─ text ─────╮".
func (st *Styles) edge(left, right, text string, w int) string {
	if text == "" {
		return st.tool.Render(left + strings.Repeat("─", w-2) + right)
	}
	rest := max(w-5-ansi.StringWidth(text), 0)

	return st.tool.Render(left+"─ ") + text + st.tool.Render(" "+strings.Repeat("─", rest)+right)
}

// choiceWidth is the labels' column in choiceRows; the help starts four
// cells after it.
func choiceWidth(keys, labels, helps []string, w int) int {
	width, help := 0, false
	for i := range labels {
		width = max(width, ansi.StringWidth(keys[i]+" "+labels[i]))
		help = help || helps[i] != ""
	}
	if help {
		width = min(width, max(w/2, 12)) // a long label leaves its help some room
	}

	return min(width, max(w-2, 1))
}

// choiceRows draw a panel's choices: each row's key in the accent, its
// label, and its help dim, the labels padded to one column of at most half
// the width when there is help to show; the selected row (-1: none) in the
// accent band with "›".
func (st *Styles) choiceRows(keys, labels, helps []string, selected, w int) []string {
	width := choiceWidth(keys, labels, helps, w)
	out := make([]string, 0, len(labels))
	for i := range labels {
		label := ansi.Truncate(keys[i]+" "+labels[i], width, "…")
		label += strings.Repeat(" ", width-ansi.StringWidth(label))
		tail := ""
		if helps[i] != "" {
			tail = "  " + st.dim.Render(helps[i])
		}
		line := "  " + st.accent.Render(keys[i]) + strings.TrimPrefix(label, keys[i]) + tail
		if i == selected {
			line = st.selected.Render("› "+label) + tail
		}
		out = append(out, line)
	}

	return out
}
