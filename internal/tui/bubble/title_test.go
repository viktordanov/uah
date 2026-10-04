package bubble_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/viktordanov/uah/internal/tui/term"
)

// title is the terminal title and whether a progress bar (OSC 9;4) is
// shown; uah shows none, as the title says enough, and term's View has no
// progress bar to show.
func (d *driver) title() (string, bool) {
	return d.m.View().WindowTitle, false
}

func (d *driver) waitTitle(want string) {
	d.t.Helper()
	d.until("the title "+want, func() bool { title, _ := d.title(); return title == want })
}

func TestTUI_TitleFollowsAnApproval(t *testing.T) {
	deps, _ := approvalDeps(t)
	deps.Title = true
	d := start(t, deps)
	d.waitTitle("uah · workspace")
	_, progress := d.title()
	assert.False(t, progress)

	d.typeText("make the file")
	d.key(term.KeyEnter, 0)
	d.waitFor("Run outside the sandbox?")
	title, progress := d.title()
	assert.Equal(t, "uah · approve? · workspace", title)
	assert.False(t, progress, "no progress bar")

	d.typeText("y")
	d.waitFor("done")
	d.waitTitle("uah · workspace")
	_, progress = d.title()
	assert.False(t, progress, "cleared when idle")
}

func TestTUI_TitleWhileWorking(t *testing.T) {
	deps := deps(t, "timeout.jsonl")
	t.Setenv("FAKERUNNER_HANG", "1") // the run keeps a tool running until interrupted
	deps.Title = true
	deps.Prompt = "start the long job"
	d := start(t, deps)
	d.waitFor("sleep 300")
	title, progress := d.title()
	assert.Equal(t, "uah · working · workspace", title)
	assert.False(t, progress, "no progress bar")

	d.key(term.KeyEscape, 0)
	d.key(term.KeyEscape, 0)
	d.waitFor("■ interrupted")
	d.waitTitle("uah · workspace")
	_, progress = d.title()
	assert.False(t, progress)
}

func TestTUI_TitleOff(t *testing.T) {
	d := start(t, deps(t, "simple.jsonl"))
	d.typeText("hi")
	d.key(term.KeyEnter, 0)
	d.waitFor("• hello")
	title, progress := d.title()
	assert.Empty(t, title, "uah leaves the title alone")
	assert.False(t, progress, "and no progress bar")
}
