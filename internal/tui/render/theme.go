// Package render draws TUI state as screen lines with lipgloss. It knows
// nothing about the terminal: the shell passes in the composer's view.
package render

import (
	"fmt"
	"image/color"
	"math"
	"regexp"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/alecthomas/chroma/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/viktordanov/uah/internal/contextusage"
	"github.com/viktordanov/uah/internal/tui/render/markdown"
)

// Theme is every color the TUI draws with. Text is left to the terminal,
// and so is the background: only your messages, the composer, code blocks,
// and every other table row sit on Band, so the terminal's own background
// shows everywhere else.
type Theme struct {
	Accent, Dim, Bad, Warn color.Color
	// Good marks what is ready or passed, such as an MCP server.
	Good color.Color
	// Notice is the neutral gray of information notices, such as an
	// auto-approved command, apart from the warm dim of tool lines.
	Notice color.Color
	// Info and Extra tell /context's categories apart.
	Info, Extra color.Color
	// Band is the background of your messages, the composer, code, and
	// table rows.
	Band color.Color
	// Breath runs from dim to bright: the working λ's frames.
	Breath []color.Color
	// Code colors code blocks: keywords, names, strings, numbers, comments.
	Keyword, Name, String, Number, Comment color.Color
	// DiffAdd and DiffDel tint added and removed diff lines across the
	// whole line, in the edit tool's diffs and in diff code blocks;
	// DiffAddWord and DiffDelWord mark the changed words in them.
	DiffAdd, DiffDel, DiffAddWord, DiffDelWord color.Color
	// Selection is the background of text selected with the mouse.
	Selection color.Color
}

// Amber is the default theme, for dark terminals: a saturated amber for
// the λ, live commands, and marks, on dim text that stays warm.
var Amber = Theme{
	Accent: hex("#ffc400"), Dim: hex("#a08c64"), Bad: hex("#ff5a3c"), Warn: hex("#ffc400"), Good: hex("#9be564"),
	Notice: hex("#8a8a8a"), Info: hex("#5cc8ff"), Extra: hex("#d49bff"),
	Band:    hex("#2a2a2a"),
	Breath:  []color.Color{hex("#5a4200"), hex("#806000"), hex("#a67c00"), hex("#cc9900"), hex("#e6b000"), hex("#ffc400"), hex("#ffe066")},
	Keyword: hex("#ffc400"), Name: hex("#ffd75e"), String: hex("#9be564"), Number: hex("#ff9f43"), Comment: hex("#8a8272"),
	// Codex's dark diff tints, and stronger ones for the changed words.
	DiffAdd: hex("#212922"), DiffDel: hex("#3c170f"), DiffAddWord: hex("#2f5a32"), DiffDelWord: hex("#6e2a18"),
	Selection: hex("#5c4608"),
}

// AmberLight is Amber for light terminals: deep amber ink.
var AmberLight = Theme{
	Accent: hex("#b86e00"), Dim: hex("#8f7b58"), Bad: hex("#d0301c"), Warn: hex("#b86e00"), Good: hex("#4f8a10"),
	Notice: hex("#707070"), Info: hex("#0a7bc2"), Extra: hex("#8a4fd6"),
	Band:    hex("#efe9dc"),
	Breath:  []color.Color{hex("#ecd9b0"), hex("#e0bf80"), hex("#d4a24c"), hex("#c88a22"), hex("#bd7a08"), hex("#b86e00"), hex("#8a4f00")},
	Keyword: hex("#b86e00"), Name: hex("#9a5c00"), String: hex("#4f8a10"), Number: hex("#c4501a"), Comment: hex("#9a917f"),
	// GitHub's light diff colors, as Codex uses on light terminals.
	DiffAdd: hex("#e6ffec"), DiffDel: hex("#ffebe9"), DiffAddWord: hex("#abf2bc"), DiffDelWord: hex("#ffc1c0"),
	Selection: hex("#f7d98b"),
}

// ThemeFor picks Amber or AmberLight for the terminal's background and
// derives the band from it, as Codex tints its message background.
func ThemeFor(bg color.Color) Theme {
	t := Amber
	if bg == nil {
		return t
	}
	r, g, b := rgb(bg)
	light := 0.299*float64(r)+0.587*float64(g)+0.114*float64(b) > 128
	if light {
		t = AmberLight
		t.Band = mix(bg, color.Black, 0.06)
	} else {
		t.Band = mix(bg, color.White, 0.07)
	}

	return t
}

// Styles are a theme's styles, which every drawing function is a method of.
// A render Cache holds the Styles its frames draw with, so each renderer
// has its own theme and nothing is shared between them.
type Styles struct {
	dim, bold, accent, bad, warn, italic, header, selected lipgloss.Style
	// notice is information notices' gray; comment is the code comments'
	// color, which an MCP call's result and a heredoc's body take.
	notice, comment lipgloss.Style
	// chip is a note drawn over the transcript (overlay.go): the accent on
	// the band.
	chip lipgloss.Style
	// yoloChip is yolo mode in the header: the warning color on the band,
	// since the header's background is the accent.
	yoloChip lipgloss.Style
	// tool is the accent without bold; ok is Good; codeSpan is `code`.
	tool, ok, codeSpan lipgloss.Style
	quoteBar           string
	// bandOn switches the band's background on; it is re-applied after every
	// reset inside a band line. addOn and delOn are the diff tints'.
	bandOn, addOn, delOn string
	// dimOn switches the dim foreground on, for lines faded while going
	// back to an earlier message.
	dimOn string
	// selectOn switches the selection's background on (selection.go).
	selectOn  string
	breath    []lipgloss.Style
	codeStyle *chroma.Style
	// categoryColors color /context's categories.
	categoryColors map[string]lipgloss.Style
	// diffStyles draw added and removed diff lines.
	diffStyles map[string]diffStyle
	// markdown draws agent messages and keeps their finished blocks.
	markdown *markdown.Renderer
}

// NewStyles builds a theme's styles.
func NewStyles(t Theme) *Styles {
	st := &Styles{}
	st.dim = lipgloss.NewStyle().Foreground(t.Dim)
	st.bold = lipgloss.NewStyle().Bold(true)
	st.accent = lipgloss.NewStyle().Foreground(t.Accent).Bold(true)
	st.bad = lipgloss.NewStyle().Foreground(t.Bad)
	st.notice = lipgloss.NewStyle().Foreground(t.Notice)
	st.comment = lipgloss.NewStyle().Foreground(t.Comment)
	st.warn = lipgloss.NewStyle().Foreground(t.Warn)
	st.italic = lipgloss.NewStyle().Foreground(t.Dim).Italic(true)
	st.header = lipgloss.NewStyle().Foreground(t.Band).Background(t.Accent)
	st.chip = lipgloss.NewStyle().Foreground(t.Accent).Background(t.Band)
	st.yoloChip = lipgloss.NewStyle().Foreground(t.Warn).Background(t.Band).Bold(true)
	st.selected = st.header
	st.tool = lipgloss.NewStyle().Foreground(t.Accent)
	st.ok = lipgloss.NewStyle().Foreground(t.Good)
	st.codeSpan = lipgloss.NewStyle().Foreground(t.Name)
	st.quoteBar = st.dim.Render("│ ")
	st.categoryColors = map[string]lipgloss.Style{
		contextusage.SystemPrompt: st.dim,
		contextusage.Instructions: lipgloss.NewStyle().Foreground(t.Name),
		contextusage.Skills:       lipgloss.NewStyle().Foreground(t.Good),
		contextusage.Tools:        lipgloss.NewStyle().Foreground(t.Accent),
		contextusage.MCPTools:     lipgloss.NewStyle().Foreground(t.Number),
		contextusage.UserMessages: lipgloss.NewStyle().Foreground(t.Info),
		contextusage.Assistant:    lipgloss.NewStyle().Foreground(t.Extra),
		contextusage.ToolResults:  lipgloss.NewStyle().Foreground(t.Bad),
	}
	st.diffStyles = newDiffStyles(t)
	st.bandOn, st.addOn, st.delOn = backgroundOn(t.Band), backgroundOn(t.DiffAdd), backgroundOn(t.DiffDel)
	r, g, b := rgb(t.Dim)
	st.dimOn = fmt.Sprintf("\x1b[38;2;%d;%d;%dm", r, g, b)
	r, g, b = rgb(t.Selection)
	st.selectOn = fmt.Sprintf("\x1b[48;2;%d;%d;%dm", r, g, b)
	st.breath = make([]lipgloss.Style, 0, len(t.Breath))
	for _, c := range t.Breath {
		st.breath = append(st.breath, lipgloss.NewStyle().Foreground(c).Bold(true))
	}
	st.codeStyle = chroma.MustNewStyle("uah", chroma.StyleEntries{
		chroma.Keyword:             "bold " + hexOf(t.Keyword),
		chroma.NameFunction:        hexOf(t.Name),
		chroma.NameClass:           hexOf(t.Name),
		chroma.LiteralString:       hexOf(t.String),
		chroma.LiteralNumber:       hexOf(t.Number),
		chroma.Comment:             "italic " + hexOf(t.Comment),
		chroma.GenericInserted:     hexOf(t.String),
		chroma.GenericDeleted:      hexOf(t.Bad),
		chroma.NameBuiltin:         hexOf(t.Name),
		chroma.KeywordType:         hexOf(t.Name),
		chroma.OperatorWord:        "bold " + hexOf(t.Keyword),
		chroma.LiteralStringEscape: hexOf(t.Number),
	})
	st.markdown = st.newMarkdown(t)

	return st
}

// band draws a line on the band background, padded to width w.
func (st *Styles) band(line string, w int) string {
	return onBackground(st.bandOn, line, w)
}

// onBackground draws a line on the background that on switches on, padded
// to width w. Any SGR that resets the background would end it early, so
// the background follows each one.
func onBackground(on, line string, w int) string {
	line = ansi.Truncate(untab(line), w, "")
	pad := strings.Repeat(" ", max(w-ansi.StringWidth(line), 0))
	line = sgr.ReplaceAllStringFunc(line, func(seq string) string {
		if resetsBackground(seq) {
			return seq + on
		}

		return seq
	})

	return on + line + pad + "\x1b[m"
}

// resetsBackground reports whether an SGR ends the background: a reset (0
// or no parameters) or 49. What follows 38, 48, or 58 is a color, so the 0
// in 48;2;255;196;0 is not a reset.
func resetsBackground(seq string) bool {
	params := strings.Split(seq[2:len(seq)-1], ";")
	for i := 0; i < len(params); i++ {
		switch params[i] {
		case "", "0", "49":
			return true
		case "38", "48", "58":
			i += extendedColorLen(params[i+1:])
		}
	}

	return false
}

// backgroundOn is the SGR that switches the background to c.
func backgroundOn(c color.Color) string {
	r, g, b := rgb(c)

	return fmt.Sprintf("\x1b[48;2;%d;%d;%dm", r, g, b)
}

// sgr matches one SGR escape sequence.
var sgr = regexp.MustCompile("\x1b\\[[0-9;]*m")

// breathing is the working λ: seven shades, dim to bright and back, one
// breath every 1.6 s, eased like a slow breath.
func (st *Styles) breathing(ms int64) string {
	const period = 1600
	t := float64(ms%period) / period
	i := int((1-cos2pi(t))/2*float64(len(st.breath)-1) + 0.5)

	return st.breath[i].Render("λ")
}

var spinner = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func hex(s string) color.Color { return lipgloss.Color(s) }

func rgb(c color.Color) (r, g, b uint8) {
	cr, cg, cb, _ := c.RGBA()

	return uint8(cr >> 8), uint8(cg >> 8), uint8(cb >> 8)
}

func hexOf(c color.Color) string {
	r, g, b := rgb(c)

	return fmt.Sprintf("#%02x%02x%02x", r, g, b)
}

// mix moves a toward b by f (0..1).
func mix(a, b color.Color, f float64) color.Color {
	ar, ag, ab := rgb(a)
	br, bg, bb := rgb(b)
	m := func(x, y uint8) uint8 { return uint8(float64(x) + (float64(y)-float64(x))*f + 0.5) }

	return color.RGBA{R: m(ar, br), G: m(ag, bg), B: m(ab, bb), A: 0xff}
}

func cos2pi(t float64) float64 { return math.Cos(2 * math.Pi * t) }

// Accent is the style of the λ prompt, for the shell's composer.
func (st *Styles) Accent() lipgloss.Style { return st.accent }

// Dim is the style of the composer's placeholder.
func (st *Styles) Dim() lipgloss.Style { return st.dim }
