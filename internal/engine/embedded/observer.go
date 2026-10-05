package embedded

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"io"
	"sync"

	"github.com/viktordanov/uah-core/harness/session"
	"github.com/viktordanov/uah-core/harness/sessionstore"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/engine"
)

// observer writes each persisted session item as one JSON line, exactly as
// the runner prints it. It also emits the diff of each applied patch, the
// output of each failed command and finished MCP call, and the answers to
// each request_user_input call, read from the same line, so the live view
// and a reloaded one agree, and each model response that called no tool
// (engine.Answered).
type observer struct {
	sessionID session.ID
	out       io.Writer
	cancel    context.CancelFunc
	// emit, when set, receives engine.PatchApplied, engine.ToolOutput,
	// engine.QuestionsAnswered, and engine.Answered.
	emit func(core.Event)
	// early are the items recorded before the run started, written first.
	early []sessionstore.Item

	mu      sync.Mutex
	failure error
	// patched, output, and answered are the calls whose events were
	// emitted.
	patched, output, answered map[string]bool
}

func (o *observer) observe(id session.ID, item sessionstore.Item) {
	if id != o.sessionID {
		return
	}
	for _, ev := range o.write(item) {
		if o.emit != nil {
			o.emit(ev)
		}
	}
}

// write writes the item and returns the patch it completes, the output it
// finishes, and the answers it records, each once per call.
func (o *observer) write(item sessionstore.Item) []core.Event {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.failure != nil {
		return nil
	}
	line, err := json.Marshal(item)
	if err == nil {
		_, err = o.out.Write(append(line, '\n'))
	}
	if err != nil {
		o.failure = fmt.Errorf("failed to write session item %d: %w", item.Sequence, err)
		o.cancel()

		return nil
	}
	var events []core.Event
	if ev, ok := engine.PatchFromItem(line); ok && once(&o.patched, ev.CallID) {
		events = append(events, ev)
	}
	if ev, ok := engine.ToolOutputFromItem(line); ok && once(&o.output, ev.CallID) {
		events = append(events, ev)
	}
	if ev, ok := engine.QuestionsFromItem(line); ok && once(&o.answered, ev.CallID) {
		events = append(events, ev)
	}
	if ev, ok := engine.AnswerFromItem(line); ok {
		events = append(events, ev)
	}

	return events
}

// once records id in seen and reports whether it is new.
func once(seen *map[string]bool, id string) bool {
	if (*seen)[id] {
		return false
	}
	if *seen == nil {
		*seen = map[string]bool{}
	}
	(*seen)[id] = true

	return true
}

func (o *observer) err() error {
	o.mu.Lock()
	defer o.mu.Unlock()

	return o.failure
}

// writeError writes the runner's error event.
func writeError(out io.Writer, err error) {
	line, merr := json.Marshal(struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	}{"error", err.Error()})
	if merr == nil {
		_, _ = out.Write(append(line, '\n'))
	}
}
