package state_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uagent/core"
	uaharness "github.com/viktordanov/uagent/harness"

	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/images"
	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/internal/tui/state"
)

var imageRef = strings.Repeat("ab", 32) + ".png"

// talked is an idle embedded session with three messages and their
// answers; the second carries an image.
func talked(t *testing.T) state.State {
	t.Helper()
	s, _ := apply(state.New(t0), session.SessionOpened{At: t0, ID: "sess-1", Engine: "embedded", Settings: settings()})
	img := images.Image{Label: "[Image #1]", Ref: imageRef, Width: 1, Height: 1}
	texts := map[string]string{"m1": "first", "m2": images.Join("look [Image #1]", []images.Image{img}), "m3": "third"}
	for _, id := range []string{"m1", "m2", "m3"} {
		run := "run-" + id
		s, _ = apply(s,
			core.RunStarted{At: t0, RunID: run, SessionID: "sess-1"},
			core.UserMessage{At: t0, ID: id, Text: texts[id]},
			core.AssistantMessage{At: t0, Text: "answer " + id, Final: true},
			core.RunFinished{At: t0, Result: core.Result{Request: core.Request{RunID: run}, Status: core.StatusOK}},
			session.Idle{At: t0},
		)
	}

	return s
}

func selectedText(s state.State) string {
	it, _ := s.Item(s.Backtrack.Key)

	return it.Text
}

func TestBacktrack_EscEscSelectsTheLatestAndStepsBack(t *testing.T) {
	s := talked(t)
	s, _ = apply(s, state.Esc{Empty: true})
	assert.Nil(t, s.Backtrack, "the first esc primes")
	assert.Contains(t, s.Status, "esc again")

	s, _ = apply(s, state.Esc{Empty: true})
	require.NotNil(t, s.Backtrack)
	assert.Equal(t, "third", selectedText(s))

	s, _ = apply(s, state.Esc{Empty: true}, state.BacktrackMove{Delta: -1})
	assert.Equal(t, "first", selectedText(s), "esc and ↑ step back")
	s, _ = apply(s, state.BacktrackMove{Delta: -1})
	assert.Equal(t, "first", selectedText(s), "the first stays")
	s, _ = apply(s, state.BacktrackMove{Delta: 1})
	assert.Equal(t, "look [Image #1]", selectedText(s))
}

func TestBacktrack_SelectPutsTheMessageInTheComposerAndCutsOnRewound(t *testing.T) {
	s := talked(t)
	n := len(s.Items)
	s, _ = apply(s, state.Esc{Empty: true}, state.Esc{Empty: true}, state.Esc{Empty: true})
	s, effects := apply(s, state.BacktrackSelect{})
	require.Len(t, effects, 2)
	assert.Equal(t, state.EffRewind{ID: "m2"}, effects[0])
	assert.Equal(t, state.EffSetDraft{Text: "look [Image #1]"}, effects[1])
	require.Len(t, s.Attached, 1, "the image comes back with its placeholder")
	assert.Equal(t, imageRef, s.Attached[0].Ref)
	assert.Nil(t, s.Backtrack)
	assert.Len(t, s.Items, n, "the transcript waits for the session")

	s, _ = apply(s, engine.Rewound{At: t0, MessageID: "m2", Tokens: 1234})
	_, ok := s.Item("msg:m2")
	assert.False(t, ok, "the message and what followed leave")
	_, ok = s.Item("msg:m3")
	assert.False(t, ok)
	first, ok := s.Item("msg:m1")
	require.True(t, ok)
	assert.Equal(t, "first", first.Text)
	assert.Equal(t, int64(1234), s.ContextUsed)
	var answers []string
	for _, it := range s.Items {
		if it.Kind == state.KindAssistant {
			answers = append(answers, it.Text)
		}
	}
	assert.Equal(t, []string{"answer m1"}, answers)
}

func TestBacktrack_CancelChangesNothing(t *testing.T) {
	s := talked(t)
	before := len(s.Items)
	s, _ = apply(s, state.Esc{Empty: true}, state.Esc{Empty: true}, state.BacktrackMove{Delta: -1})
	s, effects := apply(s, state.BacktrackCancel{})
	assert.Empty(t, effects)
	assert.Nil(t, s.Backtrack)
	assert.Empty(t, s.Status)
	assert.Len(t, s.Items, before)
}

func TestBacktrack_NeedsAnIdleSessionAnEmptyComposerAndMessages(t *testing.T) {
	s := talked(t)
	s, _ = apply(s, state.Esc{}, state.Esc{})
	assert.Nil(t, s.Backtrack, "a draft in the composer")

	busy, _ := apply(talked(t), session.InputQueued{At: t0, Input: core.UserInput{ID: "m4", Text: "more"}})
	busy, effects := apply(busy, state.Esc{Empty: true}, state.Esc{Empty: true})
	assert.Nil(t, busy.Backtrack)
	assert.Equal(t, []state.Effect{state.EffInterrupt{}}, effects, "esc esc still interrupts a busy agent")

	reviewing, _ := apply(talked(t), session.ReviewStarted{At: t0, ID: "rev-1"})
	reviewing, effects = apply(reviewing, state.Esc{Empty: true}, state.Esc{Empty: true})
	assert.Nil(t, reviewing.Backtrack)
	assert.Equal(t, []state.Effect{state.EffInterrupt{}}, effects, "esc esc stops a running /review")

	empty, _ := apply(state.New(t0), session.SessionOpened{At: t0, ID: "sess-2", Engine: "embedded", Settings: settings()})
	empty, _ = apply(empty, state.Esc{Empty: true}, state.Esc{Empty: true})
	assert.Nil(t, empty.Backtrack)
	assert.Equal(t, "No previous message to edit.", empty.Items[len(empty.Items)-1].Text)
}

func TestBacktrack_SlashRewindSelectsAtOnceAndPrimingExpires(t *testing.T) {
	s, _ := apply(talked(t), state.Submit{Text: "/rewind"})
	require.NotNil(t, s.Backtrack)
	assert.Equal(t, "third", selectedText(s))

	s = talked(t)
	s, _ = apply(s, state.Esc{Empty: true}, state.Tick{Now: t0.Add(3 * time.Second)})
	s, _ = apply(s, state.Esc{Empty: true})
	assert.Nil(t, s.Backtrack, "a second esc after the window primes again")
}

func TestBacktrack_AResumedTranscriptEndsAtTheCut(t *testing.T) {
	res := func(run string) session.LoadedRun {
		return session.LoadedRun{Record: uaharness.RunRecord{Complete: true, Result: core.Result{
			Request: core.Request{RunID: run, SessionID: "sess-1"}, Status: core.StatusOK, StartedAt: t0, Wall: time.Second,
		}}}
	}
	one, two := res("run-1"), res("run-2")
	one.Events = []core.Event{
		core.UserMessage{At: t0, ID: "m1", Text: "first"},
		core.AssistantMessage{At: t0, Text: "answer one", Final: true},
		core.UserMessage{At: t0, ID: "m2", Text: "second"},
		core.AssistantMessage{At: t0, Text: "answer two", Final: true},
		engine.Rewound{At: t0, MessageID: "m2", Tokens: 50},
	}
	two.Events = []core.Event{
		core.UserMessage{At: t0, ID: "m3", Text: "second, edited"},
		core.AssistantMessage{At: t0, Text: "answer three", Final: true},
	}
	s, _ := apply(opened(), state.HistoryLoaded{SessionID: "sess-1", Runs: []session.LoadedRun{one, two}})
	var users []string
	for _, it := range s.Items {
		if it.Kind == state.KindUser {
			users = append(users, it.Text)
		}
	}
	assert.Equal(t, []string{"first", "second, edited"}, users)
}

// TestBacktrack_StreamingIsNeverCut: while an answer streams the session
// is busy, so esc esc interrupts instead of going back; once the run ends
// no streamed item is left, and a cut followed by a late reset keeps the
// transcript as cut.
func TestBacktrack_StreamingIsNeverCut(t *testing.T) {
	s, _ := apply(talked(t),
		session.InputQueued{At: t0, Input: core.UserInput{ID: "m4", Text: "fourth"}},
		session.InputSent{At: t0, IDs: []string{"m4"}},
		core.RunStarted{At: t0, RunID: "run-m4", SessionID: "sess-1"},
		core.UserMessage{At: t0, ID: "m4", Text: "fourth"},
		engine.TextDelta{At: t0, ItemID: "a4", Text: "answer in progr"},
	)
	s, effects := apply(s, state.Esc{Empty: true}, state.Esc{Empty: true})
	assert.Nil(t, s.Backtrack)
	assert.Equal(t, []state.Effect{state.EffInterrupt{}}, effects)

	s, _ = apply(s,
		core.RunFinished{At: t0, Result: core.Result{Request: core.Request{RunID: "run-m4"}, Status: core.StatusInterrupted}},
		session.Idle{At: t0},
	)
	for _, it := range s.Items {
		assert.False(t, it.Streaming, "the run's end drops a streamed item")
	}
	s, _ = apply(s, state.Esc{Empty: true}, state.Esc{Empty: true}, state.BacktrackSelect{})
	s, _ = apply(s, engine.Rewound{At: t0, MessageID: "m4"}, engine.StreamReset{At: t0})
	_, ok := s.Item("msg:m4")
	assert.False(t, ok)
	_, ok = s.Item("msg:m3")
	assert.True(t, ok, "the reset changes nothing after the cut")
}
