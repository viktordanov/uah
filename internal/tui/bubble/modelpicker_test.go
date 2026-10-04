package bubble_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/models"
	"github.com/viktordanov/uah/internal/tui/bubble"
	"github.com/viktordanov/uah/internal/tui/term"
	"github.com/viktordanov/uah/testing/fakellm"
)

// TestTUI_ModelPickerSetsModelAndEffort: /model lists the provider's
// models, enter on one lists its efforts with its default selected, and
// enter on an effort changes the live session's model and effort, which
// the next model request carries. esc goes back, then closes.
func TestTUI_ModelPickerSetsModelAndEffort(t *testing.T) {
	llm := fakellm.New(t, fakellm.Reply{Text: "first"}, fakellm.Reply{Text: "second"})
	d := liveDeps(t, llm)
	d.Models = func(context.Context, string) models.Catalog {
		return models.Catalog{Provider: "openai", Origin: models.OriginLive, Models: []models.Model{
			{ID: "gpt-test", ReasoningLevels: []string{"low", "medium", "high"}, DefaultEffort: "medium"},
			{ID: "gpt-deep", ReasoningLevels: []string{"low", "medium", "high", "xhigh"}, DefaultEffort: "low"},
		}}
	}
	dr := start(t, d)
	dr.until("the session is open", func() bool { return dr.m.(bubble.Model).Exit().SessionID != "" })

	dr.typeText("/model")
	dr.key(term.KeyEnter, 0) // the menu's /model opens the picker
	dr.waitFor("gpt-deep")
	assert.Contains(t, dr.view(), "› gpt-test (current)")
	assert.Empty(t, dr.m.(bubble.Model).Draft())

	dr.key(term.KeyDown, 0)
	dr.key(term.KeyEnter, 0)
	dr.waitFor("Select effort for gpt-deep")
	assert.Contains(t, dr.view(), "› low (default)")
	dr.key(term.KeyUp, 0) // wraps to xhigh
	dr.key(term.KeyEnter, 0)
	dr.waitFor("openai/gpt-deep · effort xhigh, applies")
	assert.NotContains(t, dr.view(), "Select effort")

	dr.typeText("hi")
	dr.key(term.KeyEnter, 0)
	dr.waitFor("• first")
	dr.waitIdle()
	reqs := llm.Requests()
	require.Len(t, reqs, 1)
	assert.Equal(t, "gpt-deep", reqs[0].Model)
	assert.Equal(t, "xhigh", reqs[0].Effort)

	dr.typeText("/model")
	dr.key(term.KeyEnter, 0)
	dr.waitFor("› gpt-deep (current)")
	dr.key(term.KeyEnter, 0)
	dr.waitFor("› xhigh (current)")
	dr.key(term.KeyEscape, 0)
	dr.waitFor("› gpt-deep (current)")
	dr.key(term.KeyEscape, 0)
	assert.NotContains(t, dr.view(), "Select model", "esc on the models closes the picker")

	dr.typeText("/model gpt-test low") // both at once: no picker
	dr.key(term.KeyEnter, 0)
	dr.waitFor("openai/gpt-test · effort low, applies")
	dr.typeText("again")
	dr.key(term.KeyEnter, 0)
	dr.waitFor("• second")
	reqs = llm.Requests()
	require.Len(t, reqs, 2)
	assert.Equal(t, "gpt-test", reqs[1].Model)
	assert.Equal(t, "low", reqs[1].Effort)
}
