package render

import (
	"image/color"

	"charm.land/lipgloss/v2"

	"github.com/viktordanov/uah/internal/citations"
	"github.com/viktordanov/uah/internal/tui/render/markdown"
)

// markdownLines renders the Markdown the model writes at width w, the way
// Codex draws it (package markdown). The first line starts with first, the
// others with rest (both already styled and of equal width). A text that
// grows, as a streaming answer does, re-renders only its last block. The
// model's web-search citation markers are left out (package citations).
func (st *Styles) markdownLines(text string, w int, first, rest string) []string {
	return st.markdown.Render(citations.Strip(text), w, first, rest)
}

// newMarkdown is the theme's Markdown renderer: headings and table headers
// in the accent, diff blocks on the edit tool's tints, and GitHub alerts'
// titles in the theme's colors.
func (st *Styles) newMarkdown(t Theme) *markdown.Renderer {
	title := func(c color.Color) markdown.Style { return lipgloss.NewStyle().Foreground(c).Bold(true) }

	return markdown.New(markdown.Styles{
		Bold:        st.bold,
		Italic:      lipgloss.NewStyle().Italic(true),
		Strike:      lipgloss.NewStyle().Strikethrough(true),
		Code:        st.codeSpan,
		Dim:         st.dim,
		H1:          st.accent,
		H2:          st.accent,
		Heading:     st.bold,
		TableHeader: st.accent,
		Band:        st.band,
		Added:       func(line string, w int) string { return onBackground(st.addOn, line, w) },
		Removed:     func(line string, w int) string { return onBackground(st.delOn, line, w) },
		Alerts: map[string]markdown.Style{
			"NOTE": title(t.Info), "TIP": title(t.Good), "IMPORTANT": title(t.Extra),
			"WARNING": title(t.Warn), "CAUTION": title(t.Bad),
		},
		CodeStyle: st.codeStyle,
	})
}
