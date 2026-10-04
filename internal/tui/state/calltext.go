package state

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"

	"github.com/viktordanov/uah/internal/cmdparse"
	"github.com/viktordanov/uah/internal/mcp"
)

// mcpParts is an MCP call's line: "server · tool  key "value", key 5",
// from its qualified name (mcp__server__tool) and its JSON arguments in
// the order the model sent them. A string with no space shows bare.
// A resource tool's line is "server · list_mcp_resources  ...", with the
// server from its arguments, else "mcp".
func mcpParts(name, arguments string) []cmdparse.Part {
	rest := strings.TrimPrefix(name, "mcp__")
	server, tool, ok := strings.Cut(rest, "__")
	if !ok {
		server, tool = "mcp", rest
	}
	if mcp.IsResourceTool(name) {
		var args struct {
			Server string `json:"server"`
		}
		_ = json.Unmarshal([]byte(arguments), &args)
		server, tool = cmp.Or(strings.TrimSpace(args.Server), "mcp"), name
	}
	parts := []cmdparse.Part{plain(server), dim(" · "), plain(tool)}
	for i, kv := range orderedArgs(arguments) {
		sep := ", "
		if i == 0 {
			sep = "  "
		}
		parts = append(parts, dim(sep+kv[0]+" "), plain(kv[1]))
	}

	return parts
}

func plain(s string) cmdparse.Part { return cmdparse.Part{Text: s, Style: cmdparse.Plain} }
func dim(s string) cmdparse.Part   { return cmdparse.Part{Text: s, Style: cmdparse.Dim} }

// orderedArgs are a JSON object's keys and values as shown, in order.
func orderedArgs(arguments string) [][2]string {
	dec := json.NewDecoder(strings.NewReader(arguments))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil
	}
	var out [][2]string
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return out
		}
		var raw json.RawMessage
		if dec.Decode(&raw) != nil {
			return out
		}
		out = append(out, [2]string{fmt.Sprint(key), argValue(raw)})
	}

	return out
}

// argValue shows a value: a string bare unless it has a space or is
// empty, anything else as compact JSON.
func argValue(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if s == "" || strings.ContainsFunc(s, unicode.IsSpace) {
			return strconv.Quote(s)
		}

		return s
	}
	var b bytes.Buffer
	if json.Compact(&b, raw) != nil {
		return string(raw)
	}

	return b.String()
}

// resultSummary sums up an MCP call's result on one line: the keys of a
// JSON object in order ("{a, b, …}" when the kept text is cut short), the
// length of a JSON array, else the first line of text; and the size when
// it is over a kilobyte.
func resultSummary(result string, size int) string {
	text := strings.TrimSpace(result)
	summary := firstLine(text)
	var items []json.RawMessage
	switch {
	case strings.HasPrefix(text, "[") && json.Unmarshal([]byte(text), &items) == nil:
		summary = fmt.Sprintf("%d items", len(items))
		if len(items) == 1 {
			summary = "1 item"
		}
	case strings.HasPrefix(text, "{"):
		if keys := topKeys(text); len(keys) > 0 {
			more := ""
			if !json.Valid([]byte(text)) {
				more = ", …"
			}
			summary = "{" + strings.Join(keys, ", ") + more + "}"
		}
	}
	if size >= 1024 {
		summary += fmt.Sprintf(" · %.1f KB", float64(size)/1024)
	}

	return strings.TrimPrefix(summary, " · ")
}

// topKeys are the top-level keys of a JSON object cut short.
func topKeys(text string) []string {
	dec := json.NewDecoder(strings.NewReader(text))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil
	}
	var keys []string
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return keys
		}
		keys = append(keys, fmt.Sprint(key))
		var skip json.RawMessage
		if dec.Decode(&skip) != nil {
			return keys
		}
	}

	return keys
}

// lastLine is the last line of output that says something: it has a
// letter or a digit, once ANSI codes and spaces are gone.
func lastLine(text string) string {
	lines := strings.Split(ansi.Strip(text), "\n")
	for _, l := range slices.Backward(lines) {
		if l = strings.TrimSpace(l); strings.ContainsFunc(l, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) {
			return l
		}
	}

	return ""
}

// firstLine is the first line that says something.
func firstLine(text string) string {
	for l := range strings.SplitSeq(ansi.Strip(text), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}

	return ""
}

// isMCP reports whether a tool is an MCP server's tool or a resource
// tool: both run as MCP calls and show their result.
func isMCP(name string) bool { return strings.HasPrefix(name, mcp.Prefix) || mcp.IsResourceTool(name) }
