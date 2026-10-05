package engine

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"
)

// modelResponse is the kind of a session item with a model response.
const modelResponse = "model_response"

// Answered is a model response that ended the model's turn: it has an
// assistant message and no tool call. Text is its last message, the one
// in the final_answer phase when it has one. The runner's run goes on
// while an earlier call still runs, and a heartbeat or that call's result
// wakes the model again; a session that ends on the first answer, such
// as /review's reviewer, ends on this. The embedded engine adds it when
// the session stores the response.
type Answered struct {
	At     time.Time
	TurnID string
	Text   string
}

func (e Answered) OccurredAt() time.Time { return e.At }

// responseItem is the part of a model_response item AnswerFromItem reads.
type responseItem struct {
	RecordedAt time.Time
	Kind       string
	Data       struct {
		TurnID   string
		Response struct {
			Output []struct {
				Type string
				Data struct {
					Role, Text, Phase string
				}
			}
		}
	}
}

// AnswerFromItem reads a session item, one line of the session file as
// the runner writes it, and returns the answer of a model response that
// called no tool.
func AnswerFromItem(line []byte) (Answered, bool) {
	if !bytes.Contains(line, []byte(`"`+modelResponse+`"`)) || bytes.Contains(line, []byte(`"Type":"tool_call"`)) {
		return Answered{}, false
	}
	var item responseItem
	if json.Unmarshal(line, &item) != nil || item.Kind != modelResponse {
		return Answered{}, false
	}
	a := Answered{At: item.RecordedAt, TurnID: item.Data.TurnID}
	final := false
	for _, out := range item.Data.Response.Output {
		switch {
		case out.Type == "tool_call":
			return Answered{}, false
		case out.Type != "message" || out.Data.Role != "assistant" || strings.TrimSpace(out.Data.Text) == "":
		case out.Data.Phase == "final_answer":
			a.Text, final = out.Data.Text, true
		case !final:
			a.Text = out.Data.Text
		}
	}

	return a, a.Text != ""
}
