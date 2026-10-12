package state_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/internal/tui/state"
)

// TestReduce_Shells: /ps lists the background shells, an end says so in
// the transcript, /ps stop stops them, and the notification the agent got
// reads as a line.
func TestReduce_Shells(t *testing.T) {
	t0 := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	s, _ := apply(opened(), state.Submit{Text: "/ps"})
	assert.Contains(t, s.Items[len(s.Items)-1].Text, "no background shells in this session")

	running := engine.ShellUpdated{At: t0, ID: "shell-1", Command: "npm run dev", State: engine.ShellRunning, Group: 4242, Log: "/tmp/shell-1.log", Started: t0}
	s, _ = apply(s, running, engine.ShellUpdated{At: t0, ID: "shell-2", Command: "make build", State: engine.ShellRunning, Group: 4343, Log: "/tmp/shell-2.log", Started: t0})
	n := len(s.Items)
	assert.Equal(t, 2, s.RunningShells())

	s, _ = apply(s, state.Submit{Text: "/ps"})
	list := s.Items[len(s.Items)-1].Text
	assert.Contains(t, list, "shell-1 · running")
	assert.Contains(t, list, "process group 4242 · npm run dev")
	assert.Contains(t, list, "log: /tmp/shell-2.log")

	ended := engine.ShellUpdated{At: t0, ID: "shell-2", Command: "make build", State: engine.ShellExited, ExitCode: 2, Started: t0, Ended: t0.Add(90 * time.Second)}
	s, _ = apply(s, ended)
	assert.Equal(t, "background shell shell-2 exited 2 after 1m30s: make build", s.Items[len(s.Items)-1].Text)
	assert.Equal(t, 1, s.RunningShells())
	assert.Greater(t, len(s.Items), n)

	s, _ = apply(s, engine.ShellUpdated{At: t0, ID: "shell-2", Command: "make build", State: engine.ShellRunning, Started: t0})
	assert.Equal(t, 1, s.RunningShells(), "a late running update does not undo an end")

	_, eff := apply(s, state.Submit{Text: "/ps stop all"})
	assert.Equal(t, []state.Effect{state.EffStopShells{IDs: []string{"shell-1", "shell-2"}}}, eff, "an ended shell too: it may have left children")
	_, eff = apply(s, state.Submit{Text: "/ps stop shell-1"})
	assert.Equal(t, []state.Effect{state.EffStopShells{IDs: []string{"shell-1"}}}, eff)

	note, err := engine.ShellNotification(engine.ShellEnd{ID: "shell-2", Command: "make build", State: engine.ShellExited})
	require.NoError(t, err)
	s, _ = apply(s, core.DeveloperMessage{At: t0, ID: "d1", Text: note})
	assert.Equal(t, "the agent was told that background shell shell-2 exited", s.Items[len(s.Items)-1].Text)
}

// TestReduce_QuitStopsShells: while a background shell runs, ctrl+c asks
// again, naming the shells it stops.
func TestReduce_QuitStopsShells(t *testing.T) {
	s, _ := apply(opened(), engine.ShellUpdated{ID: "shell-1", Command: "sleep 100", State: engine.ShellRunning})
	s, eff := apply(s, state.Quit{})
	assert.Empty(t, eff)
	assert.Equal(t, "press ctrl+c again to stop 1 background shell and quit", s.Status)
	_, eff = apply(s, state.Quit{})
	assert.Equal(t, []state.Effect{state.EffQuit{}}, eff)
}

// TestReduce_ShellsAreTheSessions: another session opened starts with no
// shells; switching stopped the old session's.
func TestReduce_ShellsAreTheSessions(t *testing.T) {
	s, _ := apply(opened(), engine.ShellUpdated{ID: "shell-1", Command: "sleep 100", State: engine.ShellRunning})
	require.Equal(t, 1, s.RunningShells())
	s, _ = apply(s, session.SessionOpened{ID: "s2", Settings: session.Settings{Model: "gpt-test"}})
	assert.Equal(t, 0, s.RunningShells())
	_, eff := apply(s, state.Quit{})
	assert.Equal(t, []state.Effect{state.EffQuit{}}, eff, "nothing left to confirm")
}
