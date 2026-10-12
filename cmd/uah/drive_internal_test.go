package main

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/engine/embedded"
	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/testing/fakellm"
	"github.com/viktordanov/uah/testing/harnesstest"
)

// TestDriveWaitsForARunANotificationStarted pins that uah exec --stdin
// waits for a run a background shell's end started, also when stdin ends
// while it runs.
func TestDriveWaitsForARunANotificationStarted(t *testing.T) {
	gate := make(chan struct{})
	llm := fakellm.New(t,
		fakellm.Reply{Calls: []fakellm.Call{{Name: "Bash", Args: `{"command":"sleep 0.3","background":true}`}}},
		fakellm.Reply{Text: "started"},
		fakellm.Reply{Gate: gate, Text: "it finished"},
	)
	env := harnesstest.NewEnv(t)
	getenv := func(k string) string {
		if k == "OPENAI_API_KEY" {
			return "test-key"
		}

		return env.Getenv(k)
	}
	eng := embedded.New(embedded.Config{StateDir: env.StateDir, Provider: "openai", Getenv: getenv})
	s, err := session.Open(context.Background(), eng, session.Options{Settings: session.Settings{
		Provider: "openai", Model: "gpt-test", Effort: "high", Workspace: env.Workspace, BaseURL: llm.URL,
	}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	lines := make(chan stdinLine, 1)
	lines <- stdinLine{text: "start it"}
	go func() {
		for n := range llm.Seen() {
			if n == 3 { // the run the shell's end started asks the model
				close(lines)
				time.Sleep(200 * time.Millisecond) // stdin's end reaches drive first
				close(gate)

				return
			}
		}
	}()
	var stdout bytes.Buffer
	out := runOutput{stdout: &stdout}
	require.NoError(t, drive(context.Background(), s, lines, false, &out))
	require.NotNil(t, out.last)
	assert.Equal(t, "it finished", out.last.Answer)
}
