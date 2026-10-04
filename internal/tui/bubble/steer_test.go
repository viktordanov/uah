package bubble_test

import (
	"context"
	"slices"
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

// liveDeps opens sessions on the embedded engine, which takes messages
// into a live run, with fakellm answering; they stream, as the TUI's do.
func liveDeps(t *testing.T, llm *fakellm.Server) bubble.Deps {
	t.Helper()
	env := harnesstest.NewEnv(t)
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
			s, err := session.Open(ctx, eng, session.Options{ID: id, Settings: settings, Interactive: true, Stream: true})
			if err == nil {
				t.Cleanup(func() { _ = s.Close() }) // a run still writing would keep the temporary folder from going
			}

			return s, nil, err
		},
		Sessions: func() ([]session.Info, error) { return nil, nil },
	}
}

// enhanced is the terminal's answer to the keyboard enhancement query when
// it tells shift+enter and ctrl+enter from enter (kitty, Ghostty, WezTerm);
// tmux and Terminal.app never answer. It changes no send key.
var enhanced = term.KeyboardEnhancementsMsg{Flags: 1}

// TestTUI_SendTheQueueNow: while the model thinks, tab queues two messages;
// enter (or ctrl+enter) on the empty composer gives both to the working
// agent, in order, before its next model request.
func TestTUI_SendTheQueueNow(t *testing.T) {
	for _, tc := range []struct {
		name    string
		sendNow term.KeyPressMsg
	}{
		{"enter", term.KeyPressMsg{Code: term.KeyEnter}},
		{"ctrl+enter", term.KeyPressMsg{Code: term.KeyEnter, Mod: term.ModCtrl}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gate := make(chan struct{})
			llm := fakellm.New(t, fakellm.Reply{Text: "never shown", Gate: gate})
			t.Cleanup(func() { close(gate) }) // before the server closes
			d := start(t, liveDeps(t, llm))
			d.until("the session is open", func() bool { return d.m.(bubble.Model).Exit().SessionID != "" })

			d.typeText("start")
			d.key(term.KeyEnter, 0)
			d.until("the model thinking", func() bool { return len(llm.Requests()) == 1 })
			d.typeText("first queued")
			d.key(term.KeyTab, 0)
			d.typeText("second queued")
			d.key(term.KeyTab, 0)
			d.waitFor("↳ queued: second queued")
			assert.Contains(t, d.view(), "↳ queued: first queued")
			assert.Contains(t, d.view(), "enter sends now", "the queue's hint")

			d.send(tc.sendNow)
			d.waitFor("• done")
			d.waitIdle()

			v := d.view()
			assert.NotContains(t, v, "↳ queued:", "the queue went out")
			first, second := strings.Index(v, "λ first queued"), strings.Index(v, "λ second queued")
			require.GreaterOrEqual(t, first, 0)
			assert.Greater(t, second, first, "in their order")
			reqs := llm.Requests()
			last := reqs[len(reqs)-1].UserTexts
			i := slices.Index(last, "first queued")
			require.GreaterOrEqual(t, i, 0, "the model got the queue: %q", last)
			assert.Equal(t, []string{"start", "first queued", "second queued"}, last[i-1:i+2])
			assert.NotContains(t, v, "never shown", "the held request was canceled for the new messages")
		})
	}
}

// TestTUI_SessionCallsKeepTheKeyOrder: the commands of two tab presses run
// in their own goroutines, and the second may run first, as here; the
// session still queues, and enter still sends, the messages in the order
// typed (Model.calls).
func TestTUI_SessionCallsKeepTheKeyOrder(t *testing.T) {
	gate := make(chan struct{})
	llm := fakellm.New(t, fakellm.Reply{Text: "never shown", Gate: gate})
	t.Cleanup(func() { close(gate) }) // before the server closes
	d := start(t, liveDeps(t, llm))
	d.until("the session is open", func() bool { return d.m.(bubble.Model).Exit().SessionID != "" })
	d.typeText("start")
	d.key(term.KeyEnter, 0)
	d.until("the model thinking", func() bool { return len(llm.Requests()) == 1 })

	d.typeText("first queued")
	first := d.update(term.KeyPressMsg{Code: term.KeyTab})
	d.typeText("second queued")
	second := d.update(term.KeyPressMsg{Code: term.KeyTab})
	d.execNow(second)
	time.Sleep(50 * time.Millisecond) // without the chain, the second Send lands first
	d.execNow(first)
	d.waitFor("↳ queued: first queued")
	d.waitFor("↳ queued: second queued")
	v := d.view()
	assert.Less(t, strings.Index(v, "↳ queued: first queued"), strings.Index(v, "↳ queued: second queued"), "queued in order")

	d.key(term.KeyEnter, 0)
	d.waitFor("• done")
	d.waitIdle()
	reqs := llm.Requests()
	last := reqs[len(reqs)-1].UserTexts
	i := slices.Index(last, "first queued")
	require.GreaterOrEqual(t, i, 1, "the model got the queue: %q", last)
	assert.Equal(t, "start", last[i-1], "%q", last)
	assert.Equal(t, i+1, slices.Index(last, "second queued"), "the order typed: %q", last)
}

// TestTUI_EnterWaitsForTheToolCallCtrlEnterCutsIn: while the model
// streams a response, enter holds the message, shown as queued, until the
// response and its tool call are done, so it rides the request after the
// tool's output; ctrl+enter (alt+enter where the terminal cannot tell it
// from enter) drops the response under way, and the next request has the
// message at once.
func TestTUI_EnterWaitsForTheToolCallCtrlEnterCutsIn(t *testing.T) {
	patch := fakellm.Call{Name: "apply_patch", Args: "*** Begin Patch\n*** Add File: a.go\n+package a\n*** End Patch\n", Custom: true}
	for _, tc := range []struct {
		name string
		mod  term.KeyMod
	}{
		{"enter", 0},
		{"ctrl+enter", term.ModCtrl},
		{"alt+enter", term.ModAlt},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hold := make(chan struct{})
			var release sync.Once
			llm := fakellm.New(t, fakellm.Reply{Deltas: []string{"Reading the code first"}, Hold: hold, Calls: []fakellm.Call{patch}})
			t.Cleanup(func() { release.Do(func() { close(hold) }) }) // before the server closes
			d := start(t, liveDeps(t, llm))
			d.until("the session is open", func() bool { return d.m.(bubble.Model).Exit().SessionID != "" })

			d.typeText("start")
			d.key(term.KeyEnter, 0)
			d.waitFor("Reading the code first")
			d.waitFor("enter after tool · alt+enter now · tab later")
			d.typeText("look here")
			d.key(term.KeyEnter, tc.mod)
			if tc.mod != 0 {
				d.until("the request with the message", func() bool { return len(llm.Requests()) == 2 })
				next := llm.Requests()[1]
				assert.Contains(t, next.UserTexts, "look here", "at once")
				assert.Empty(t, next.ToolOutputs, "the response was dropped before its tool call")
				d.waitIdle()

				return
			}
			d.waitFor("↳ after tool: look here") // held, and labelled so
			d.pump(300 * time.Millisecond)
			assert.Len(t, llm.Requests(), 1, "the response is not cut off")

			release.Do(func() { close(hold) })
			d.until("the request with the message", func() bool {
				return slices.ContainsFunc(llm.Requests(), func(r fakellm.Request) bool { return slices.Contains(r.UserTexts, "look here") })
			})
			d.waitIdle()
			reqs := llm.Requests()
			i := slices.IndexFunc(reqs, func(r fakellm.Request) bool { return slices.Contains(r.UserTexts, "look here") })
			assert.NotEmpty(t, reqs[i].ToolOutputs, "after the tool call's output")
			assert.Contains(t, d.view(), "λ look here")
		})
	}
}

// TestTUI_TabWaitsForTheRunsEnd: a tab-queued message waits for the run's
// end while a ctrl+enter one reaches the next request, as in Codex. The
// terminal's keyboard answer names the send-now key and the new-line key.
func TestTUI_TabWaitsForTheRunsEnd(t *testing.T) {
	for _, tc := range []struct {
		name    string
		report  term.Msg
		newline string
		hint    string
	}{
		{"no answer, as tmux", nil, "ctrl+j new line", "enter after tool · alt+enter now · tab later"},
		{"enhanced", enhanced, "shift+enter new line", "enter after tool · ^enter now · tab later"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gate := make(chan struct{})
			llm := fakellm.New(t, fakellm.Reply{Text: "never shown", Gate: gate})
			t.Cleanup(func() { close(gate) })
			deps := liveDeps(t, llm)
			deps.Details = true // the idle footer names the new-line key
			d := start(t, deps)
			d.send(term.WindowSizeMsg{Width: 140, Height: 30}) // the detailed footer's hint fits
			if tc.report != nil {
				d.send(tc.report)
			}
			d.until("the session is open", func() bool { return d.m.(bubble.Model).Exit().SessionID != "" })
			d.waitFor(tc.newline)

			d.typeText("start")
			d.key(term.KeyEnter, 0)
			d.until("the model thinking", func() bool { return len(llm.Requests()) == 1 })
			d.waitFor(tc.hint) // the footer while the agent works
			d.typeText("after the run")
			d.key(term.KeyTab, 0)
			d.waitFor("1. after the run") // the detailed view lists the queue
			d.typeText("look here first")
			d.key(term.KeyEnter, term.ModCtrl)
			d.until("the steered request", func() bool {
				reqs := llm.Requests()

				return len(reqs) > 1 && slices.Contains(reqs[1].UserTexts, "look here first")
			})
			assert.NotContains(t, llm.Requests()[1].UserTexts, "after the run", "the queued message waits for the run's end")
			d.waitFor("λ after the run")
			d.waitIdle()
			reqs := llm.Requests()
			assert.Contains(t, reqs[len(reqs)-1].UserTexts, "after the run", "then the queue goes out")
		})
	}
}
