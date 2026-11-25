package embedded

import (
	"bufio"
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

// Codex sends a response's web_search_call items back with the rest of the
// conversation (core/src/context_manager/history.rs in rust-v0.156.1), so
// the model sees its own searches. The runner drops them (v0.1.1). uah
// records each one with the ID of the output item it came before, in
// sessions/<id>.websearch.jsonl, and the transport inserts it before that
// item wherever a later turn request's input has it. The rest of the body
// stays byte for byte as the runner wrote it. A compaction, a rewind, or
// /clear removes the anchor from the input, and with it the search. See
// docs/design/web-search.md.

// searchRecord is one dropped web search: Item goes before the input item
// whose ID is Before, or, when none came after it in its response, after
// the one whose ID is After.
type searchRecord struct {
	At     time.Time       `json:"at"`
	Before string          `json:"before,omitempty"`
	After  string          `json:"after,omitempty"`
	Item   json.RawMessage `json:"item"`
}

// searchLog is a session's recorded searches.
type searchLog struct {
	path   string
	logger *slog.Logger

	mu      sync.Mutex
	records []searchRecord
}

func searchLogPath(dir, sessionID string) string {
	return filepath.Join(dir, sessionID+".websearch.jsonl")
}

// openSearchLog reads a session's searches; a missing file is empty, and
// a line that does not decode is skipped.
func openSearchLog(dir, sessionID string) (*searchLog, error) {
	l := &searchLog{path: searchLogPath(dir, sessionID)}
	f, err := os.Open(l.path)
	if errors.Is(err, fs.ErrNotExist) {
		return l, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to open the web search log: %w", err)
	}
	defer f.Close()
	r := bufio.NewReader(f)
	for {
		line, rerr := r.ReadBytes('\n')
		var rec searchRecord
		if json.Unmarshal(bytes.TrimSpace(line), &rec) == nil && len(rec.Item) > 0 && (rec.Before != "" || rec.After != "") {
			l.records = append(l.records, rec)
		}
		if errors.Is(rerr, io.EOF) {
			return l, nil
		}
		if rerr != nil {
			return nil, fmt.Errorf("failed to read the web search log: %w", rerr)
		}
	}
}

// add appends records to the file and to the log.
func (l *searchLog) add(records []searchRecord) error {
	if len(records) == 0 {
		return nil
	}
	var buf bytes.Buffer
	for _, rec := range records {
		line, err := json.Marshal(rec)
		if err != nil {
			return fmt.Errorf("failed to encode a web search: %w", err)
		}
		buf.Write(append(line, '\n'))
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("failed to open the web search log: %w", err)
	}
	_, err = f.Write(buf.Bytes())
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("failed to write the web search log: %w", err)
	}
	l.records = append(l.records, records...)

	return nil
}

// snapshot is the log's records; a nil log, a provider's without web
// search, has none.
func (l *searchLog) snapshot() []searchRecord {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	return slices.Clone(l.records)
}

// copySearches gives a forked child its parent's searches: its history
// holds the parent's items, so the anchors are there.
func copySearches(dir, parentID, childID string) error {
	parent, err := openSearchLog(dir, parentID)
	if err != nil {
		return err
	}
	child, err := openSearchLog(dir, childID)
	if err != nil {
		return err
	}

	return child.add(parent.snapshot())
}

// apply returns body with the recorded searches inserted next to their
// anchors, or body itself when none applies.
func (l *searchLog) apply(body []byte) []byte {
	if records := l.snapshot(); len(records) > 0 {
		if out, ok := insertSearches(body, records); ok {
			return out
		}
	}

	return body
}

// inputItem is where an input item lies in the body, and its ID.
type inputItem struct {
	id         string
	start, end int64
}

// insertion is bytes to insert at an offset.
type insertion struct {
	at   int64
	text []byte
}

// insertSearches inserts each record's item next to its anchor in body's
// input array; ok is false when none has its anchor there.
func insertSearches(body []byte, records []searchRecord) ([]byte, bool) {
	items := inputItems(body)
	if len(items) == 0 {
		return nil, false
	}
	byID := make(map[string]inputItem, len(items))
	for _, it := range items {
		if it.id != "" {
			byID[it.id] = it
		}
	}
	var ins []insertion
	for _, rec := range records {
		if it, ok := byID[rec.Before]; ok && rec.Before != "" {
			ins = append(ins, insertion{at: it.start, text: append(slices.Clone([]byte(rec.Item)), ',')})
		} else if it, ok := byID[rec.After]; ok && rec.After != "" {
			ins = append(ins, insertion{at: it.end, text: append([]byte{','}, rec.Item...)})
		}
	}
	if len(ins) == 0 {
		return nil, false
	}
	slices.SortStableFunc(ins, func(a, b insertion) int { return int(a.at - b.at) })
	out := make([]byte, 0, len(body)+len(ins)*256)
	var from int64
	for _, x := range ins {
		out = append(out, body[from:x.at]...)
		out = append(out, x.text...)
		from = x.at
	}

	return append(out, body[from:]...), true
}

// inputItems finds the items of the body's top-level input array, or none
// when the body is not a JSON object with one.
func inputItems(body []byte) []inputItem {
	dec := jsontext.NewDecoder(bytes.NewReader(body))
	if tok, err := dec.ReadToken(); err != nil || tok.Kind() != '{' {
		return nil
	}
	for {
		name, err := dec.ReadToken()
		if err != nil || name.Kind() != '"' {
			return nil
		}
		if name.String() != "input" {
			if dec.SkipValue() != nil {
				return nil
			}

			continue
		}
		if tok, err := dec.ReadToken(); err != nil || tok.Kind() != '[' {
			return nil
		}
		var items []inputItem
		for dec.PeekKind() != ']' {
			v, err := dec.ReadValue()
			if err != nil {
				return nil
			}
			end := dec.InputOffset()
			var head struct {
				ID string `json:"id"`
			}
			_ = json.Unmarshal(v, &head)
			items = append(items, inputItem{id: head.ID, start: end - int64(len(v)), end: end})
		}

		return items
	}
}
