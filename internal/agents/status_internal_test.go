package agents

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/viktordanov/uah/internal/engine"
)

// TestReported points at the notification only for the status whose note
// went into the parent's live run: a reviewer, whose first message does
// not count, and a child with a newer message get their answer.
func TestReported(t *testing.T) {
	done := Status{State: engine.AgentCompleted, Message: "forty-two"}
	for _, tc := range []struct {
		name string
		c    child
		want string
	}{
		{"told", child{status: done, gen: 1, told: 1}, toldNote},
		{"not told", child{status: done, gen: 1}, "forty-two"},
		{"told an earlier answer", child{status: done, gen: 2, told: 1}, "forty-two"},
		{"a reviewer never messaged", child{status: done, review: true}, "forty-two"},
		{"a reviewer", child{status: done, review: true, gen: 1, told: 1}, "forty-two"},
		{"errored", child{status: Status{State: engine.AgentErrored, Message: "boom"}, gen: 1, told: 1}, "boom"},
	} {
		t.Run(tc.name, func(t *testing.T) { assert.Equal(t, tc.want, tc.c.reported().Message) })
	}
}
