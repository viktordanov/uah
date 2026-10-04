package bubble_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/engine/embedded"
	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/internal/tui/bubble"
	"github.com/viktordanov/uah/internal/tui/term"
	"github.com/viktordanov/uah/testing/fakellm"
	"github.com/viktordanov/uah/testing/harnesstest"
)

// rewindDeps opens sessions on the embedded engine with a scripted model.
func rewindDeps(t *testing.T, replies ...fakellm.Reply) (bubble.Deps, *fakellm.Server) {
	t.Helper()
	env := harnesstest.NewEnv(t)
	llm := fakellm.New(t, replies...)
	getenv := func(key string) string {
		if key == "OPENAI_API_KEY" {
			return "test-key"
		}

		return env.Getenv(key)
	}
	eng := embedded.New(embedded.Config{StateDir: env.StateDir, Provider: "openai", Getenv: getenv})
	settings := session.Settings{Provider: "openai", Model: "gpt-test", Effort: "high", Workspace: env.Workspace, BaseURL: llm.URL}

	return bubble.Deps{
		Open: func(ctx context.Context, id string) (*session.Session, []session.LoadedRun, error) {
			s, err := session.Open(ctx, eng, session.Options{ID: id, Settings: settings, Interactive: true})
			if err == nil {
				t.Cleanup(func() { _ = s.Close() }) // a run still writing would keep the temporary folder from going
			}

			return s, nil, err
		},
		Sessions: func() ([]session.Info, error) { return nil, nil },
	}, llm
}

// TestTUI_RewindToAnEarlierMessage: esc esc on an empty composer selects
// the latest message, esc steps back, enter puts the message in the
// composer and cuts the transcript, and the edited message continues from
// there with nothing of the old branch in the model's request.
func TestTUI_RewindToAnEarlierMessage(t *testing.T) {
	deps, llm := rewindDeps(t,
		fakellm.Reply{Text: "answer one"},
		fakellm.Reply{Text: "answer two"},
		fakellm.Reply{Text: "answer three"},
		fakellm.Reply{Text: "answer two, again"},
	)
	d := start(t, deps)
	for i, text := range []string{"first", "second", "third"} {
		d.typeText(text)
		d.key(term.KeyEnter, 0)
		d.waitFor("answer " + []string{"one", "two", "three"}[i])
		d.waitIdle()
	}

	d.key(term.KeyEscape, 0)
	d.waitFor("esc again to edit a previous message")
	d.key(term.KeyEscape, 0)
	d.waitFor("▶ third")
	d.key(term.KeyEscape, 0)
	d.waitFor("▶ second")
	d.key(term.KeyEnter, 0)
	d.until("the cut", func() bool { return !strings.Contains(d.view(), "answer two") })
	view := d.view()
	assert.Contains(t, view, "answer one")
	assert.NotContains(t, view, "answer three")
	assert.NotContains(t, view, "▶")

	d.typeText(", edited")
	d.key(term.KeyEnter, 0)
	d.waitFor("answer two, again")
	reqs := llm.Requests()
	require.Len(t, reqs, 4)
	assert.Equal(t, []string{"first", "second, edited"}, reqs[3].UserTexts)
}

// TestTUI_AnyOtherKeyCancelsGoingBack: a typed character leaves the
// selection and goes into the composer; nothing is cut.
func TestTUI_AnyOtherKeyCancelsGoingBack(t *testing.T) {
	deps, _ := rewindDeps(t, fakellm.Reply{Text: "answer one"})
	d := start(t, deps)
	d.typeText("first")
	d.key(term.KeyEnter, 0)
	d.waitFor("answer one")
	d.waitIdle()

	d.key(term.KeyEscape, 0)
	d.key(term.KeyEscape, 0)
	d.waitFor("▶ first")
	d.typeText("x")
	d.until("the selection to end", func() bool { return !strings.Contains(d.view(), "▶") })
	assert.Contains(t, d.view(), "answer one")
	assert.Contains(t, d.view(), "λ x")
}

// TestTUI_EscEscAfterIdling: esc esc selects your message after the
// session sat idle longer than the two seconds a first esc waits, while
// the clock did not tick: a key sets the clock first.
func TestTUI_EscEscAfterIdling(t *testing.T) {
	deps, _ := rewindDeps(t, fakellm.Reply{Text: "answer one"})
	clock := &testClock{at: time.Now()}
	deps.Now = clock.now
	d := start(t, deps)
	d.typeText("first")
	d.key(term.KeyEnter, 0)
	d.waitFor("answer one")
	d.waitIdle()

	clock.add(time.Minute)
	d.key(term.KeyEscape, 0)
	d.waitFor("esc again to edit a previous message")
	clock.add(500 * time.Millisecond)
	d.pump(300 * time.Millisecond) // the tick the hint starts
	assert.Contains(t, d.view(), "esc again to edit a previous message", "still primed half a second later")
	d.key(term.KeyEscape, 0)
	d.waitFor("▶ first")
}

// testClock is a clock a test moves.
type testClock struct {
	mu sync.Mutex
	at time.Time
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.at
}

func (c *testClock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}
