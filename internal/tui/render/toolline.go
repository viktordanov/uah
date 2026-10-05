package render

import (
	"cmp"
	"regexp"
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/viktordanov/uah/internal/cmdparse"
	"github.com/viktordanov/uah/internal/tui/state"
)

// A tool call in the compact view is one line in the column: its label,
// its time or state, and what it did (READ, LIST, and SEARCH with their
// targets, other commands without their wrappers). A second line, under
// the text, comes only when it says something: why the call failed (in
// the error color), an MCP call's result (in the comment color), and the
// auto-reviewer's approval (in the notices' gray). See
// docs/design/tool-calls.md.

// toolIndent is where a tool line's text starts: its indent and two fields.
const toolIndent = 2 + 2*labelWidth + 1

// compactToolLines draws a tool call and its second lines; a call joined
// to the one above it (a skill loaded after another) draws nothing.
func (st *Styles) compactToolLines(it state.Item, w int, now time.Time) []string {
	if it.MergedInto != "" {
		return nil
	}
	out := []string{st.compactTool(it, w, now)}
	for _, n := range st.toolNotes(it) {
		out = append(out, strings.Repeat(" ", toolIndent)+n.style.Render(ansi.Truncate(n.text, max(w-toolIndent, 8), "…")))
	}

	return out
}

// compactTool draws a tool call as a column: "  RAN    4.1s   go test ./...".
// A live call's label and time are in the accent; a finished one's dim,
// with fail and its exit code in the error color.
func (st *Styles) compactTool(it state.Item, w int, now time.Time) string {
	label := cmp.Or(it.Verb, toolLabel(it.Name))
	noMatches := noMatches(it)
	var head, tail string
	switch {
	case it.Tool == state.ToolCalled || it.Tool == state.ToolRunning:
		head = st.accent.Render("  " + pad(liveLabel(label)) + pad(elapsed(now.Sub(it.Started))) + " ")
	case it.Tool == state.ToolFailed && !noMatches:
		head = st.dim.Render("  "+pad(label)) + st.bad.Render(pad("fail")) + " "
		if t := failTail(it); t != "" {
			tail = st.bad.Render("  " + t)
		}
	case it.Tool == state.ToolStopped:
		head = st.dim.Render("  "+pad(label)) + st.warn.Render(pad("stop")) + " "
	default:
		when := ""
		if it.Duration >= time.Second {
			when = secs(it.Duration)
		}
		head = st.dim.Render("  " + pad(label) + pad(when) + " ")
		if noMatches {
			tail = st.dim.Render("  no matches")
		}
	}
	room := max(w-ansi.StringWidth(head)-ansi.StringWidth(tail), 8)

	return head + ansi.Truncate(st.parts(toolParts(it)), room, "…") + tail
}

// liveLabel is a running call's label: RUN for a command, which reads RAN
// once it is done.
func liveLabel(label string) string {
	if label == "RAN" {
		return "RUN"
	}

	return label
}

// toolParts are what a call's line says: its shaped parts, the names of
// the calls joined to it, or its label.
func toolParts(it state.Item) []cmdparse.Part {
	switch {
	case len(it.Group) > 1:
		var parts []cmdparse.Part
		for i, name := range it.Group {
			if i > 0 {
				parts = append(parts, cmdparse.Part{Text: ", ", Style: cmdparse.Dim})
			}
			parts = append(parts, cmdparse.Part{Text: name})
		}

		return parts
	case it.Parts != nil:
		return it.Parts
	}

	return []cmdparse.Part{{Text: it.Label}}
}

// parts draws styled parts on one line: plain text in the terminal's own
// color, the rest dim or in the comment color.
func (st *Styles) parts(parts []cmdparse.Part) string {
	var b strings.Builder
	for _, p := range parts {
		text := strings.ReplaceAll(untab(p.Text), "\n", " ")
		switch p.Style {
		case cmdparse.Dim:
			b.WriteString(st.dim.Render(text))
		case cmdparse.Faint:
			b.WriteString(st.comment.Render(text))
		case cmdparse.Plain:
			if p.Path == "" {
				b.WriteString(text)

				continue
			}
			from, to := lineRange(p.Lines)
			b.WriteString(st.linked(text, lipgloss.NewStyle(), st.fileLink(p.Path, from, to)))
		}
	}

	return b.String()
}

// lineRange reads a read's lines, "1-360" or "12" (0: none).
func lineRange(lines string) (from, to int) {
	a, b, _ := strings.Cut(lines, "-")
	from, _ = strconv.Atoi(a)
	to, _ = strconv.Atoi(b)

	return from, to
}

// noMatches is a search that found nothing: rg and grep exit 1 with no
// error, which is no failure.
func noMatches(it state.Item) bool {
	return it.Verb == cmdparse.LabelSearch && it.Tool == state.ToolFailed && it.Detail == "exit 1" && it.ErrorLine == ""
}

// statusCode is an HTTP status at the start of an error.
var statusCode = regexp.MustCompile(`^[1-5][0-9][0-9]\b`)

// failTail is what a failed call shows at its line's end: its exit code,
// an MCP error's status, or a short reason.
func failTail(it state.Item) string {
	switch {
	case strings.HasPrefix(it.Detail, "exit "):
		return it.Detail
	case statusCode.MatchString(it.ErrorLine):
		return statusCode.FindString(it.ErrorLine)
	case it.Detail != "failed" && it.Detail != "completed" && ansi.StringWidth(it.Detail) <= 12:
		return oneLine(it.Detail)
	}

	return ""
}

// toolNote is one of a call's second lines.
type toolNote struct {
	text  string
	style interface{ Render(...string) string }
}

// toolNotes are a call's second lines, in order: why it failed, an MCP
// result, the user's answers to the agent's questions, the auto-reviewer's
// approval.
func (st *Styles) toolNotes(it state.Item) []toolNote {
	var out []toolNote
	if it.Tool == state.ToolFailed && !noMatches(it) {
		why := it.ErrorLine
		if why == "" && failTail(it) == "" && it.Detail != "failed" {
			why = it.Detail // a reason too long for the line's end
		}
		if why != "" {
			out = append(out, toolNote{oneLine(why), st.bad})
		}
	}
	if it.Result != "" && it.Tool == state.ToolOK {
		out = append(out, toolNote{oneLine(it.Result), st.comment})
	}
	for _, a := range it.Answers {
		out = append(out, toolNote{a, st.comment})
	}
	if it.Note != "" {
		out = append(out, toolNote{oneLine(it.Note), st.notice})
	}

	return out
}
