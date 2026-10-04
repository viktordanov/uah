package render

import (
	"strings"

	"github.com/viktordanov/uah/internal/tui/state"
)

// approvalLines is the approval above the composer, after Codex's, in the
// panel the agent's questions share: the question in the frame, in the
// warning color, the model's reason, the command, and the choices with
// their keys.
func (st *Styles) approvalLines(a state.Approval, w int) []string {
	title := "Run this command?"
	switch {
	case a.MCPTool != "":
		title = "Call this MCP tool?"
	case a.Escalation:
		title = "Run outside the sandbox?"
	}
	inner := max(w-4, panelMin)
	var body []string
	if a.Justification != "" {
		body = append(body, st.italic.Render("Reason: "+oneLine(a.Justification)))
	}
	for line := range strings.SplitSeq(strings.TrimRight(a.Command, "\n"), "\n") {
		body = append(body, st.bold.Render("$ "+line))
		if len(body) > 8 {
			body = append(body, st.dim.Render("…"))

			break
		}
	}
	if a.Answered {
		return st.panel(title, st.warn.Bold(true), body, "answering…", w)
	}
	keys, labels := []string{"y"}, []string{"Yes, proceed"}
	hint := "y yes"
	if len(a.Prefix) > 0 {
		keys, labels = append(keys, "s"), append(labels, "Yes, and don't ask again for commands that start with `"+strings.Join(a.Prefix, " ")+"`")
		hint += " · s always for the prefix"
	}
	if a.MCPTool != "" {
		// Codex's MCP prompt says "Allow and don't ask me again".
		keys, labels = append(keys, "a"), append(labels, "Yes, and don't ask again for this tool")
		hint += " · a always for the tool"
	}
	if a.GrantRoot != "" {
		keys, labels = append(keys, "w"), append(labels, "Yes, and allow writes to "+a.GrantRoot+" for this session")
		hint += " · w allow the directory"
	}
	keys, labels = append(keys, "n"), append(labels, "No, and tell the agent what to do differently")
	body = append(body, "")
	body = append(body, st.choiceRows(keys, labels, make([]string, len(keys)), -1, inner)...)

	return st.panel(title, st.warn.Bold(true), body, hint+" · n or esc no", w)
}
