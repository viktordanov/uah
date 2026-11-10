package compaction

import (
	"fmt"
	"slices"

	"github.com/viktordanov/uah-core/harness/llm"
)

// Elision replaces old tool outputs with short stubs before a summary is
// needed. Tool outputs are most of a long context (72% in the owner's
// sessions), and old ones are rarely read again. A stubbed output stays a
// stub on every later request, so the prompt cache holds after the pass.
type Elision struct {
	// AfterCalls elides an output once this many tool calls came after
	// its call; 0 turns elision off.
	AfterCalls int
	// BigTokens elides an output over this many tokens once BigAfterCalls
	// calls came after it (0: no rule for big outputs).
	BigTokens     int64
	BigAfterCalls int
}

// DefaultElision is uah's default: outputs older than ten calls, and
// outputs over 2,000 tokens older than three.
var DefaultElision = Elision{AfterCalls: 10, BigTokens: 2_000, BigAfterCalls: 3}

// elideMinTokens is the smallest output worth a stub: a stub takes about
// 40 tokens.
const elideMinTokens = 100

// Elidable are the call IDs of the outputs in view that the rules elide
// now, in order, without those already elided. SkillUse outputs are never
// elided: a skill's body is instructions the model follows, not data.
func (e Elision) Elidable(view []llm.Item, elided []string) []string {
	if e.AfterCalls <= 0 {
		return nil
	}
	calls := map[string]llm.ToolCall{}
	after := map[string]int{} // tool calls after each call
	n := 0
	for _, item := range view {
		if c, ok := item.Data.(llm.ToolCall); ok {
			calls[c.CallID] = c
			after[c.CallID] = n
			n++
		}
	}
	done := set(elided)
	var out []string
	for _, item := range view {
		r, ok := item.Data.(llm.ToolResult)
		call, known := calls[r.CallID]
		if !ok || !known || call.Name == toolSkillUse || done[r.CallID] {
			continue
		}
		age, tokens := n-1-after[r.CallID], EstimateTokens([]llm.Item{item})
		old := age >= e.AfterCalls || (e.BigTokens > 0 && tokens > e.BigTokens && age >= e.BigAfterCalls)
		if old && tokens >= elideMinTokens {
			out = append(out, r.CallID)
		}
	}

	return out
}

// Elide replaces the output of each call in ids with a stub. items is not
// changed.
func Elide(items []llm.Item, ids []string) []llm.Item {
	if len(ids) == 0 {
		return items
	}
	calls := map[string]llm.ToolCall{}
	for _, item := range items {
		if c, ok := item.Data.(llm.ToolCall); ok {
			calls[c.CallID] = c
		}
	}
	elide := set(ids)
	out := slices.Clone(items)
	for i, item := range out {
		if r, ok := item.Data.(llm.ToolResult); ok && elide[r.CallID] {
			if c, ok := calls[r.CallID]; ok {
				out[i] = llm.Item{ProviderID: item.ProviderID, Type: item.Type, Data: Stub(c, r)}
			}
		}
	}

	return out
}

// ElideToFit stubs tool outputs from the newest back until the estimate of
// items is at most budget tokens, as Codex trims a history over the window
// before a remote compaction. Each output keeps its item and call ID. ids
// are the stubbed calls, newest first; ok is false when items is still over
// budget with every output a stub. items is not changed.
func ElideToFit(items []llm.Item, budget int64) (out []llm.Item, ids []string, ok bool) {
	total := EstimateTokens(items)
	if total <= budget {
		return items, nil, true
	}
	calls := map[string]llm.ToolCall{}
	for _, item := range items {
		if c, ok := item.Data.(llm.ToolCall); ok {
			calls[c.CallID] = c
		}
	}
	out = slices.Clone(items)
	for i := len(out) - 1; i >= 0 && total > budget; i-- {
		r, isResult := out[i].Data.(llm.ToolResult)
		c, known := calls[r.CallID]
		if !isResult || !known {
			continue
		}
		stub := llm.Item{ProviderID: out[i].ProviderID, Type: out[i].Type, Data: Stub(c, r)}
		saved := EstimateTokens(out[i:i+1]) - EstimateTokens([]llm.Item{stub})
		if saved <= 0 {
			continue // already a stub, or too small for one
		}
		out[i], total = stub, total-saved
		ids = append(ids, r.CallID)
	}
	if len(ids) == 0 {
		return items, nil, false
	}

	return out, ids, total <= budget
}

// Stub is what the model sees in place of an elided output: the tool, its
// command, the exit code, and the size.
func Stub(call llm.ToolCall, r llm.ToolResult) llm.ToolResult {
	what := call.Name
	if cmd := Argument(call.Arguments, "command"); cmd != "" {
		what += fmt.Sprintf(" %#q", clip(oneLine(cmd), 200))
	} else if p := Argument(call.Arguments, "path"); p != "" {
		what += " " + p
	}
	if m := exitLine.FindAllStringSubmatch(ResultText(r), -1); len(m) > 0 {
		what += ", exit " + m[len(m)-1][1]
	}
	size := EstimateTokens([]llm.Item{{Type: llm.ItemToolResult, Data: r}})
	text := fmt.Sprintf("[uah elided this output to save context: %s, %s tokens. Run it again if you need it.]", what, Commas(size))

	return llm.ToolResult{CallID: r.CallID, Output: []llm.ToolResultOutput{{Kind: llm.ToolResultText, Value: text}}}
}

func set(values []string) map[string]bool {
	m := make(map[string]bool, len(values))
	for _, v := range values {
		m[v] = true
	}

	return m
}

// union is a followed by the values of b it lacks.
func union(a, b []string) []string {
	out := slices.Clone(a)
	have := set(a)
	for _, v := range b {
		if !have[v] {
			have[v] = true
			out = append(out, v)
		}
	}

	return out
}

// WithElided is rec with ids elided too: an elision pass keeps the
// summary the record has and adds stubs.
func (r Record) WithElided(ids []string) Record {
	r.Elided = union(r.Elided, ids)

	return r
}

// StillElided are the ids whose outputs are among items: what a new
// summary keeps of the elisions before it, since it covers the rest.
func StillElided(items []llm.Item, ids []string) []string {
	have := set(ids)
	var out []string
	for _, item := range items {
		if r, ok := item.Data.(llm.ToolResult); ok && have[r.CallID] {
			out = append(out, r.CallID)
		}
	}

	return out
}
