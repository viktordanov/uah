package embedded_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/testing/fakellm"
)

// send sends a message, waits for the session to be idle again, and
// returns the message's ID.
func send(t *testing.T, s *session.Session, ev *events, text string) string {
	t.Helper()
	in, err := s.Submit(text)
	require.NoError(t, err)
	ev.finished()
	ev.idle()

	return in.ID
}

func rewound(ev *events) []engine.Rewound {
	var out []engine.Rewound
	for _, e := range ev.all {
		if r, ok := e.(engine.Rewound); ok {
			out = append(out, r)
		}
	}

	return out
}

func TestEmbedded_RewindCutsTheContextAndSurvivesResume(t *testing.T) {
	e := newEnv(t,
		fakellm.Reply{Text: "answer one", InputTokens: 40},
		fakellm.Reply{Commands: []string{"echo old branch"}},
		fakellm.Reply{Text: "answer two"},
		fakellm.Reply{Text: "answer two, again"},
		fakellm.Reply{Text: "answer three"},
	)
	eng := e.embedded()
	s, ev := e.open(t, eng, "")
	send(t, s, ev, "first")
	second := send(t, s, ev, "second, the old branch")
	before, ok := eng.ContextUsage(s.ID())
	require.True(t, ok)

	require.NoError(t, s.Rewind(second))
	cut := ev.until("Rewound", isA[engine.Rewound]).(engine.Rewound)
	assert.Equal(t, second, cut.MessageID)
	assert.Positive(t, cut.Tokens, "the last response before the message")
	after, ok := eng.ContextUsage(s.ID())
	require.True(t, ok, "/context shows the request before the message")
	assert.Less(t, after.Used, before.Used)
	assert.Equal(t, cut.Tokens, after.Used)
	send(t, s, ev, "second, edited")

	reqs := e.llm.Requests()
	require.Len(t, reqs, 4)
	last := reqs[3]
	assert.Equal(t, []string{"first", "second, edited"}, last.UserTexts, "nothing from the old branch")
	assert.Empty(t, last.CallIDs)
	assert.Empty(t, last.ToolOutputs)

	// The session file keeps the old branch.
	id := s.ID()
	raw, err := os.ReadFile(filepath.Join(e.StateDir, "sessions", id+".session.jsonl"))
	require.NoError(t, err)
	assert.Contains(t, string(raw), "second, the old branch")
	assert.Contains(t, string(raw), "echo old branch")

	// A resumed session keeps the cut, and its reloaded transcript has it.
	require.NoError(t, s.Close())
	runs, err := session.Load(e.StateDir, id)
	require.NoError(t, err)
	var loaded []engine.Rewound
	for _, r := range runs {
		for _, x := range r.Events {
			if v, ok := x.(engine.Rewound); ok {
				loaded = append(loaded, v)
			}
		}
	}
	require.Len(t, loaded, 1)
	assert.Equal(t, second, loaded[0].MessageID)
	assert.Equal(t, cut.Tokens, loaded[0].Tokens)

	s2, ev2 := e.open(t, e.embedded(), id)
	send(t, s2, ev2, "third")
	reqs = e.llm.Requests()
	assert.Equal(t, []string{"first", "second, edited", "third"}, reqs[len(reqs)-1].UserTexts)
}

func TestEmbedded_RewindPastACompactionUsesTheOneBefore(t *testing.T) {
	e := newEnv(t,
		fakellm.Reply{Text: "answer one"},
		fakellm.Reply{Text: "answer two"},
		fakellm.Reply{Text: "SUMMARY"},
		fakellm.Reply{Text: "answer three"},
		fakellm.Reply{Text: "answer two, again"},
	)
	s, ev := e.open(t, e.compacting(0), "")
	send(t, s, ev, "first")
	second := send(t, s, ev, "second")
	require.NoError(t, s.Compact())
	send(t, s, ev, "third")
	reqs := e.llm.Requests()
	require.Len(t, reqs, 4)
	assert.Equal(t, []string{"first", "second", summaryText("SUMMARY"), "third"}, reqs[3].UserTexts)

	// The compaction covered the message; going back before it leaves the
	// history uncompacted, with no mismatch reported.
	require.NoError(t, s.Rewind(second))
	send(t, s, ev, "second, edited")
	reqs = e.llm.Requests()
	assert.Equal(t, []string{"first", "second, edited"}, reqs[len(reqs)-1].UserTexts)
	_, done := compactions(ev.all)
	for _, d := range done {
		assert.Empty(t, d.Err)
	}
}

func TestEmbedded_RewindAfterAClearKeepsTheClear(t *testing.T) {
	e := newEnv(t,
		fakellm.Reply{Text: "answer one"},
		fakellm.Reply{Text: "answer two"},
		fakellm.Reply{Text: "answer two, again"},
	)
	s, ev := e.open(t, e.compacting(0), "")
	send(t, s, ev, "first")
	require.NoError(t, s.Clear())
	second := send(t, s, ev, "second")

	require.NoError(t, s.Rewind(second))
	send(t, s, ev, "second, edited")
	reqs := e.llm.Requests()
	assert.Equal(t, []string{"second, edited"}, reqs[len(reqs)-1].UserTexts)
	_, done := compactions(ev.all)
	for _, d := range done {
		assert.Empty(t, d.Err)
	}
}

func TestEmbedded_RewindRefusesUnknownMessagesAndKeepsTheContext(t *testing.T) {
	e := newEnv(t,
		fakellm.Reply{Text: "answer one"},
		fakellm.Reply{Text: "answer two"},
	)
	s, ev := e.open(t, e.embedded(), "")
	send(t, s, ev, "first")
	require.Error(t, s.Rewind("no-such-message"))
	send(t, s, ev, "second")
	reqs := e.llm.Requests()
	assert.Equal(t, []string{"first", "second"}, reqs[1].UserTexts)
	assert.Empty(t, rewound(ev))
}

func TestEmbedded_RewindSendsWhatWentWithTheMessageAgain(t *testing.T) {
	e := newEnv(t,
		fakellm.Reply{Text: "answer one"},
		fakellm.Reply{Text: "noted"},
		fakellm.Reply{Text: "answer two"},
		fakellm.Reply{Text: "answer two, again"},
	)
	s, ev := e.open(t, e.embedded(), "")
	send(t, s, ev, "first")
	s.Inject("a note for the agent") // the idle session starts a run for it
	ev.finished()
	ev.idle()
	second := send(t, s, ev, "second")
	reqs := e.llm.Requests()
	require.Equal(t, []string{"first", "a note for the agent", "second"}, reqs[2].UserTexts)

	require.NoError(t, s.Rewind(second))
	send(t, s, ev, "second, edited")
	reqs = e.llm.Requests()
	assert.Equal(t, []string{"first", "a note for the agent", "second, edited"}, reqs[len(reqs)-1].UserTexts)
}
