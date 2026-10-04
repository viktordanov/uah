package embedded

import (
	"encoding/json"
	"log/slog"
	"maps"
	"slices"
	"time"

	"github.com/viktordanov/uah-core/harness/llm"
)

// Web search is the provider's hosted tool (docs/design/web-search.md).
// The runner encodes llm.Tool{Type: ToolHosted, Name: "web_search"} as
// {"type":"web_search"} (responsesapi/request.go in v0.1.1) and drops the
// web_search_call items of a response, so the model's searches leave no
// record. The scanner reads them from the stream instead (sse.go), for the
// transcript.

// webSearchCall is the output item type of a hosted web search.
const webSearchCall = "web_search_call"

var webSearchTool = llm.Tool{Type: llm.ToolHosted, Name: "web_search"}

// hostedTools are the hosted tools a run offers: web search when the
// engine offers it, the run's provider has it, and the session's scope
// offers it (a scope without "web_search", such as /review's reviewer's,
// leaves it out, as Codex turns search off for a review).
func (w *wiring) hostedTools(provider, sessionID string) []llm.Tool {
	if !w.e.cfg.WebSearch || !w.e.scope(sessionID).offers(webSearchTool.Name) || !w.e.cfg.Tools.Allows(webSearchTool.Name) {
		return nil
	}
	if p, err := w.e.provider(provider); err != nil || !p.WebSearch {
		return nil
	}

	return []llm.Tool{webSearchTool}
}

// searchLog opens the session's recorded searches on a provider that runs
// web search, so its turn requests get them back; nil elsewhere, and for a
// session whose scope leaves web search out.
func (w *wiring) searchLog(provider, sessionID string) (*searchLog, error) {
	if p, err := w.e.provider(provider); err != nil || !p.WebSearch || !w.e.scope(sessionID).offers(webSearchTool.Name) || !w.e.cfg.Tools.Allows(webSearchTool.Name) {
		return nil, nil //nolint:nilnil // no log: the provider has no web search
	}
	l, err := openSearchLog(w.l.SessionsDir, sessionID)
	if l != nil {
		l.logger = w.e.cfg.Logger
	}

	return l, err
}

// webSearchAction is a web_search_call item's action. A search has a
// query, or queries in newer responses.
type webSearchAction struct {
	Type    string   `json:"type"`
	Query   string   `json:"query,omitempty"`
	Queries []string `json:"queries,omitempty"`
	URL     string   `json:"url,omitempty"`
	Pattern string   `json:"pattern,omitempty"`
}

// query is the search's query, or its first query with " ..." when there
// are more, as Codex shows it.
func (a webSearchAction) query() string {
	switch {
	case a.Query != "":
		return a.Query
	case len(a.Queries) == 1:
		return a.Queries[0]
	case len(a.Queries) > 1 && a.Queries[0] != "":
		return a.Queries[0] + " ..."
	}

	return ""
}

// IsZero leaves out an action without a type, as Codex does.
func (a webSearchAction) IsZero() bool { return a.Type == "" }

// attemptOutputs are an attempt's output items by output index, and its
// finished web searches, from which record makes the searchRecords.
type attemptOutputs struct {
	items    map[int]outputRef
	searches map[int]json.RawMessage
}

// outputRef is an output item's type and ID.
type outputRef struct{ typ, id string }

// output notes an output item that started or a web search that finished.
func (c *modelCall) output(ev streamEvent) {
	if c.log == nil || ev.OutputIndex == nil || ev.Item.Type == "" {
		return
	}
	var item json.RawMessage
	if ev.Type == eventItemDone && ev.Item.Type == webSearchCall {
		item = searchItem(ev)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	o := &c.outputs
	if o.items == nil {
		o.items, o.searches = map[int]outputRef{}, map[int]json.RawMessage{}
	}
	o.items[*ev.OutputIndex] = outputRef{typ: ev.Item.Type, id: ev.Item.ID}
	if item != nil {
		o.searches[*ev.OutputIndex] = item
	}
}

// searchItem is a finished web search as Codex sends it back: its type,
// ID, status, and action (codex-rs/protocol/src/models.rs).
func searchItem(ev streamEvent) json.RawMessage {
	item, err := json.Marshal(struct {
		Type   string          `json:"type"`
		ID     string          `json:"id,omitempty"`
		Status string          `json:"status,omitempty"`
		Action webSearchAction `json:"action,omitzero"`
	}{webSearchCall, ev.Item.ID, ev.Item.Status, ev.Item.Action})
	if err != nil {
		return nil
	}

	return item
}

// record keeps the successful attempt's searches, each anchored to the
// next output item the runner keeps, else the one before it.
func (c *modelCall) record() {
	c.mu.Lock()
	o := c.outputs
	c.mu.Unlock()
	if c.log == nil || len(o.searches) == 0 {
		return
	}
	indexes := slices.Sorted(maps.Keys(o.items))
	var records []searchRecord
	for _, i := range slices.Sorted(maps.Keys(o.searches)) {
		rec := searchRecord{At: time.Now(), Item: o.searches[i]}
		for _, j := range indexes {
			ref := o.items[j]
			if ref.typ == webSearchCall || ref.id == "" {
				continue
			}
			if j < i {
				rec.After = ref.id
			} else if rec.Before == "" {
				rec.Before = ref.id
			}
		}
		if rec.Before != "" || rec.After != "" {
			records = append(records, rec)
		}
	}
	if err := c.log.add(records); err != nil && c.log.logger != nil {
		c.log.logger.Warn("failed to record web searches", slog.String("error", err.Error()))
	}
}
