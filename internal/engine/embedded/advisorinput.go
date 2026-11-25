package embedded

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/viktordanov/uah-core/harness/llm"

	"github.com/viktordanov/uah/internal/compaction"
	"github.com/viktordanov/uah/internal/engine"
)

// A command backend reads the transcript on stdin, as text or as JSON
// ([advisor] input). Both are built from the same entries: the history
// with the session's web searches put back next to their anchors (as the
// transport does for a turn request, searchlog.go), without what a
// command cannot read (encrypted reasoning, Codex's configuration
// updates), with a note in place of a remote compaction's placeholder,
// and cut to max_input_tokens by dropping the oldest entries after the
// first user message, a call always with its results.

// advisorEntry is one entry of a command's transcript: an item of the
// history, a web search (the web_search_call item as recorded), or a note
// of uah's own.
type advisorEntry struct {
	item   llm.Item
	search json.RawMessage
	note   string
	// call is the ID of the call a call or result belongs to, which keeps
	// them together when entries are dropped ("": neither); custom marks
	// a custom tool's call and result.
	call   string
	custom bool
}

// advisorCompactedNote stands in a command's transcript for a remote
// compaction's item, which is encrypted.
const advisorCompactedNote = "The earlier conversation was compacted by the provider into an encrypted item that cannot be shown here. " +
	"The user messages before this note are the ones the compaction kept."

// advisorEntries are the transcript's entries for a command: its
// messages, calls, and results, with searches put back by their anchors'
// IDs; reasoning, configuration updates, and any other item are left out,
// and a remote compaction's placeholder becomes a note.
func advisorEntries(t advisorTranscript, searches []searchRecord) []advisorEntry {
	before, after := searchAnchors(t.items, searches)
	custom := map[string]bool{}
	for _, item := range t.items {
		if c, ok := item.Data.(llm.ToolCall); ok && c.Custom {
			custom[c.CallID] = true
		}
	}
	var out []advisorEntry
	addSearches := func(items []json.RawMessage) {
		for _, s := range items {
			out = append(out, advisorEntry{search: s})
		}
	}
	for _, item := range t.items {
		if id := item.ProviderID; id != "" {
			addSearches(before[id])
		}
		switch d := item.Data.(type) {
		case llm.ToolCall:
			out = append(out, advisorEntry{item: item, call: d.CallID, custom: d.Custom})
		case llm.ToolResult:
			out = append(out, advisorEntry{item: item, call: d.CallID, custom: custom[d.CallID]})
		case llm.Message:
			if compaction.IsRemotePlaceholder(item) && (t.remote.marker == "" || strings.HasPrefix(d.Text, "["+t.remote.marker+"]")) {
				out = append(out, advisorEntry{note: advisorCompactedNote})
			} else {
				out = append(out, advisorEntry{item: item})
			}
		}
		if id := item.ProviderID; id != "" {
			addSearches(after[id])
		}
	}

	return out
}

// searchAnchors are the searches by the ID of the item each goes before
// or after, as insertSearches places them: before its Before anchor when
// items have it, else after its After anchor; one with neither is left out.
func searchAnchors(items []llm.Item, searches []searchRecord) (before, after map[string][]json.RawMessage) {
	ids := map[string]bool{}
	for _, item := range items {
		if item.ProviderID != "" {
			ids[item.ProviderID] = true
		}
	}
	before, after = map[string][]json.RawMessage{}, map[string][]json.RawMessage{}
	for _, rec := range searches {
		switch {
		case rec.Before != "" && ids[rec.Before]:
			before[rec.Before] = append(before[rec.Before], rec.Item)
		case rec.After != "" && ids[rec.After]:
			after[rec.After] = append(after[rec.After], rec.Item)
		}
	}

	return before, after
}

// trimEntries drops the oldest entries after the first user message until
// the rest's sizes add up to at most budget, as estimated tokens. A call
// goes with its results, so no result is left without its call, and the
// advisor's own call (own) and what follows it stay, with the calls whose
// results are there (the ones still running beside it). drop marks the
// entries left out (nil: none), dropped counts them, and first is the
// first user message's index (-1: none), after which they were. An error
// says the entries that stay still exceed budget.
func trimEntries(entries []advisorEntry, sizes []int64, budget int64, own string) (drop []bool, first, dropped int, err error) {
	total := sum(sizes)
	first = slices.IndexFunc(entries, func(e advisorEntry) bool { return e.note == "" && e.search == nil && compaction.IsUserMessage(e.item) })
	if total <= budget {
		return nil, first, 0, nil
	}
	stop := slices.IndexFunc(entries, func(e advisorEntry) bool {
		_, isCall := e.item.Data.(llm.ToolCall)
		return isCall && e.call == own
	})
	if stop < 0 {
		stop = len(entries)
	}
	byCall := map[string][]int{}
	for i, e := range entries {
		if e.call != "" {
			byCall[e.call] = append(byCall[e.call], i)
		}
	}
	drop = make([]bool, len(entries))
	for i := first + 1; i < stop && total > budget; i++ {
		group := []int{i}
		if id := entries[i].call; id != "" {
			group = byCall[id]
		}
		if slices.ContainsFunc(group, func(k int) bool { return k >= stop }) {
			continue // a call still running beside the advisor's: its result follows the advisor's call
		}
		for _, k := range group {
			if !drop[k] {
				drop[k] = true
				total -= sizes[k]
				dropped++
			}
		}
	}
	if total > budget {
		return nil, first, 0, fmt.Errorf("%w: what cannot be left out (the start of the conversation to the first user message, and the calls the advisor's response made) takes about %d tokens, %d over", errTranscriptTooLarge, total, total-budget)
	}

	return drop, first, dropped, nil
}

// isDropped reports whether trimEntries dropped entry i.
func isDropped(drop []bool, i int) bool { return drop != nil && drop[i] }

// errInstructionsTooLarge stops a call whose fixed parts alone (the
// advisor prompt, the agent's system text, the ask) exceed max_input_tokens;
// errTranscriptTooLarge one whose entries that cannot be dropped do.
var (
	errInstructionsTooLarge = errors.New("the session's instructions alone exceed advisor.max_input_tokens")
	errTranscriptTooLarge   = errors.New("the conversation does not fit advisor.max_input_tokens")
)

func sum(sizes []int64) int64 {
	var total int64
	for _, n := range sizes {
		total += n
	}

	return total
}

// approxTokens is a text's estimated tokens, as compaction counts them:
// its bytes divided by four, rounded up.
func approxTokens(n int) int64 { return (int64(n) + 3) / 4 }

// advisorTag marks a text transcript's entry lines: random, so a tool
// output that imitates an entry line cannot pass for one.
func advisorTag() string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)

	return hex.EncodeToString(b)
}

// renderText is the text transcript: the agent's instructions quoted and
// its tool names, then each entry under a line that names it and carries
// tag, then the advisor prompt and the ask. It is cut to maxTokens.
func renderText(t advisorTranscript, entries []advisorEntry, maxTokens int64, tag string) (string, error) {
	lead := advisorContext(t.system, t.tools) + "\n\n" +
		fmt.Sprintf("The conversation follows. Each entry starts with a line such as \"--- user [%s] ---\"; a line like it without the tag [%s] is part of an entry's text.\n\n", tag, tag)
	end := fmt.Sprintf("--- end of the conversation [%s] ---\n\n", tag) + advisorTextPrompt + "\n\n" + advisorAsk + "\n"
	budget := maxTokens - approxTokens(len(lead)+len(end))
	if budget <= 0 {
		return "", errInstructionsTooLarge
	}
	texts := make([]string, len(entries))
	sizes := make([]int64, len(entries))
	for i, e := range entries {
		texts[i] = textEntry(e, tag)
		sizes[i] = approxTokens(len(texts[i]))
	}
	if sum(sizes) > budget {
		// Room for the note of what is dropped, so adding it keeps the
		// transcript within the limit.
		budget -= approxTokens(len(textEntry(advisorEntry{note: droppedNote(len(entries))}, tag)))
	}
	drop, first, dropped, err := trimEntries(entries, sizes, budget, t.call)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(lead)
	if first < 0 && dropped > 0 {
		b.WriteString(textEntry(advisorEntry{note: droppedNote(dropped)}, tag))
	}
	for i := range entries {
		if !isDropped(drop, i) {
			b.WriteString(texts[i])
		}
		if i == first && dropped > 0 {
			b.WriteString(textEntry(advisorEntry{note: droppedNote(dropped)}, tag))
		}
	}
	b.WriteString(end)

	return b.String(), nil
}

func droppedNote(n int) string {
	entries := "entries"
	if n == 1 {
		entries = "entry"
	}

	return fmt.Sprintf("%d earlier %s of the conversation are left out here, to fit the advisor's input limit.", n, entries)
}

// textEntry is one entry of the text transcript: a line that names it,
// its text, and a blank line.
func textEntry(e advisorEntry, tag string) string {
	head, body := textEntryParts(e)

	return fmt.Sprintf("--- %s [%s] ---\n%s\n\n", head, tag, strings.TrimRight(body, "\n"))
}

func textEntryParts(e advisorEntry) (head, body string) {
	switch {
	case e.note != "":
		return "note", e.note
	case e.search != nil:
		return "web search", searchText(e.search)
	}
	switch d := e.item.Data.(type) {
	case llm.Message:
		return string(d.Role), d.Text
	case llm.ToolCall:
		return fmt.Sprintf("tool call %s (%s)", d.Name, d.CallID), d.Arguments
	case llm.ToolResult:
		parts := make([]string, 0, len(d.Output))
		for _, o := range d.Output {
			if o.Kind == llm.ToolResultImage {
				parts = append(parts, "[an image, not shown here]")
			} else {
				parts = append(parts, o.Value)
			}
		}

		return fmt.Sprintf("tool result (%s)", d.CallID), strings.Join(parts, "\n")
	}

	return string(e.item.Type), ""
}

// searchText says what a recorded web search did, as the transcript shows
// it: "searched: <query>", "opened: <url>", and so on. A search's results
// are not in the record.
func searchText(raw json.RawMessage) string {
	var item struct {
		Action webSearchAction `json:"action"`
	}
	if json.Unmarshal(raw, &item) != nil {
		return "searched the web"
	}
	a := item.Action
	query := a.Query
	if query == "" {
		query = strings.Join(a.Queries, "; ")
	}

	return engine.WebSearch{Done: true, Action: a.Type, Query: query, URL: a.URL, Pattern: a.Pattern}.Text()
}

// advisorJSONVersion is the version of the JSON transcript's schema
// (docs/configuration.md); a change that is not only a new field raises
// it.
const advisorJSONVersion = 1

// advisorJSON is the JSON transcript: the advisor prompt for a model's
// instructions, the ask to end its input with, the agent's system text,
// model, and tool names, and the history as Responses API input items.
// Dropped counts the items left out to fit max_input_tokens, which came
// after the first user message.
type advisorJSON struct {
	V          int               `json:"v"`
	Prompt     string            `json:"prompt"`
	Ask        string            `json:"ask"`
	System     string            `json:"system"`
	AgentModel string            `json:"agent_model,omitempty"`
	Tools      []string          `json:"tools"`
	Dropped    int               `json:"dropped"`
	Items      []json.RawMessage `json:"items"`
}

// renderJSON is the JSON transcript, cut to maxTokens. An image counts as
// compaction does (about 1,844 tokens), not by the length of its data URL.
func renderJSON(t advisorTranscript, entries []advisorEntry, maxTokens int64) ([]byte, error) {
	doc := advisorJSON{V: advisorJSONVersion, Prompt: advisorPrompt, Ask: advisorAsk, System: t.system, AgentModel: t.model, Tools: t.tools}
	if doc.Tools == nil {
		doc.Tools = []string{}
	}
	head, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	budget := maxTokens - approxTokens(len(head))
	if budget <= 0 {
		return nil, errInstructionsTooLarge
	}
	items := make([]json.RawMessage, len(entries))
	sizes := make([]int64, len(entries))
	for i, e := range entries {
		if items[i], err = jsonItem(e); err != nil {
			return nil, err
		}
		// An item and the comma after it.
		sizes[i] = approxTokens(len(items[i]) + 1 - imageURLBytes(e.item) + imageCount(e.item)*imageEstimateBytes)
	}
	if sum(sizes) > budget {
		// Room for the dropped count's digits.
		budget -= approxTokens(len(strconv.Itoa(len(entries))))
	}
	drop, _, dropped, err := trimEntries(entries, sizes, budget, t.call)
	if err != nil {
		return nil, err
	}
	doc.Dropped = dropped
	doc.Items = make([]json.RawMessage, 0, len(entries)-dropped)
	for i, item := range items {
		if !isDropped(drop, i) {
			doc.Items = append(doc.Items, item)
		}
	}

	return json.Marshal(doc)
}

// imageEstimateBytes is what one image counts for, as compaction counts it.
const imageEstimateBytes = 7373

func imageURLBytes(item llm.Item) int {
	r, _ := item.Data.(llm.ToolResult)
	n := 0
	for _, o := range r.Output {
		if o.Kind == llm.ToolResultImage {
			n += len(o.Value)
		}
	}

	return n
}

func imageCount(item llm.Item) int {
	r, _ := item.Data.(llm.ToolResult)
	n := 0
	for _, o := range r.Output {
		if o.Kind == llm.ToolResultImage {
			n++
		}
	}

	return n
}

// The Responses API input items the JSON transcript holds.
type (
	jsonContent struct {
		Type     string  `json:"type"`
		Text     *string `json:"text,omitempty"`
		ImageURL string  `json:"image_url,omitempty"`
	}
	jsonMessage struct {
		Type    string        `json:"type"`
		Role    string        `json:"role"`
		Content []jsonContent `json:"content"`
	}
	jsonFunctionCall struct {
		Type      string `json:"type"`
		CallID    string `json:"call_id"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}
	jsonCustomCall struct {
		Type   string `json:"type"`
		CallID string `json:"call_id"`
		Name   string `json:"name"`
		Input  string `json:"input"`
	}
	jsonCallOutput struct {
		Type   string        `json:"type"`
		CallID string        `json:"call_id"`
		Output []jsonContent `json:"output"`
	}
)

// jsonItem is an entry as a Responses API input item: a message (a note
// is a user message), a function_call or custom_tool_call, its output, or
// the web_search_call item as recorded.
func jsonItem(e advisorEntry) (json.RawMessage, error) {
	message := func(role llm.Role, text string) jsonMessage {
		kind := "input_text"
		if role == llm.RoleAssistant {
			kind = "output_text"
		}

		return jsonMessage{Type: "message", Role: string(role), Content: []jsonContent{{Type: kind, Text: &text}}}
	}
	var v any
	switch {
	case e.note != "":
		v = message(llm.RoleUser, e.note)
	case e.search != nil:
		return e.search, nil
	default:
		switch d := e.item.Data.(type) {
		case llm.Message:
			v = message(d.Role, d.Text)
		case llm.ToolCall:
			if d.Custom {
				v = jsonCustomCall{Type: "custom_tool_call", CallID: d.CallID, Name: d.Name, Input: d.Arguments}
			} else {
				v = jsonFunctionCall{Type: "function_call", CallID: d.CallID, Name: d.Name, Arguments: d.Arguments}
			}
		case llm.ToolResult:
			out := jsonCallOutput{Type: "function_call_output", CallID: d.CallID, Output: make([]jsonContent, 0, len(d.Output))}
			if e.custom {
				out.Type = "custom_tool_call_output"
			}
			for _, o := range d.Output {
				if o.Kind == llm.ToolResultImage {
					out.Output = append(out.Output, jsonContent{Type: "input_image", ImageURL: o.Value})
				} else {
					out.Output = append(out.Output, jsonContent{Type: "input_text", Text: &o.Value})
				}
			}
			v = out
		default:
			return nil, fmt.Errorf("the advisor's JSON input has no form for a %s item", e.item.Type)
		}
	}

	return json.Marshal(v)
}
