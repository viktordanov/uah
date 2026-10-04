package bubble_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	uaharness "github.com/viktordanov/uagent/harness"
	"github.com/viktordanov/uagent/testing/fixtures"

	"github.com/viktordanov/uah/internal/approval"
	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/internal/tui/bubble"
	"github.com/viktordanov/uah/internal/tui/term"
	"github.com/viktordanov/uah/testing/harnesstest"
)

// deps opens real sessions on uagent's fake runner (harnesstest.RunnerEngine),
// which takes nothing live, so a change applies from the next run.
func deps(t *testing.T, fixture string) bubble.Deps {
	t.Helper()

	return depsIn(t, fixture, "")
}

// depsIn is deps with sessions in a permission mode ("": the default).
func depsIn(t *testing.T, fixture string, mode approval.Mode) bubble.Deps {
	t.Helper()
	env := harnesstest.NewEnv(t)
	t.Setenv("FAKERUNNER_FIXTURE", fixtures.Path(fixture))
	t.Setenv("FAKERUNNER_ECHO", "1")
	eng := harnesstest.RunnerEngine(uaharness.Config{
		RunnerPath: harnesstest.FakeRunner(t), StateDir: env.StateDir, KillGrace: 300 * time.Millisecond, Getenv: env.Getenv,
	})
	settings := session.Settings{Provider: "openai-codex", Model: "gpt-6-sol", Effort: "high", Workspace: env.Workspace}
	if mode != "" {
		settings = settings.WithMode(mode)
	}

	return bubble.Deps{
		Open: func(ctx context.Context, id string) (*session.Session, []session.LoadedRun, error) {
			s, err := session.Open(ctx, eng, session.Options{ID: id, Resumed: id != "", Settings: settings, Yolo: mode == approval.ModeYolo})
			if err != nil || id == "" {
				return s, nil, err
			}
			history, err := session.Load(env.StateDir, id)

			return s, history, err
		},
		Sessions: func() ([]session.Info, error) { return session.Sessions(env.StateDir) },
	}
}

// driver runs a model like term.Program does: commands run in goroutines and
// their messages go back through Update. Checks read the rendered view, which
// is exact, unlike the renderer's cell-diff output.
type driver struct {
	t    *testing.T
	m    term.Model
	msgs chan term.Msg
	quit bool
}

func start(t *testing.T, d bubble.Deps) *driver {
	t.Helper()
	dr := &driver{t: t, m: bubble.New(context.Background(), d), msgs: make(chan term.Msg, 256)}
	dr.send(term.WindowSizeMsg{Width: 100, Height: 30})
	dr.exec(dr.m.Init())

	return dr
}

func (d *driver) exec(cmd term.Cmd) {
	if cmd != nil {
		go func() {
			if msg := cmd(); msg != nil {
				d.msgs <- msg
			}
		}()
	}
}

// update gives the model a message and returns its command without running
// it, so a test can choose when, and in which order, commands run.
func (d *driver) update(msg term.Msg) term.Cmd {
	next, cmd := d.m.Update(msg)
	d.m = next

	return cmd
}

// execNow runs a command in a goroutine at once, and each command of a batch
// in its own, as term.Program does; exec leaves a batch for send to start.
func (d *driver) execNow(cmd term.Cmd) {
	if cmd == nil {
		return
	}
	go func() {
		switch msg := cmd().(type) {
		case nil:
		case term.BatchMsg:
			for _, c := range msg {
				d.execNow(c)
			}
		default:
			d.msgs <- msg
		}
	}()
}

func (d *driver) send(msg term.Msg) {
	switch msg := msg.(type) {
	case term.BatchMsg:
		for _, c := range msg {
			d.exec(c)
		}

		return
	case term.QuitMsg:
		d.quit = true

		return
	}
	next, cmd := d.m.Update(msg)
	d.m = next
	d.exec(cmd)
}

func (d *driver) view() string { return ansi.Strip(d.m.View().Content) }

func (d *driver) typeText(s string) {
	for _, r := range s {
		d.send(term.KeyPressMsg{Code: r, Text: string(r)})
	}
}

func (d *driver) key(code rune, mod term.KeyMod) { d.send(term.KeyPressMsg{Code: code, Mod: mod}) }

// waitIdle waits until the session is idle: no run, and no message on its
// way. It reads the state, since the screen shows no "esc to interrupt"
// while the session finishes a run.
func (d *driver) waitIdle() {
	d.t.Helper()
	d.until("idle", func() bool { return !d.m.(bubble.Model).Busy() })
}

// until processes messages until check passes. It also checks every 10
// ms, since a check may wait on something outside the model, such as the
// fake model's requests, while no message comes.
func (d *driver) until(what string, check func() bool) {
	d.t.Helper()
	deadline := time.After(10 * time.Second)
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	for !check() {
		select {
		case msg := <-d.msgs:
			d.send(msg)
		case <-poll.C:
		case <-deadline:
			d.t.Fatalf("timed out waiting for %s; screen:\n%s", what, d.view())
		}
	}
}

// pump processes messages for a while, such as the clock's ticks.
func (d *driver) pump(dur time.Duration) {
	end := time.After(dur)
	for {
		select {
		case msg := <-d.msgs:
			d.send(msg)
		case <-end:
			return
		}
	}
}

func (d *driver) waitFor(text string) {
	d.t.Helper()
	d.until(fmt.Sprintf("%q", text), func() bool { return strings.Contains(d.view(), text) })
}

func (d *driver) waitQuit() {
	d.t.Helper()
	d.until("quit", func() bool { return d.quit })
}

func TestTUI_SendAMessageAndQuit(t *testing.T) {
	d := start(t, deps(t, "simple.jsonl"))
	assert.Contains(t, d.view(), "Opening the session")
	d.until("the session is open", func() bool { return d.m.(bubble.Model).Exit().SessionID != "" })
	assert.False(t, d.m.(bubble.Model).Exit().Resumable, "a session that never ran has nothing to resume")

	d.typeText("hi there") // typed before the session opens: it is held, not lost
	d.key(term.KeyEnter, 0)
	d.waitFor("• hello")
	assert.Contains(t, d.view(), "λ hi there")
	assert.NotContains(t, d.view(), "1 run ·", "the compact view hides totals")

	d.key('t', term.ModCtrl)
	d.waitFor("1 run ·")
	d.waitFor("● answer")
	d.key('t', term.ModCtrl)
	d.until("the compact view again", func() bool { return !strings.Contains(d.view(), "1 run ·") })

	exit := d.m.(bubble.Model).Exit()
	assert.True(t, exit.Resumable, "after a run, uah names the command that resumes it")
	assert.NotEmpty(t, exit.SessionID)

	d.typeText("/quit")
	d.key(term.KeyEnter, 0)
	d.waitQuit()
}

func TestTUI_CommandsAndPrompt(t *testing.T) {
	deps := deps(t, "simple.jsonl")
	deps.Prompt = "first prompt"
	d := start(t, deps)
	d.waitFor("hello")

	d.typeText("/re")
	assert.Contains(t, d.view(), "/resume [id]", "typing a command shows completions")
	d.key('c', term.ModCtrl) // clears the draft, not quits
	assert.Contains(t, d.view(), "Ask uah to do anything", "the composer is empty again")
	assert.NotContains(t, d.view(), "/resume [id]")

	d.typeText("/effort low")
	d.key(term.KeyEnter, 0)
	d.waitFor("effort low, applies from the next run")
	assert.Contains(t, d.view(), "gpt-6-sol low", "the footer shows the new effort")

	d.typeText("/nope")
	d.key(term.KeyEnter, 0)
	d.waitFor("unknown command /nope")

	d.waitIdle() // ctrl+c while a run is live only arms the quit
	d.key('c', term.ModCtrl)
	d.waitQuit()
}

func TestTUI_ResumeFromThePicker(t *testing.T) {
	deps := deps(t, "simple.jsonl")
	deps.Prompt = "remember this"
	d := start(t, deps)
	d.waitFor("hello")
	d.waitIdle() // /new waits while a run is live

	d.typeText("/new")
	d.key(term.KeyEnter, 0)
	d.until("a new, empty session", func() bool {
		v := d.view()

		return !strings.Contains(v, "remember this") && !strings.Contains(v, "hello")
	})

	d.key('s', term.ModCtrl)
	d.waitFor("Resume a session")
	d.typeText("remember")
	d.key(term.KeyEnter, 0)
	d.waitFor("λ remember this")
	d.waitFor("• hello")

	d.key('c', term.ModCtrl)
	d.waitQuit()
}

func TestTUI_QueueInterruptAndEdit(t *testing.T) {
	deps := deps(t, "timeout.jsonl")
	t.Setenv("FAKERUNNER_HANG", "1") // the run keeps a tool running until interrupted
	deps.Prompt = "start the long job"
	d := start(t, deps)
	d.waitFor("esc to interrupt")
	d.waitFor("sleep 300")

	d.typeText("then update the README")
	d.key(term.KeyTab, 0) // queues for the end of the run
	d.waitFor("↳ queued: then update the README")

	d.key(term.KeyEscape, 0)
	d.waitFor("press esc again to interrupt")
	d.key(term.KeyEscape, 0)
	d.waitFor("■ interrupted")
	d.waitIdle()
	assert.Contains(t, d.view(), "↳ queued: then update the README", "an interrupt keeps the queue")
	assert.Contains(t, d.view(), "stop ", "the unfinished tool is shown as stopped")

	d.key(term.KeyUp, 0)
	d.until("the queued message back in the composer", func() bool {
		v := d.view()

		return strings.Contains(v, "λ then update the README") && !strings.Contains(v, "queued: then update the README")
	})

	d.key('c', term.ModCtrl) // clears the draft
	d.key('c', term.ModCtrl) // quits: the session is idle
	d.waitQuit()
}

func TestTUI_WheelScrolls(t *testing.T) {
	d := start(t, deps(t, "simple.jsonl"))
	d.typeText("hi")
	d.key(term.KeyEnter, 0)
	d.waitFor("• hello")
	d.waitIdle() // a busy footer's elapsed time would change the views compared below
	for range 3 {
		d.typeText("/help")
		d.key(term.KeyEnter, 0)
	}
	bottom := d.view()

	d.send(term.MouseWheelMsg{Button: term.MouseWheelUp})
	assert.Contains(t, d.view(), "scrolled up")
	for range 200 {
		d.send(term.MouseWheelMsg{Button: term.MouseWheelUp})
	}
	top := d.view()
	assert.Contains(t, top, "λ hi", "the first message is reachable")
	d.send(term.MouseWheelMsg{Button: term.MouseWheelDown})
	assert.NotEqual(t, top, d.view(), "scrolling back down moves at once: the offset stops at the top")

	d.key(term.KeyEnd, 0)
	assert.Equal(t, bottom, d.view())

	// The terminal's wheel as ↑ and ↓ scrolls with a prompt typed too: a
	// one-line prompt has no row above or below for the cursor.
	d.typeText("half a thought")
	d.key(term.KeyUp, 0)
	assert.Contains(t, d.view(), "scrolled up")
	assert.Contains(t, d.view(), "half a thought", "the prompt stays")
	d.key(term.KeyDown, 0)
	assert.NotContains(t, d.view(), "scrolled up")
}

func TestTUI_MenuCompletes(t *testing.T) {
	deps := deps(t, "simple.jsonl")
	deps.Cwd = t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(deps.Cwd, "notes.md"), []byte("x"), 0o600))
	d := start(t, deps)
	d.typeText("hi")
	d.key(term.KeyEnter, 0)
	d.waitFor("• hello")
	d.waitIdle() // esc below must find no live run

	d.typeText("/eff")
	d.key(term.KeyTab, 0)
	d.typeText("l")
	d.key(term.KeyTab, 0)
	assert.Contains(t, d.view(), "λ /effort low")
	d.key(term.KeyEnter, 0)
	d.waitFor("effort low, applies from the next run")

	d.typeText("see @not")
	d.waitFor("notes.md")
	d.key(term.KeyTab, 0)
	assert.Contains(t, d.view(), "λ see notes.md")
	d.key(term.KeyEscape, 0)
	assert.NotContains(t, d.view(), "press esc again", "esc with no menu open still means interrupt only while busy")
}

func TestTUI_StatusShowsActivity(t *testing.T) {
	deps := deps(t, "simple.jsonl")
	deps.Activity = func() (map[string]int, error) { return map[string]int{time.Now().Format(time.DateOnly): 3}, nil }
	d := start(t, deps)
	d.typeText("hi")
	d.key(term.KeyEnter, 0)
	d.waitFor("• hello")
	d.typeText("/status")
	d.key(term.KeyEnter, 0)
	d.waitFor("activity · last 12 weeks · 3 runs")
}

// TestTUI_LambdaOnTheFirstRowOnly draws the λ before the composer's first
// row, and nothing before the rows under it.
func TestTUI_LambdaOnTheFirstRowOnly(t *testing.T) {
	d := start(t, deps(t, "simple.jsonl"))
	d.until("the session is open", func() bool { return !strings.Contains(d.view(), "Opening the session") })
	d.typeText("first")
	d.key('j', term.ModCtrl) // a new line
	d.typeText("second")
	d.key('j', term.ModCtrl)
	d.typeText("third")
	view := d.view()
	assert.Contains(t, view, "λ first")
	assert.Contains(t, view, "\n  second")
	assert.Contains(t, view, "\n  third")
	assert.Equal(t, 1, strings.Count(view, "\nλ "), "one λ for the whole composer")
	d.key('c', term.ModCtrl)
	d.key('c', term.ModCtrl)
	d.waitQuit()
}
