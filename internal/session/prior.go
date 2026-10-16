package session

import (
	"bufio"
	"bytes"
	"cmp"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/viktordanov/uagent/core"
	"github.com/viktordanov/uagent/harness"

	"github.com/viktordanov/uah/internal/sessionfile"
)

// withPrior puts in front of the runs, as a run of its own with no run ID
// and no directory (IsPrior), the session file's items from before the
// first run: history another program wrote, such as a conversation
// converted from another harness, which no run record holds. A session
// whose first run with an item starts at the first item has none, and its
// file is not read. A subagent has none: a forked one's session starts
// with a copy of its parent's history, which is the parent's, and only
// uah writes subagents. Neither has a copy up to a fork item, the
// runner's own fork. The history is an addition: a session file that does
// not read leaves the runs as they are.
func withPrior(br *bufio.Reader, stateDir, id string, runs []LoadedRun) []LoadedRun {
	if subagent(stateDir, id) {
		return runs
	}
	before := uint64(0) // read every item
	for _, r := range runs {
		if first, ok := firstSequence(r.Record.Dir); ok { // a run that failed before its first item has none
			if first <= 1 {
				return runs
			}
			before = first

			break
		}
	}
	limit := 0
	if before > 0 {
		limit = int(before - 1)
	}
	h, page, err := sessionfile.Read(filepath.Join(stateDir, "sessions", id+sessionFileSuffix), sessionfile.BeforeFirst, limit)
	if err != nil {
		return runs // no file, or one this version does not read: the runs are the transcript, as before
	}
	items := page.Items
	for i, it := range slices.Backward(items) {
		if it.Kind == sessionfile.KindFork {
			items = items[i+1:] // what came before is the parent's

			break
		}
	}
	if len(items) == 0 {
		return runs
	}
	var buf bytes.Buffer
	for _, it := range items {
		data, err := eventData(it)
		if err != nil {
			return runs
		}
		line, err := json.Marshal(struct {
			Sequence   uint64
			RecordedAt time.Time
			Kind       sessionfile.Kind
			Data       json.RawMessage
		}{it.Sequence, it.RecordedAt, it.Kind, data})
		if err != nil {
			return runs
		}
		buf.Write(append(line, '\n'))
	}
	br.Reset(&buf)
	events, err := decodeEvents(br)
	if err != nil {
		return runs
	}
	start, end := items[0].RecordedAt, items[len(items)-1].RecordedAt
	prior := LoadedRun{
		Record: harness.RunRecord{Complete: true, Result: core.Result{
			Request: core.Request{SessionID: cmp.Or(h.ID, id)}, Status: core.StatusOK,
			StartedAt: start, Wall: end.Sub(start),
		}},
		Events: events,
	}

	return append([]LoadedRun{prior}, runs...)
}

// eventData is an item's data as a run's events file has it: a
// tool_call_status item's operation snapshots, which the session file keeps
// beside the item, go inside it, where the decoders find a call's result.
func eventData(it sessionfile.Item) (json.RawMessage, error) {
	if len(it.Operations) == 0 {
		return it.Data, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(it.Data, &fields); err != nil {
		return nil, err
	}
	ops, err := json.Marshal(it.Operations)
	if err != nil {
		return nil, err
	}
	fields["Operations"] = ops

	return json.Marshal(fields)
}

// IsPrior reports whether a loaded run is the history before a session's
// first run (withPrior), which has no run record.
func (r LoadedRun) IsPrior() bool { return r.Record.Dir == "" && r.Record.Result.Request.RunID == "" }

// firstSequence is the Sequence of a run's first item; ok is false when
// the run has none (it failed before its first) or its events do not read.
func firstSequence(runDir string) (uint64, bool) {
	f, err := os.Open(filepath.Join(runDir, harness.EventsFile))
	if err != nil {
		return 0, false
	}
	defer f.Close()
	line, err := bufio.NewReader(f).ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return 0, false
	}
	var it struct{ Sequence uint64 }
	if json.Unmarshal(line, &it) != nil || it.Sequence == 0 {
		return 0, false
	}

	return it.Sequence, true
}

// subagent reports whether a session is a subagent: by its ID, or by the
// parent its sidecar names.
func subagent(stateDir, id string) bool {
	if strings.HasPrefix(id, SubagentIDPrefix) {
		return true
	}
	sc, found, err := ReadSidecar(filepath.Join(stateDir, "sessions"), id)

	return err == nil && found && sc.Parent != ""
}
