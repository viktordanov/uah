package embedded

import (
	"bytes"
	"cmp"
	"encoding/json"
	"strings"
	"time"

	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/patch"
)

// Stream events the scanner acts on.
const (
	eventItemAdded  = "response.output_item.added"
	eventItemDone   = "response.output_item.done"
	eventCompleted  = "response.completed"
	eventIncomplete = "response.incomplete"
	eventError      = "error"
)

// streamEvent is the part of a Responses stream event the scanner reads.
type streamEvent struct {
	Type         string   `json:"type"`
	ItemID       string   `json:"item_id"`
	Delta        string   `json:"delta"`
	SummaryIndex int      `json:"summary_index"`
	OutputIndex  *int     `json:"output_index"`
	Code         string   `json:"code"`
	Message      string   `json:"message"`
	Error        apiError `json:"error"`
	Response     struct {
		ID    string   `json:"id"`
		Error apiError `json:"error"`
	} `json:"response"`
	Item struct {
		ID     string          `json:"id"`
		Type   string          `json:"type"`
		Name   string          `json:"name"`
		Status string          `json:"status"`
		Phase  string          `json:"phase"`
		Action webSearchAction `json:"action"`
	} `json:"item"`
}

// apiError is an in-band failure: a response.failed's or an error event's.
type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e apiError) String() string { return strings.TrimPrefix(e.Code+": "+e.Message, ": ") }

// toolCall is the call being written: the file an apply_patch names last.
type toolCall struct {
	id, name, target, tail string
	bytes                  int64
}

var (
	dataPrefix = []byte("data:")
	// streamTypes are the events the scanner decodes; a finished item,
	// which can be large, only when it is one of doneTypes.
	streamTypes = [][]byte{
		[]byte(`"response.created"`),
		[]byte(`"response.output_text.delta"`), []byte(`"response.reasoning_summary_text.delta"`), []byte(`"` + eventItemAdded + `"`),
		[]byte(`"` + eventItemDone + `"`), []byte(`"response.function_call_arguments.delta"`), []byte(`"response.custom_tool_call_input.delta"`),
		[]byte(`"` + eventCompleted + `"`), []byte(`"response.failed"`), []byte(`"` + eventIncomplete + `"`), []byte(`"` + eventError + `"`),
	}
	doneTypes = [][]byte{[]byte(`"` + webSearchCall + `"`), []byte(`"function_call"`), []byte(`"custom_tool_call"`)}
)

// line reads a line of the stream and returns it as the runner reads it.
func (c *modelCall) line(line []byte) []byte {
	if c.remote != nil {
		line = c.remote.swap(line)
	}
	payload, data := bytes.CutPrefix(bytes.TrimRight(line, "\r\n"), dataPrefix)
	c.mu.Lock()
	now := time.Now()
	c.a.RecvBytes += int64(len(line))
	if data {
		c.a.Events++
		c.a.LongestGapMS = max(c.a.LongestGapMS, now.Sub(c.last).Milliseconds())
		c.last = now
		c.progressLocked(engine.PhaseStreaming, false)
	}
	c.mu.Unlock()
	var ev streamEvent
	if data && containsAny(payload, streamTypes) && (!bytes.Contains(payload, []byte(eventItemDone)) || containsAny(payload, doneTypes)) &&
		json.Unmarshal(payload, &ev) == nil {
		c.output(ev)
		c.event(ev)
	}

	return line
}

// event turns a stream event into engine events and the attempt's state.
func (c *modelCall) event(ev streamEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch ev.Type {
	case "response.created":
		if c.latestResponse != nil && ev.Response.ID != "" {
			c.latestResponse.Store(&ev.Response.ID)
		}
	case eventCompleted, eventIncomplete:
		c.terminal = ev.Type
	case "response.failed", eventError:
		c.terminal, c.failure = ev.Type, cmp.Or(apiError{ev.Code, ev.Message}, ev.Error, ev.Response.Error)
	case eventItemAdded, eventItemDone:
		c.item(ev)
	case "response.function_call_arguments.delta", "response.custom_tool_call_input.delta":
		if ev.ItemID != c.tool.id {
			return
		}
		c.tool.bytes += int64(len(ev.Delta))
		target := c.tool.target
		if c.tool.name == patch.ToolName {
			tail := c.tool.tail + ev.Delta
			c.tool.target, c.tool.tail = cmp.Or(patch.LastFile(tail), target), tail[max(len(tail)-512, 0):]
		}
		c.progressLocked(engine.PhaseStreaming, c.tool.target != target)
	case "response.output_text.delta":
		if c.text && ev.Delta != "" {
			c.streamed = true
			c.pushLocked(engine.TextDelta{At: time.Now(), ItemID: ev.ItemID, Text: ev.Delta, Final: c.final[ev.ItemID]})
		}
	case "response.reasoning_summary_text.delta":
		if c.text && ev.Delta != "" {
			c.streamed = true
			c.pushLocked(engine.ReasoningDelta{At: time.Now(), ItemID: ev.ItemID, Part: ev.SummaryIndex, Text: ev.Delta})
		}
	}
}

// item reads an output item that started or finished.
func (c *modelCall) item(ev streamEvent) {
	switch {
	case ev.Item.Type == webSearchCall:
		a := ev.Item.Action
		c.pushLocked(engine.WebSearch{At: time.Now(), ItemID: ev.Item.ID, Done: ev.Type == eventItemDone, Action: a.Type, Query: a.query(), URL: a.URL, Pattern: a.Pattern})
	case ev.Type == eventItemDone && ev.Item.ID == c.tool.id:
		c.tool = toolCall{}
		c.progressLocked(engine.PhaseStreaming, true)
	case ev.Type == eventItemDone:
	case ev.Item.Type == "function_call" || ev.Item.Type == "custom_tool_call":
		c.tool, c.a.Tool = toolCall{id: ev.Item.ID, name: ev.Item.Name}, ev.Item.Name
		c.progressLocked(engine.PhaseStreaming, true)
	case ev.Item.Phase == "final_answer":
		c.final[ev.Item.ID] = true
	}
}

func containsAny(b []byte, subs [][]byte) bool {
	for _, sub := range subs {
		if bytes.Contains(b, sub) {
			return true
		}
	}

	return false
}
