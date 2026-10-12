package embedded_test

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/engine/embedded"
	"github.com/viktordanov/uah/internal/toolpolicy"
	"github.com/viktordanov/uah/testing/fakellm"
)

func shellCall(name, args string) fakellm.Reply {
	return fakellm.Reply{Calls: []fakellm.Call{{Name: name, Args: args}}}
}

func shellState(state string) func(core.Event) bool {
	return func(e core.Event) bool { u, ok := e.(engine.ShellUpdated); return ok && u.State == state }
}

// TestShells_Background: a Bash call with background returns at once with
// the shell's ID and log, the run ends while the shell runs, and its end
// starts a run of its own, which takes it up; Bash offers the argument and
// the shell tools are offered.
func TestShells_Background(t *testing.T) {
	e := newEnv(t,
		shellCall("Bash", `{"command":"echo started; sleep 0.3; echo finished","background":true}`),
		fakellm.Reply{Text: "it runs"},
		fakellm.Reply{Text: "it finished"},
	)
	s, ev := e.open(t, e.embedded(), "")

	_, err := s.Submit("start the job")
	require.NoError(t, err)
	assert.Equal(t, "it runs", ev.finished().Answer)
	exited := ev.until("ShellUpdated exited", shellState(engine.ShellExited)).(engine.ShellUpdated)
	assert.Equal(t, "it finished", ev.finished().Answer, "the shell's end started a run")
	assert.Equal(t, "shell-1", exited.ID)
	assert.Equal(t, 0, exited.ExitCode)

	reqs := e.llm.Requests()
	require.Len(t, reqs, 3)
	assert.Contains(t, reqs[0].Tools["Bash"], `"background"`)
	assert.Contains(t, reqs[0].ToolNames, "wait_shell")
	assert.Contains(t, reqs[0].ToolNames, "kill_shell")
	assert.Contains(t, reqs[1].ToolOutputs[0], "Background shell shell-1 started")
	assert.Contains(t, reqs[1].ToolOutputs[0], exited.Log)

	var notes []string
	for _, d := range slices.Concat(reqs[2].UserTexts, reqs[2].DeveloperTexts) {
		if end, ok := engine.ParseShellNotification(d); ok {
			notes = append(notes, end.ID)
			assert.Equal(t, engine.ShellExited, end.State)
			require.NotNil(t, end.ExitCode)
			assert.Equal(t, 0, *end.ExitCode)
			assert.Contains(t, end.Tail, "finished")
		}
	}
	assert.Equal(t, []string{"shell-1"}, notes, "the end comes once")
}

// TestShells_WaitAndKill: kill_shell stops a shell's process group, and
// wait_shell returns a shell's end; neither sends a notification, since
// the agent learned the end from the call.
func TestShells_WaitAndKill(t *testing.T) {
	e := newEnv(t,
		shellCall("Bash", `{"command":"sleep 30","background":true}`),
		shellCall("kill_shell", `{"shell_id":"shell-1"}`),
		shellCall("Bash", `{"command":"sleep 0.2; echo hi","background":true}`),
		shellCall("wait_shell", `{"shell_ids":["shell-2"]}`),
		shellCall("wait_shell", `{"shell_ids":["shell-9"]}`),
		fakellm.Reply{Text: "done"},
	)
	s, ev := e.open(t, e.embedded(), "")

	_, err := s.Submit("run them")
	require.NoError(t, err)
	assert.Equal(t, "done", ev.finished().Answer)

	reqs := e.llm.Requests()
	require.Len(t, reqs, 6)
	var killed engine.ShellEnd
	require.NoError(t, json.Unmarshal([]byte(reqs[2].ToolOutputs[1]), &killed))
	assert.Equal(t, engine.ShellKilled, killed.State)

	var waited struct {
		Shells   []engine.ShellEnd `json:"shells"`
		TimedOut bool              `json:"timed_out"`
	}
	require.NoError(t, json.Unmarshal([]byte(reqs[4].ToolOutputs[3]), &waited))
	require.Len(t, waited.Shells, 1)
	assert.False(t, waited.TimedOut)
	assert.Equal(t, engine.ShellExited, waited.Shells[0].State)
	assert.Contains(t, waited.Shells[0].Tail, "hi")
	assert.Contains(t, reqs[5].ToolOutputs[4], "shells do not survive a restart")

	for _, r := range reqs {
		for _, d := range slices.Concat(r.UserTexts, r.DeveloperTexts) {
			assert.NotContains(t, d, "background_shell_notification")
		}
	}
}

// TestShells_StopWithTheSession: closing the session stops its shells.
func TestShells_StopWithTheSession(t *testing.T) {
	e := newEnv(t,
		shellCall("Bash", `{"command":"sleep 30","background":true}`),
		fakellm.Reply{Text: "started"},
	)
	s, ev := e.open(t, e.embedded(), "")

	_, err := s.Submit("start it")
	require.NoError(t, err)
	running := ev.until("ShellUpdated running", shellState(engine.ShellRunning)).(engine.ShellUpdated)
	ev.finished()
	require.NoError(t, syscall.Kill(-running.Group, 0), "it runs after the run")

	require.NoError(t, s.Close())
	deadline := time.Now().Add(5 * time.Second)
	for syscall.Kill(-running.Group, 0) == nil && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	assert.True(t, errors.Is(syscall.Kill(-running.Group, 0), syscall.ESRCH), "its process group is gone")
}

// TestShells_StopFromTheUser: a shell the user stops tells the agent, in a
// run its end starts.
func TestShells_StopFromTheUser(t *testing.T) {
	e := newEnv(t,
		shellCall("Bash", `{"command":"sleep 30","background":true}`),
		fakellm.Reply{Text: "started"},
		fakellm.Reply{Text: "noted"},
	)
	eng := e.embedded()
	s, ev := e.open(t, eng, "")

	_, err := s.Submit("start it")
	require.NoError(t, err)
	ev.finished()
	ev.idle()
	require.NoError(t, eng.StopShell(s.ID(), "shell-1"))
	assert.Equal(t, "noted", ev.finished().Answer)

	reqs := e.llm.Requests()
	require.Len(t, reqs, 3)
	assert.Contains(t, strings.Join(slices.Concat(reqs[2].UserTexts, reqs[2].DeveloperTexts), "\n"), `"state":"killed"`)
}

// TestShells_PolicyLeavesThemOut: a tool policy that allows Bash alone
// offers no background argument and no shell tools, and a session with a
// background call still loads under it.
func TestShells_PolicyLeavesThemOut(t *testing.T) {
	e := newEnv(t,
		shellCall("Bash", `{"command":"sleep 30","background":true}`), // stopped with its session: no end to take up
		fakellm.Reply{Text: "started"},
		fakellm.Reply{Text: "again"},
	)
	s, ev := e.open(t, e.embedded(), "")
	_, err := s.Submit("start it")
	require.NoError(t, err)
	ev.finished()
	ev.idle()
	require.NoError(t, s.Close())

	bashOnly := embedded.New(embedded.Config{StateDir: e.StateDir, Provider: "openai", Getenv: e.getenv, Tools: toolpolicy.Policy{Allow: []string{"Bash"}}})
	s2, ev2 := e.open(t, bashOnly, s.ID())
	_, err = s2.Submit("and now?")
	require.NoError(t, err)
	assert.Equal(t, "again", ev2.finished().Answer)

	reqs := e.llm.Requests()
	require.Len(t, reqs, 3)
	assert.NotContains(t, reqs[2].Tools["Bash"], `"background"`)
	assert.NotContains(t, reqs[2].ToolNames, "wait_shell")
	assert.NotContains(t, reqs[2].ToolNames, "kill_shell")
	assert.Contains(t, strings.Join(reqs[2].ToolOutputs, "\n"), "Background shell shell-1 started", "the earlier result still reads")
}
