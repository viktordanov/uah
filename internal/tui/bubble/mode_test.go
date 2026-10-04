package bubble_test

import (
	"testing"

	"github.com/viktordanov/uah/internal/approval"
	"github.com/viktordanov/uah/internal/tui/term"
)

// TestTUI_ShiftTabCyclesTheMode drives shift+tab through the session: the
// footer follows each change, and the fake runner applies it from the
// next run.
func TestTUI_ShiftTabCyclesTheMode(t *testing.T) {
	d := start(t, deps(t, "simple.jsonl"))
	d.waitFor("gpt-6-sol high ·")

	d.key(term.KeyTab, term.ModShift)
	d.waitFor("auto mode ·")
	d.waitFor("Applies from the next run.")
	d.key(term.KeyTab, term.ModShift)
	d.waitFor("read only mode ·")
	d.key(term.KeyTab, term.ModShift)
	d.waitFor("workspace mode ·")
}

// TestTUI_YoloIsPreselectedAndCycles: a session started with --yolo opens
// in yolo mode, and shift+tab goes read only, workspace, auto, and back to
// yolo; a session without it never offers yolo (TestTUI_ShiftTabCyclesTheMode).
func TestTUI_YoloIsPreselectedAndCycles(t *testing.T) {
	d := start(t, depsIn(t, "simple.jsonl", approval.ModeYolo))
	d.waitFor("yolo mode ·")

	for _, want := range []string{"read only mode ·", "workspace mode ·", "auto mode ·", "yolo mode ·"} {
		d.key(term.KeyTab, term.ModShift)
		d.waitFor(want)
	}
}

// TestTUI_AltECyclesAdaptiveEffort: alt+e steps adaptive effort through 1
// step, 2 steps, and off for this session; the footer marks the effort and
// a notice says where follow-ups go.
func TestTUI_AltECyclesAdaptiveEffort(t *testing.T) {
	d := start(t, deps(t, "simple.jsonl"))
	d.waitFor("gpt-6-sol high ·")

	d.key('e', term.ModAlt)
	d.waitFor("adaptive effort: 1 step (follow-ups at medium)")
	d.waitFor("gpt-6-sol high↓ ·")
	d.key('e', term.ModAlt)
	d.waitFor("adaptive effort: 2 steps (follow-ups at low)")
	d.waitFor("gpt-6-sol high⇊ ·")
	d.key('e', term.ModAlt)
	d.waitFor("adaptive effort: off")
	d.waitFor("gpt-6-sol high ·")
}
