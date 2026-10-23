package render

import (
	"fmt"
	"math"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/tui/state"
)

const (
	// questionTextRows is how few rows a question's text is cut to when
	// the panel would not fit the screen.
	questionTextRows = 4
	// helpColumn is the narrowest column the chosen option's description
	// wraps in beside its label; a narrower one wraps it under the row.
	helpColumn = 24
	// previewRows is how many rows of a preview show; the rest is "… N
	// more lines".
	previewRows = 14
	// previewSideBySide is the narrowest panel body that shows a preview
	// beside the options; a narrower one shows it under them.
	previewSideBySide = 96
	// ownLabel is the last row: an answer in the user's own words.
	ownLabel = "Type your own answer"
	// noteMark marks an option with a note.
	noteMark = "✎"
)

// questionLines draw the agent's questions above the composer, after
// Codex's request_user_input overlay and Claude Code's AskUserQuestion, in
// the panel an approval shares: the shown question's header in the frame
// (and which of how many), the headers as tabs when there are several, the
// question, its options numbered with their descriptions and notes, the
// chosen one in the accent band, then "Type your own answer", and the
// keys. Options with previews show the chosen one's preview beside the
// list, or under it on a narrow screen. The question's text and the chosen
// option's description wrap, the others' descriptions take one row; when
// the panel would be taller than h rows, the longer of the two loses a row
// at a time, down to questionTextRows and one.
func (st *Styles) questionLines(q *state.Questions, w, h int) []string {
	question := q.Questions[q.Current]
	title := "? " + question.Header
	if n := len(q.Questions); n > 1 {
		title += fmt.Sprintf(" · %d of %d", q.Current+1, n)
	}
	inner := max(w-4, panelMin)
	text := wrapPrefixed(oneLine(question.Question), inner, "", "")
	lines, help, least := st.questionPanel(q, title, text, len(text), math.MaxInt, w)
	over := len(lines) - h
	if over <= 0 {
		return lines
	}
	// Each row either gives back, down to least for the description, is
	// one row less of the panel.
	ask := len(text)
	for ; over > 0 && (ask > questionTextRows || help > least); over-- {
		if help > least && help >= ask || ask <= questionTextRows {
			help--
		} else {
			ask--
		}
	}
	lines, _, _ = st.questionPanel(q, title, text, ask, help, w)

	return lines
}

// questionPanel is the panel with the question's text cut to ask rows and
// the chosen option's description to help, how many rows that one shows,
// and the fewest it can be cut to with each row cut making the panel
// shorter: one, or more beside a taller preview.
func (st *Styles) questionPanel(q *state.Questions, title string, text []string, ask, help, w int) ([]string, int, int) {
	question := q.Questions[q.Current]
	inner := max(w-4, panelMin)
	var body []string
	if len(q.Questions) > 1 {
		body = append(body, st.questionTabs(q))
	}
	body = append(body, styleLines(capRows(text, ask), st.bold)...)
	if q.Sent {
		return st.panel(title, st.accent, body, "sending the answers…", w), 0, 0
	}
	body = append(body, "")
	var rows []string
	var shown, least int
	switch {
	case !hasPreview(question):
		rows, shown = st.questionRows(q, inner, true, help)
		body = append(body, rows...)
		least = min(shown, 1)
	case inner < previewSideBySide:
		rows, shown = st.questionRows(q, inner, true, help)
		body = append(body, rows...)
		body = append(body, "")
		body = append(body, st.previewBox(q, inner)...)
		least = min(shown, 1)
	default:
		rows, shown, least = st.sideBySide(q, inner, help)
		body = append(body, rows...)
	}

	return st.panel(title, st.accent, body, questionHint(q), w), shown, least
}

// sideBySide puts the options, each with its help under it, on the left
// two fifths, and the chosen one's preview on the right; help and the
// ints are questionPanel's.
func (st *Styles) sideBySide(q *state.Questions, w, help int) ([]string, int, int) {
	left := min(max(w*2/5, 30), w-30)
	rows, shown := st.questionRows(q, left, false, help)
	box := st.previewBox(q, w-left-2)
	out := make([]string, 0, max(len(rows), len(box)))
	for i := range max(len(rows), len(box)) {
		line := ""
		if i < len(rows) {
			line = ansi.Truncate(rows[i], left, "…")
		}
		line += strings.Repeat(" ", left-ansi.StringWidth(line)) + "  "
		if i < len(box) {
			line += box[i]
		}
		out = append(out, line)
	}
	// Cut rows shorten the panel while the options are taller than the
	// preview.
	least := min(shown, max(shown-(len(rows)-len(box)), 1))

	return out, shown, least
}

// questionRows are the shown question's rows: each option, its help after
// it (inline) or under it, and its note under it, then "Type your own
// answer" with what the user typed there. The chosen row's help wraps, in
// its column or under the row, to at most help rows; the int is how many
// it takes.
func (st *Styles) questionRows(q *state.Questions, w int, inline bool, help int) ([]string, int) {
	question := q.Questions[q.Current]
	n := state.Rows(question)
	keys, labels, helps := make([]string, n), make([]string, n), make([]string, n)
	for i := range n {
		keys[i] = fmt.Sprintf("%d.", i+1)
		if i == len(question.Options) {
			labels[i], helps[i] = ownLabel, oneLine(q.Own[q.Current])

			continue
		}
		labels[i], helps[i] = question.Options[i].Label, oneLine(question.Options[i].Description)
		if q.Notes[q.Current][i] != "" || q.Noting == i {
			labels[i] += " " + noteMark
		}
	}
	under := make([]string, n) // help under the row instead of after it
	if !inline {
		copy(under, helps[:len(question.Options)])
		helps = make([]string, n)
		helps[n-1] = oneLine(q.Own[q.Current])
	}
	chosen, rows, shown := q.Choice[q.Current], max(help, 1), 0
	var more []string // the chosen row's help after its first row
	col := choiceWidth(keys, labels, helps, w) + 4
	if inline && chosen < n && w-col < helpColumn { // too narrow a column: under the row
		under[chosen], helps[chosen] = helps[chosen], ""
	}
	if inline && chosen < n && helps[chosen] != "" {
		wrapped := capRows(wrapPrefixed(helps[chosen], max(w-col, 1), "", ""), rows)
		helps[chosen], shown = wrapped[0], len(wrapped)
		for _, l := range wrapped[1:] {
			more = append(more, strings.Repeat(" ", col)+st.dim.Render(l))
		}
	}
	var out []string
	for i, row := range st.choiceRows(keys, labels, helps, chosen, w) {
		out = append(out, row)
		if i == chosen {
			out = append(out, more...)
		}
		switch {
		case under[i] == "":
		case i == chosen:
			wrapped := capRows(wrapPrefixed(under[i], w, "     ", "     "), rows)
			out, shown = append(out, styleLines(wrapped, st.dim)...), len(wrapped)
		default:
			out = append(out, st.dim.Render("     "+ansi.Truncate(under[i], max(w-5, 1), "…")))
		}
		out = append(out, st.noteLines(q, i, w)...)
	}

	return out, shown
}

// capRows keeps at most n of lines, the last kept one ending in "…" when
// any are dropped.
func capRows(lines []string, n int) []string {
	if len(lines) <= n {
		return lines
	}

	return append(lines[:n-1:n-1], lines[n-1]+"…")
}

// noteLines are an option's note under it, dim, or a hint while it is
// being written in the composer.
func (st *Styles) noteLines(q *state.Questions, row, w int) []string {
	if row >= len(q.Notes[q.Current]) {
		return nil
	}
	note := q.Notes[q.Current][row]
	switch {
	case q.Noting == row:
		note = "writing a note below…"
	case note == "":
		return nil
	}

	return []string{st.dim.Render(ansi.Truncate("     "+noteMark+" "+oneLine(note), w, "…"))}
}

// hasPreview reports whether any option of the question has a preview.
func hasPreview(q engine.Question) bool {
	for _, o := range q.Options {
		if o.Preview != "" {
			return true
		}
	}

	return false
}

// previewBox frames the chosen option's preview, w wide: its lines in the
// terminal's own color, cut to the width, and at most previewRows of them
// with "… N more lines" after.
func (st *Styles) previewBox(q *state.Questions, w int) []string {
	question := q.Questions[q.Current]
	row := q.Choice[q.Current]
	title, text := "Preview", ""
	if row < len(question.Options) {
		title, text = question.Options[row].Label, question.Options[row].Preview
	}
	var lines []string
	switch {
	case row == len(question.Options):
		lines = []string{st.dim.Render("Your own answer, typed below.")}
	case text == "":
		lines = []string{st.dim.Render("No preview for this option.")}
	default:
		all := strings.Split(strings.TrimRight(untab(text), "\n"), "\n")
		for i, l := range all {
			if i == previewRows {
				lines = append(lines, st.dim.Render(fmt.Sprintf("… %d more lines", len(all)-previewRows)))

				break
			}
			lines = append(lines, l)
		}
	}
	room := max(w-4, 1)
	label := ansi.Truncate(title, max(w-6, 1), "…")
	out := []string{st.dim.Render("┌ ") + st.bold.Render(label) + st.dim.Render(" "+strings.Repeat("─", max(w-4-ansi.StringWidth(label), 0))+"┐")}
	for _, l := range lines {
		l = ansi.Truncate(l, room, "…")
		out = append(out, st.dim.Render("│ ")+l+strings.Repeat(" ", room-ansi.StringWidth(l))+st.dim.Render(" │"))
	}

	return append(out, st.dim.Render("└"+strings.Repeat("─", max(w-2, 0))+"┘"))
}

// questionTabs are the headers: the shown one in the accent band, an
// answered one with ✓, the others dim.
func (st *Styles) questionTabs(q *state.Questions) string {
	tabs := make([]string, 0, len(q.Questions))
	for i, question := range q.Questions {
		switch {
		case i == q.Current:
			tabs = append(tabs, st.selected.Render(" "+question.Header+" "))
		case q.Done[i]:
			tabs = append(tabs, st.dim.Render("✓ "+question.Header))
		default:
			tabs = append(tabs, st.dim.Render("  "+question.Header))
		}
	}

	return strings.Join(tabs, " ")
}

// questionHint names the keys of what has focus: a note being written,
// the own-answer row, or the options. Enter answers the question, or sends
// the answers on the last one unanswered.
func questionHint(q *state.Questions) string {
	if q.Noting >= 0 {
		return "enter keep the note · esc drop it"
	}
	enter := "enter send"
	for i, done := range q.Done {
		if !done && i != q.Current {
			enter = "enter answer"

			break
		}
	}
	next := ""
	if len(q.Questions) > 1 {
		next = " · tab next"
	}
	if q.OwnRow() {
		return "type your answer · " + enter + " · ↑ back" + next + " · esc interrupt"
	}

	return fmt.Sprintf("↑↓ choose · 1-%d pick · n note · %s%s · esc interrupt", min(state.Rows(q.Questions[q.Current]), 9), enter, next)
}
