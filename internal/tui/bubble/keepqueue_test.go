package bubble_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/viktordanov/uah/internal/engine/embedded"
	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/internal/tui/bubble"
	"github.com/viktordanov/uah/internal/tui/term"
	"github.com/viktordanov/uah/testing/fakellm"
	"github.com/viktordanov/uah/testing/harnesstest"
)

// TestTUI_QueueSurvivesARestart: a message queued while the agent works
// is kept when uah quits; resuming the session shows it queued again, and
// enter on the empty composer sends it.
func TestTUI_QueueSurvivesARestart(t *testing.T) {
	gate := make(chan struct{})
	llm := fakellm.New(t, fakellm.Reply{Text: "never shown", Gate: gate})
	t.Cleanup(func() { close(gate) }) // before the server closes
	env := harnesstest.NewEnv(t)
	getenv := func(key string) string {
		if key == "OPENAI_API_KEY" {
			return "test-key"
		}

		return env.Getenv(key)
	}
	eng := embedded.New(embedded.Config{StateDir: env.StateDir, Provider: "openai", Getenv: getenv})
	settings := session.Settings{Provider: "openai", Model: "gpt-test", Effort: "high", Workspace: env.Workspace, BaseURL: llm.URL}
	deps := bubble.Deps{
		Open: func(ctx context.Context, id string) (*session.Session, []session.LoadedRun, error) {
			s, err := session.Open(ctx, eng, session.Options{
				ID: id, Resumed: id != "", Settings: settings, Interactive: true, Stream: true,
				SessionsDir: filepath.Join(env.StateDir, "sessions"), Source: session.SourceTUI,
			})

			return s, nil, err
		},
		Sessions: func() ([]session.Info, error) { return nil, nil },
	}

	d := start(t, deps)
	d.until("the session is open", func() bool { return d.m.(bubble.Model).Exit().SessionID != "" })
	d.typeText("start")
	d.key(term.KeyEnter, 0)
	<-llm.Seen()
	d.typeText("then update the README")
	d.key(term.KeyTab, 0)
	d.waitFor("↳ queued: then update the README")
	d.typeText("/quit")
	d.key(term.KeyEnter, 0)
	d.waitQuit()
	assert.Equal(t, 1, d.m.(bubble.Model).Exit().Queued, "the exit summary counts it")

	deps.SessionID = d.m.(bubble.Model).Exit().SessionID
	d = start(t, deps)
	d.waitFor("↳ queued: then update the README")
	d.waitIdle()
	seen := len(llm.Requests())

	d.key(term.KeyEnter, 0) // on the empty composer: the queue now
	d.waitFor("• done")
	assert.Greater(t, len(llm.Requests()), seen, "sent only now")
	assert.Contains(t, d.view(), "λ then update the README")
	assert.NotContains(t, d.view(), "queued: then update the README")
	d.key('c', term.ModCtrl)
	d.waitQuit()
}
