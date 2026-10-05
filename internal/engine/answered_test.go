package engine_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/viktordanov/uah/internal/engine"
)

// TestAnswerFromItem reads the answer of a model response that called no
// tool, the final_answer message when it has one, as the reviewer's
// session file records them.
func TestAnswerFromItem(t *testing.T) {
	const head = `{"Sequence":90,"RecordedAt":"2026-10-05T15:21:19Z","Kind":"model_response","Data":{"TurnID":"t1","Response":{"Stop":"complete","Output":[`
	for _, tc := range []struct {
		name, output, want string
	}{
		{"one message", `{"Type":"reasoning","Data":{"Summary":[]}},{"Type":"message","Data":{"Role":"assistant","Text":"{\"findings\":[]}","Phase":"final_answer"}}`, `{"findings":[]}`},
		{"commentary then the answer", `{"Type":"message","Data":{"Role":"assistant","Text":"still stalled","Phase":"commentary"}},{"Type":"message","Data":{"Role":"assistant","Text":"the review","Phase":"final_answer"}}`, "the review"},
		{"no phase: the last", `{"Type":"message","Data":{"Role":"assistant","Text":"a"}},{"Type":"message","Data":{"Role":"assistant","Text":"b"}}`, "b"},
		{"a tool call", `{"Type":"message","Data":{"Role":"assistant","Text":"checking","Phase":"commentary"}},{"Type":"tool_call","Data":{"CallID":"c","Name":"Bash","Arguments":"{}"}}`, ""},
		{"no text", `{"Type":"reasoning","Data":{"Summary":["thinking"]}}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, ok := engine.AnswerFromItem([]byte(head + tc.output + `]}}}`))
			assert.Equal(t, tc.want != "", ok)
			assert.Equal(t, tc.want, a.Text)
			if ok {
				assert.Equal(t, "t1", a.TurnID)
			}
		})
	}
	_, ok := engine.AnswerFromItem([]byte(`{"Kind":"tool_call_status","Data":{"CallID":"c"}}`))
	assert.False(t, ok)
}
