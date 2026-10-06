package embedded

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sync"

	"github.com/viktordanov/uah/internal/compaction"
)

// Remote compaction is Codex's (rust-v0.159.1, core/src/compact_remote_v2*.rs):
// the turn request's history ends with a {"type":"compaction_trigger"}
// input item, and the provider answers with one output item,
// {"type":"compaction","encrypted_content":...}, which later requests send
// in place of the history it covers. The runner's llm.Item (v0.1.1) has
// neither, so the transport does both, as it does for web searches
// (searchlog.go): it appends the trigger to the body of the request the
// compactor marks, the scanner takes the item out of the answer before the
// runner parses it (sse.go), and puts it back into later turn requests
// where the compaction's placeholder message is. A probe on openai-codex on
// 2026-09-30 found the backend accepts both, with or without Codex's
// x-codex-beta-features header. See docs/design/compaction.md.

// remoteCallKey marks the request that asks for a remote compaction.
type remoteCallKey struct{}

// remoteCall is one remote compaction request: what the answer carried.
type remoteCall struct {
	mu   sync.Mutex
	item json.RawMessage
}

func (c *remoteCall) result() (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.item == nil {
		return nil, errors.New("the provider answered without a compaction item")
	}

	return c.item, nil
}

// reset forgets the item of an earlier attempt.
func (c *remoteCall) reset() {
	if c != nil {
		c.mu.Lock()
		c.item = nil
		c.mu.Unlock()
	}
}

// remoteItemsKey carries the remote compaction that a turn request's
// placeholder stands for.
type remoteItemsKey struct{}

type remoteItem struct {
	marker string
	item   json.RawMessage
}

// withRemoteItem gives a turn request the item its placeholder stands for.
func withRemoteItem(ctx context.Context, rec *compaction.Record) context.Context {
	if rec == nil || len(rec.Remote) == 0 {
		return ctx
	}

	return context.WithValue(ctx, remoteItemsKey{}, remoteItem{marker: compaction.RemoteMarker(*rec), item: rec.Remote})
}

var compactionTrigger = []byte(`{"type":"compaction_trigger"}`)

// rewriteBody gives an attempt the body the provider sees: the session's
// recorded searches inserted (searchlog.go), a remote compaction's item in
// place of its placeholder, and the trigger after the history of a request
// that asks for one. Other requests pass unchanged.
func (c *modelCall) rewriteBody(req *http.Request) (*http.Request, error) {
	item, hasItem := req.Context().Value(remoteItemsKey{}).(remoteItem)
	searches := c.log != nil && len(c.log.snapshot()) > 0 // else the body would be read and copied for nothing
	if !searches && !hasItem && c.remote == nil && c.kind != kindReview && !c.guardianMarkers || req.Body == nil || req.Header.Get("Content-Encoding") != "" {
		return req, nil
	}
	body, err := io.ReadAll(req.Body)
	_ = req.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("failed to read the request body: %w", err)
	}
	if searches {
		body = c.log.apply(body)
	}
	if hasItem {
		body = replaceItem(body, item)
	}
	if c.remote != nil {
		body = appendInput(body, compactionTrigger)
	}
	clone := req.Clone(req.Context())
	body, err = c.reviewMarkers(body, clone.Header)
	if err != nil {
		return nil, err
	}
	clone.Body, clone.ContentLength = io.NopCloser(bytes.NewReader(body)), int64(len(body))
	clone.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }

	return clone, nil
}

// replaceItem puts the item where the input item with its marker is.
func replaceItem(body []byte, it remoteItem) []byte {
	for _, x := range inputItems(body) {
		if bytes.Contains(body[x.start:x.end], []byte(it.marker)) {
			return slices.Concat(body[:x.start], it.item, body[x.end:])
		}
	}

	return body
}

// appendInput adds item at the end of the body's input array.
func appendInput(body, item []byte) []byte {
	items := inputItems(body)
	if len(items) == 0 {
		return body
	}
	end := items[len(items)-1].end

	return slices.Concat(body[:end], []byte{','}, item, body[end:])
}

// compactedMessage stands in for the compaction item in what the runner
// parses.
var compactedMessage = json.RawMessage(`{"type":"message","id":"msg_uah_compaction","role":"assistant","status":"completed",` +
	`"content":[{"type":"output_text","text":"(compacted)","annotations":[]}]}`)

// swap returns an SSE line with any compaction item replaced, keeping the
// finished one.
func (c *remoteCall) swap(line []byte) []byte {
	payload, ok := bytes.CutPrefix(line, []byte("data: "))
	if !ok || !bytes.Contains(payload, []byte(`"compaction"`)) {
		return line
	}
	var ev map[string]json.RawMessage
	if json.Unmarshal(payload, &ev) != nil {
		return line
	}
	if item, ok := ev["item"]; ok && isCompaction(item) {
		var typ string
		_ = json.Unmarshal(ev["type"], &typ)
		if typ == eventItemDone {
			c.mu.Lock()
			c.item = slices.Clone(item)
			c.mu.Unlock()
		}
		ev["item"] = compactedMessage
	}
	if resp, ok := ev["response"]; ok {
		var item json.RawMessage
		ev["response"], item = swapOutput(resp)
		c.mu.Lock()
		if c.item == nil {
			c.item = item // the API lists it in the completed response
		}
		c.mu.Unlock()
	}
	out, err := json.Marshal(ev)
	if err != nil {
		return line
	}

	return append(append([]byte("data: "), out...), '\n')
}

// swapOutput replaces the compaction item in a response's output and
// returns it.
func swapOutput(resp json.RawMessage) (json.RawMessage, json.RawMessage) {
	var r map[string]json.RawMessage
	if json.Unmarshal(resp, &r) != nil {
		return resp, nil
	}
	var output []json.RawMessage
	if json.Unmarshal(r["output"], &output) != nil {
		return resp, nil
	}
	var found json.RawMessage
	for i, item := range output {
		if isCompaction(item) {
			found, output[i] = slices.Clone(item), compactedMessage
		}
	}
	if found == nil {
		return resp, nil
	}
	b, err := json.Marshal(output)
	if err != nil {
		return resp, nil
	}
	r["output"] = b
	out, err := json.Marshal(r)
	if err != nil {
		return resp, nil
	}

	return out, found
}

func isCompaction(item json.RawMessage) bool {
	var head struct {
		Type string `json:"type"`
	}

	return json.Unmarshal(item, &head) == nil && (head.Type == "compaction" || head.Type == "compaction_summary")
}
