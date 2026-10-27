package embedded_test

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/compaction"
	"github.com/viktordanov/uah/internal/engine/codexauth"
	"github.com/viktordanov/uah/internal/engine/embedded"
	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/testing/fakellm"
)

// socketLogin is a codex login whose fake backend serves the Responses
// WebSocket when socket is set, and an engine that sends over it.
func socketLogin(t *testing.T, socket bool, replies ...fakellm.Reply) (*codexLogin, *embedded.Engine) {
	t.Helper()
	c := newCodexLogin(t, accessToken(time.Now().Add(24*time.Hour), 1), replies...)
	c.llm.WebSocket = socket
	eng := embedded.New(embedded.Config{StateDir: c.StateDir, Provider: codexauth.Provider, Getenv: c.getenv, ResponsesWebSocket: true})
	t.Cleanup(func() { _ = eng.Close() })

	return c, eng
}

func (c *codexLogin) open(t *testing.T, eng *embedded.Engine) (*session.Session, *events) {
	t.Helper()
	settings := session.Settings{Provider: codexauth.Provider, Model: "gpt-test", Effort: "high", Workspace: c.Workspace, BaseURL: c.llm.URL}
	s, err := session.Open(context.Background(), eng, session.Options{Settings: settings})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	return s, &events{t: t, s: s}
}

func (ev *events) send(t *testing.T, msg string) core.Result {
	t.Helper()
	_, err := ev.s.Submit(msg)
	require.NoError(t, err)
	result := ev.finished()
	ev.idle()

	return result
}

// attemptLines are the model attempts' diagnostics lines of every run.
func attemptLines(t *testing.T, c *codexLogin) []map[string]any {
	t.Helper()
	var lines []map[string]any
	for line := range strings.SplitSeq(c.stderrLogs(t), "\n") {
		var l map[string]any
		if json.Unmarshal([]byte(line), &l) == nil && l["diag"] == "model_attempt" {
			lines = append(lines, l)
		}
	}

	return lines
}

// TestEmbedded_CodexContinuesOverTheWebSocket: a session's turn requests
// go over one WebSocket, across its runs. The first sends the whole input;
// each later one continues the last response with only the items after
// it, and the server, which keeps the responses, sees the same requests.
// Each attempt's line says so.
func TestEmbedded_CodexContinuesOverTheWebSocket(t *testing.T) {
	c, eng := socketLogin(t, true,
		fakellm.Reply{Commands: []string{"echo one"}},
		fakellm.Reply{Text: "first"},
		fakellm.Reply{Text: "second"},
	)
	_, ev := c.open(t, eng)
	assert.Equal(t, core.StatusOK, ev.send(t, "one").Status)
	assert.Equal(t, core.StatusOK, ev.send(t, "two").Status)

	arrivals := c.llm.Arrivals()
	require.Len(t, arrivals, 3)
	var previous []string
	for _, a := range arrivals {
		assert.Equal(t, 1, a.Socket, "one connection")
		previous = append(previous, a.Previous)
	}
	assert.Equal(t, []string{"", "resp-1", "resp-2"}, previous)
	reqs := c.llm.Requests()
	assert.Less(t, arrivals[1].Items, len(reqs[1].Input))
	assert.Less(t, arrivals[2].Items, len(reqs[2].Input))
	assert.Equal(t, []string{"one", "two"}, reqs[2].UserTexts)
	assert.Equal(t, reqs[0].System, reqs[2].System)
	require.Len(t, reqs[1].ToolOutputs, 1)
	assert.Contains(t, reqs[1].ToolOutputs[0], "one")

	lines := attemptLines(t, c)
	require.Len(t, lines, 3)
	var sends []string
	for _, l := range lines {
		assert.Equal(t, "websocket", l["transport"])
		sends = append(sends, l["send"].(string)+" "+stringOf(l["send_reason"]))
	}
	assert.Equal(t, []string{"full new_connection", "incremental ", "incremental "}, sends)
	assert.Less(t, lines[2]["sent_bytes"], lines[2]["body_bytes"])
}

func stringOf(v any) string {
	s, _ := v.(string)

	return s
}

// TestEmbedded_CodexReplaysTheTurnStateOverTheWebSocket: the turn state
// comes in the event that stands for a response's headers and goes in
// each later request's client_metadata, within a run, as over HTTP.
func TestEmbedded_CodexReplaysTheTurnStateOverTheWebSocket(t *testing.T) {
	c, eng := socketLogin(t, true,
		fakellm.Reply{Commands: []string{"echo one"}, TurnState: "ts-1"},
		fakellm.Reply{Commands: []string{"echo two"}, TurnState: "ts-2"},
		fakellm.Reply{Text: "first", TurnState: "ts-3"},
		fakellm.Reply{Commands: []string{"echo three"}, TurnState: "ts-4"},
		fakellm.Reply{Text: "second"},
	)
	_, ev := c.open(t, eng)
	assert.Equal(t, core.StatusOK, ev.send(t, "one").Status)
	assert.Equal(t, core.StatusOK, ev.send(t, "two").Status)

	var states, previous []string
	for i, r := range c.llm.Requests() {
		states, previous = append(states, r.TurnState), append(previous, c.llm.Arrivals()[i].Previous)
	}
	assert.Equal(t, []string{"", "ts-1", "ts-1", "", "ts-4"}, states)
	assert.Equal(t, []string{"", "resp-1", "resp-2", "resp-3", "resp-4"}, previous, "a turn state does not end the continuation")
}

// TestEmbedded_CodexSendsWholeAfterACompaction: a remote compaction's
// request continues the last response with only the trigger; the request
// after it, whose history the provider's item replaced, sends its whole
// input on the same connection.
func TestEmbedded_CodexSendsWholeAfterACompaction(t *testing.T) {
	c := newCodexLogin(t, accessToken(time.Now().Add(24*time.Hour), 1),
		fakellm.Reply{Text: "answer one"}, fakellm.Reply{Compaction: "ENCRYPTED-1"}, fakellm.Reply{Text: "answer two"})
	c.llm.WebSocket = true
	eng := embedded.New(embedded.Config{StateDir: c.StateDir, Provider: codexauth.Provider, Getenv: c.getenv, ResponsesWebSocket: true, Compaction: compaction.Settings{Remote: true}})
	t.Cleanup(func() { _ = eng.Close() })
	s, ev := c.open(t, eng)
	assert.Equal(t, core.StatusOK, ev.send(t, "first").Status)
	require.NoError(t, s.Compact())
	assert.Equal(t, core.StatusOK, ev.send(t, "second").Status)

	arrivals, reqs := c.llm.Arrivals(), c.llm.Requests()
	require.Len(t, arrivals, 3)
	assert.Equal(t, [3]string{"", "resp-1", ""}, [3]string{arrivals[0].Previous, arrivals[1].Previous, arrivals[2].Previous})
	assert.Equal(t, 1, arrivals[1].Items, "only the trigger")
	assert.Equal(t, [3]int{1, 1, 1}, [3]int{arrivals[0].Socket, arrivals[1].Socket, arrivals[2].Socket})
	assert.Contains(t, inputTypes(t, reqs[2]), "compaction")
	lines := attemptLines(t, c)
	require.Len(t, lines, 3)
	assert.Equal(t, [2]any{"full", "input_changed"}, [2]any{lines[2]["send"], lines[2]["send_reason"]})
}

// TestEmbedded_CodexContinuesAfterAWebSearch: the search that the
// transport puts back into the next request is the response's own output
// item, so the request continues the response.
func TestEmbedded_CodexContinuesAfterAWebSearch(t *testing.T) {
	c, eng := socketLogin(t, true, fakellm.Reply{Searches: []fakellm.Search{{Query: "uah"}}, Text: "found it"}, fakellm.Reply{Text: "done"})
	_, ev := c.open(t, eng)
	assert.Equal(t, core.StatusOK, ev.send(t, "search").Status)
	assert.Equal(t, core.StatusOK, ev.send(t, "again").Status)

	arrivals := c.llm.Arrivals()
	require.Len(t, arrivals, 2)
	assert.Equal(t, "resp-1", arrivals[1].Previous)
	assert.Contains(t, inputTypes(t, c.llm.Requests()[1]), "web_search_call")
}

// TestEmbedded_CodexSessionsKeepTheirOwnWebSocket: two sessions on one
// engine, running at once, each continue their own responses on their own
// connection.
func TestEmbedded_CodexSessionsKeepTheirOwnWebSocket(t *testing.T) {
	c, eng := socketLogin(t, true)
	_, a := c.open(t, eng)
	_, b := c.open(t, eng)
	for _, msg := range []string{"first", "second"} {
		var wg sync.WaitGroup
		for _, ev := range []*events{a, b} {
			wg.Go(func() { assert.Equal(t, core.StatusOK, ev.send(t, msg).Status) })
		}
		wg.Wait()
	}

	arrivals := c.llm.Arrivals()
	require.Len(t, arrivals, 4)
	bySocket := map[int][]int{}
	for i, a := range arrivals {
		bySocket[a.Socket] = append(bySocket[a.Socket], i)
	}
	require.Len(t, bySocket, 2)
	for socket, reqs := range bySocket {
		require.Len(t, reqs, 2, "socket %d", socket)
		assert.Empty(t, arrivals[reqs[0]].Previous)
		assert.Equal(t, "resp-"+strconv.Itoa(reqs[0]+1), arrivals[reqs[1]].Previous, "socket %d continues its own", socket)
	}
}

// TestEmbedded_CodexClosedSessionClosesItsWebSocket: a session that closed
// leaves no connection: resumed, it starts on a new one.
func TestEmbedded_CodexClosedSessionClosesItsWebSocket(t *testing.T) {
	c, eng := socketLogin(t, true)
	s, ev := c.open(t, eng)
	assert.Equal(t, core.StatusOK, ev.send(t, "one").Status)
	require.NoError(t, s.Close())
	settings := session.Settings{Provider: codexauth.Provider, Model: "gpt-test", Effort: "high", Workspace: c.Workspace, BaseURL: c.llm.URL}
	resumed, err := session.Open(context.Background(), eng, session.Options{ID: s.ID(), Resumed: true, Settings: settings})
	require.NoError(t, err)
	t.Cleanup(func() { _ = resumed.Close() })
	assert.Equal(t, core.StatusOK, (&events{t: t, s: resumed}).send(t, "two").Status)

	arrivals := c.llm.Arrivals()
	require.Len(t, arrivals, 2)
	assert.Equal(t, [2]int{1, 2}, [2]int{arrivals[0].Socket, arrivals[1].Socket})
	assert.Empty(t, arrivals[1].Previous)
}

// TestEmbedded_CodexFallsBackToHTTP: a backend that refuses the upgrade
// gets every request of the session over HTTP, compressed.
func TestEmbedded_CodexFallsBackToHTTP(t *testing.T) {
	c, eng := socketLogin(t, false, fakellm.Reply{Commands: []string{"echo one"}}, fakellm.Reply{Text: "done"})
	_, ev := c.open(t, eng)
	assert.Equal(t, core.StatusOK, ev.send(t, "one").Status)

	for _, a := range c.llm.Arrivals() {
		assert.Zero(t, a.Socket)
		assert.Equal(t, "zstd", a.Encoding)
	}
	lines := attemptLines(t, c)
	require.Len(t, lines, 2)
	for _, l := range lines {
		assert.Equal(t, [3]any{"http", "full", "upgrade_required"}, [3]any{l["transport"], l["send"], l["send_reason"]})
	}
}

// TestEmbedded_CodexSendsWholeAfterAnInterruptedResponse: a run stopped
// while its response streams closes the connection; the next run's first
// request does not continue that response, it sends the whole input on a
// new connection.
func TestEmbedded_CodexSendsWholeAfterAnInterruptedResponse(t *testing.T) {
	hold := make(chan struct{})
	defer close(hold)
	c, eng := socketLogin(t, true, fakellm.Reply{Text: "first"}, fakellm.Reply{Deltas: []string{"part"}, Hold: hold}, fakellm.Reply{Text: "third"})
	s, ev := c.open(t, eng)
	assert.Equal(t, core.StatusOK, ev.send(t, "one").Status)
	_, err := s.Submit("two")
	require.NoError(t, err)
	waitSeen(t, c.llm, 2)
	require.NoError(t, s.Interrupt())
	assert.Equal(t, core.StatusInterrupted, ev.finished().Status)
	ev.idle()
	assert.Equal(t, core.StatusOK, ev.send(t, "three").Status)

	arrivals := c.llm.Arrivals()
	require.Len(t, arrivals, 3)
	assert.Equal(t, [3]string{"", "resp-1", ""}, [3]string{arrivals[0].Previous, arrivals[1].Previous, arrivals[2].Previous})
	assert.Equal(t, [3]int{1, 1, 2}, [3]int{arrivals[0].Socket, arrivals[1].Socket, arrivals[2].Socket})
	lines := attemptLines(t, c)
	require.Len(t, lines, 3)
	assert.Equal(t, [2]any{"full", "interrupted"}, [2]any{lines[2]["send"], lines[2]["send_reason"]})
}
