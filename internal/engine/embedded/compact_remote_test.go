package embedded_test

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/compaction"
	"github.com/viktordanov/uah/internal/engine/embedded"
	"github.com/viktordanov/uah/testing/fakellm"
)

func (e *env) remoteCompacting() *embedded.Engine {
	return embedded.New(embedded.Config{StateDir: e.StateDir, Provider: "openai", Getenv: e.getenv, Compaction: compaction.Settings{Remote: true}})
}

// inputTypes are the types of a request's input items, in order.
func inputTypes(t *testing.T, r fakellm.Request) []string {
	t.Helper()
	var out []string
	for _, raw := range r.Input {
		var head struct {
			Type string `json:"type"`
		}
		require.NoError(t, json.Unmarshal(raw, &head))
		out = append(out, head.Type)
	}

	return out
}

func TestEmbedded_RemoteCompactionSendsTheProvidersItem(t *testing.T) {
	e := newEnv(t,
		fakellm.Reply{Commands: []string{"cat notes.md; echo boom >&2; exit 2"}},
		fakellm.Reply{Text: "answer one"},
		fakellm.Reply{Compaction: "ENCRYPTED-1"},
		fakellm.Reply{Text: "answer two"},
		fakellm.Reply{Text: "answer three"},
	)
	s, ev := e.open(t, e.remoteCompacting(), "")
	ask(t, s, ev, "first")
	require.NoError(t, s.Compact())
	ask(t, s, ev, "second")

	reqs := e.llm.Requests()
	require.Len(t, reqs, 4)
	compact := reqs[2]
	types := inputTypes(t, compact)
	assert.Equal(t, "compaction_trigger", types[len(types)-1], "Codex's trigger ends the history")
	assert.Equal(t, `{"type":"compaction_trigger"}`, string(compact.Input[len(compact.Input)-1]))
	assert.Equal(t, []string{"call-1-0"}, compact.CallIDs, "the history as the model saw it")
	assert.Equal(t, reqs[1].ToolNames, compact.ToolNames, "the turn's tools, as Codex sends them")
	assert.Equal(t, []string{"first"}, compact.UserTexts, "no summary prompt")

	next := reqs[3]
	assert.Empty(t, next.CallIDs, "the item replaces the history")
	assert.Contains(t, inputTypes(t, next), "compaction")
	assert.Contains(t, string(next.Input[1]), "first")
	assert.JSONEq(t, `{"id":"cmp-3","type":"compaction","status":"","encrypted_content":"ENCRYPTED-1"}`, string(next.Input[2]), "the item as the provider sent it")
	require.Len(t, next.UserTexts, 3, "the kept message, the ledger, the new message")
	assert.Equal(t, "first", next.UserTexts[0])
	assert.Contains(t, next.UserTexts[1], "<uah_state_ledger>")
	assert.Contains(t, next.UserTexts[1], "exit 2")
	assert.Equal(t, "second", next.UserTexts[2])
	for _, text := range next.UserTexts {
		assert.NotContains(t, text, "uah-remote-compaction", "the placeholder never reaches the provider")
	}

	_, done := compactions(ev.all)
	require.Len(t, done, 1)
	require.NotNil(t, done[0].Stats)
	assert.Equal(t, compaction.StrategyRemote, done[0].Stats.Strategy)
	assert.Equal(t, int64(10), done[0].Stats.SummaryTokens, "the item's size is the call's output")
	assert.Positive(t, done[0].Stats.Call.Input)

	// A resumed session sends the saved item.
	id := s.ID()
	require.NoError(t, s.Close())
	s2, ev2 := e.open(t, e.remoteCompacting(), id)
	ask(t, s2, ev2, "third")
	reqs = e.llm.Requests()
	require.Len(t, reqs, 5)
	assert.Equal(t, string(next.Input[2]), string(reqs[4].Input[2]))
	records, _, err := compaction.OpenLog(filepath.Join(e.StateDir, "sessions"), id).Records()
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.JSONEq(t, `{"id":"cmp-3","type":"compaction","status":"","encrypted_content":"ENCRYPTED-1"}`, string(records[0].Remote))
}

func TestEmbedded_RemoteCompactionFallsBackToASummary(t *testing.T) {
	e := newEnv(t,
		fakellm.Reply{Commands: []string{"echo one"}},
		fakellm.Reply{Text: "answer one"},
		fakellm.Reply{Text: "no item"},
		fakellm.Reply{Text: "SUMMARY"},
		fakellm.Reply{Text: "answer two"},
	)
	s, ev := e.open(t, e.remoteCompacting(), "")
	ask(t, s, ev, "first")
	require.NoError(t, s.Compact())
	ask(t, s, ev, "second")

	reqs := e.llm.Requests()
	require.Len(t, reqs, 5)
	assert.Contains(t, inputTypes(t, reqs[2]), "compaction_trigger")
	assert.Equal(t, []string{"first", compaction.Prompt}, reqs[3].UserTexts, "the local summary")
	assert.Equal(t, []string{"first", summaryText("SUMMARY"), "second"}, reqs[4].UserTexts)
	_, done := compactions(ev.all)
	require.Len(t, done, 1)
	assert.Empty(t, done[0].Err)
	assert.Equal(t, compaction.StrategyLocal, done[0].Stats.Strategy)
}

func TestEmbedded_ACompactFocusUsesTheSummary(t *testing.T) {
	e := newEnv(t,
		fakellm.Reply{Commands: []string{"echo one"}},
		fakellm.Reply{Text: "answer one"},
		fakellm.Reply{Text: "SUMMARY"},
		fakellm.Reply{Text: "answer two"},
	)
	s, ev := e.open(t, e.remoteCompacting(), "")
	ask(t, s, ev, "first")
	require.NoError(t, s.CompactWith("the failing test"))
	ask(t, s, ev, "second")

	reqs := e.llm.Requests()
	require.Len(t, reqs, 4)
	assert.NotContains(t, inputTypes(t, reqs[2]), "compaction_trigger", "only a prompt can take a focus")
	assert.True(t, strings.HasSuffix(reqs[2].UserTexts[1], "the failing test"))
}

// TestEmbedded_RemoteCompactionOverTheWindowSummarizesLocally: the provider
// would refuse a history over the window whole, so it goes to the local
// summary, which drops the oldest items to fit, with no remote call.
func TestEmbedded_RemoteCompactionOverTheWindowSummarizesLocally(t *testing.T) {
	e := newEnv(t,
		fakellm.Reply{Commands: []string{"head -c 40000 /dev/zero | tr '\\0' x"}},
		fakellm.Reply{Text: "answer one"},
		fakellm.Reply{Text: "SUMMARY"},
		fakellm.Reply{Text: "answer two"},
	)
	s, ev := e.open(t, e.embedded(), "")
	ask(t, s, ev, "first")
	id := s.ID()
	require.NoError(t, s.Close())

	eng := embedded.New(embedded.Config{StateDir: e.StateDir, Provider: "openai", Getenv: e.getenv, ContextWindow: 8_000, Compaction: compaction.Settings{Remote: true}})
	s2, ev2 := e.open(t, eng, id)
	require.NoError(t, s2.Compact())
	ask(t, s2, ev2, "second")

	reqs := e.llm.Requests()
	require.Len(t, reqs, 4)
	assert.NotContains(t, inputTypes(t, reqs[2]), "compaction_trigger", "no remote call")
	assert.Contains(t, reqs[2].UserTexts, compaction.Prompt)
	assert.Empty(t, reqs[2].ToolOutputs, "the oldest items, with the long output, were dropped to fit")
	_, done := compactions(ev2.all)
	require.Len(t, done, 1)
	require.NotNil(t, done[0].Stats)
	assert.Equal(t, compaction.StrategyLocal, done[0].Stats.Strategy)
}
