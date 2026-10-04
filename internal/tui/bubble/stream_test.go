package bubble_test

import (
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/viktordanov/uah/internal/tui/bubble"
	"github.com/viktordanov/uah/internal/tui/term"
	"github.com/viktordanov/uah/testing/fakellm"
)

// TestTUI_StreamsTheAnswer: the answer shows as the model writes it, while
// the run is still live, and the final message takes its place once.
func TestTUI_StreamsTheAnswer(t *testing.T) {
	hold := make(chan struct{})
	var release sync.Once
	llm := fakellm.New(t, fakellm.Reply{Deltas: []string{"Streaming ", "**answer**", " so far"}, Hold: hold})
	t.Cleanup(func() { release.Do(func() { close(hold) }) }) // before the server closes
	d := start(t, liveDeps(t, llm))
	d.until("the session is open", func() bool { return d.m.(bubble.Model).Exit().SessionID != "" })

	d.typeText("write something")
	d.key(term.KeyEnter, 0)
	d.waitFor("• Streaming answer so far")
	v := d.view()
	assert.Contains(t, v, "Writing", "the run is live and writing")
	assert.Contains(t, v, "esc to interrupt")

	release.Do(func() { close(hold) })
	d.waitIdle()
	assert.Equal(t, 1, strings.Count(d.view(), "Streaming answer so far"), "the final message replaced the streamed one")
}
