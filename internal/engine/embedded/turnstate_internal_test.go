package embedded

import (
	"encoding/base64"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/viktordanov/uah-core/harness/llm"

	"github.com/viktordanov/uah/testing/fakellm"
)

// TestTurnState_SharedByTheRunsClients: a run's clients share its turn
// state, so a request on the priority client, after /fast, carries the
// token the other client's answer brought.
func TestTurnState_SharedByTheRunsClients(t *testing.T) {
	srv := fakellm.New(t, fakellm.Reply{Text: "a", TurnState: "ts-1"}, fakellm.Reply{Text: "b", TurnState: "ts-2"})
	claims := `{"exp":` + strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10) + `,"https://api.openai.com/auth":{"chatgpt_account_id":"acct-1"}}`
	env := map[string]string{"OPENAI_CODEX_ACCESS_TOKEN": "x." + base64.RawURLEncoding.EncodeToString([]byte(claims)) + ".y"}
	state := new(atomic.Pointer[string])
	for _, priority := range []bool{false, true} {
		c, err := codexClient(ClientConfig{BaseURL: srv.URL, MaxAttempts: 1, Priority: priority, Getenv: func(k string) string { return env[k] }, turnState: state})
		require.NoError(t, err)
		t.Cleanup(func() { _ = c.Close() })
		_, err = c.Respond(t.Context(), llm.Request{
			Model: llm.Model{ID: "m"},
			Input: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleUser, Text: "hi"}}},
		}, llm.RequestOptions{})
		require.NoError(t, err)
	}
	reqs := srv.Requests()
	require.Len(t, reqs, 2)
	assert.Equal(t, [2]string{"", "ts-1"}, [2]string{reqs[0].TurnState, reqs[1].TurnState})
}
