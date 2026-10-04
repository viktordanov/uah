package bubble_test

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/viktordanov/uah/internal/tui/bubble"
	"github.com/viktordanov/uah/internal/tui/term"
	"github.com/viktordanov/uah/testing/fakellm"
)

// TestTUI_StatusLineFollowsTheModel: on the embedded engine, the status
// line reads the engine's progress: waiting for a model that sends
// nothing, the patch it writes with its file, and thinking while its
// stream is held.
func TestTUI_StatusLineFollowsTheModel(t *testing.T) {
	t.Run("waiting", func(t *testing.T) {
		d := start(t, liveDeps(t, fakellm.New(t, fakellm.Reply{Freeze: true})))
		d.until("the session is open", func() bool { return d.m.(bubble.Model).Exit().SessionID != "" })
		d.typeText("hi")
		d.key(term.KeyEnter, 0)
		d.waitFor("Waiting for the model")
		d.key(term.KeyEscape, 0)
		d.key(term.KeyEscape, 0)
		d.waitIdle()
	})

	t.Run("writing a patch, then thinking", func(t *testing.T) {
		args := "*** Begin Patch\n*** Add File: a.go\n+package a\n*** End Patch\n"
		cut := strings.Index(args, "a.go\n") + len("a.go\n")
		hold := make(chan struct{})
		var release sync.Once
		llm := fakellm.New(t,
			fakellm.Reply{
				Calls:     []fakellm.Call{{Name: "apply_patch", Args: args, Custom: true}},
				ArgDeltas: []string{args[:20], args[20:cut], args[cut:]},
				Pace:      500 * time.Millisecond,
			},
			fakellm.Reply{Reasoning: []string{"Checking the file"}, Hold: hold},
		)
		t.Cleanup(func() { release.Do(func() { close(hold) }) }) // before the server closes
		d := start(t, liveDeps(t, llm))
		d.until("the session is open", func() bool { return d.m.(bubble.Model).Exit().SessionID != "" })
		d.typeText("add a.go")
		d.key(term.KeyEnter, 0)
		d.waitFor("Writing a patch · a.go ·")
		d.until("the patch's request answered", func() bool { return len(llm.Requests()) == 2 })
		d.waitFor("Thinking")
		release.Do(func() { close(hold) })
		d.waitIdle()
	})
}
