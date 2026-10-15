package bubble_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/viktordanov/uah/internal/tui/bubble"
	"github.com/viktordanov/uah/internal/tui/state"
	"github.com/viktordanov/uah/internal/tui/term"
)

// status is the session's own status record, as the view hands it to the
// terminal (OSC 7501).
func (d *driver) status() term.Status { return d.m.View().Status[0] }

func (d *driver) waitStatus(state string) {
	d.t.Helper()
	d.until("the status "+state, func() bool { return d.status().State == state })
}

func TestTUI_StatusFollowsAnApproval(t *testing.T) {
	deps, _ := approvalDeps(t)
	d := start(t, deps)
	d.waitStatus(term.StateIdle)

	d.typeText("make the file")
	d.key(term.KeyEnter, 0)
	d.waitFor("Run outside the sandbox?")
	s := d.status()
	assert.Equal(t, term.StateBlocked, s.State)
	assert.Equal(t, term.KindPermission, s.Kind)
	assert.Contains(t, s.Msg, "Approve ")

	d.typeText("y")
	d.waitFor("done")
	d.waitStatus(term.StateDone)
	assert.NotEmpty(t, d.status().Msg, "the start of the answer")

	d.send(term.PasteMsg{Content: "next"})
	d.waitStatus(term.StateIdle) // a paste takes the result in, as a key does
}

func TestTUI_StatusWhileWorkingAndInterrupted(t *testing.T) {
	deps := deps(t, "timeout.jsonl")
	t.Setenv("FAKERUNNER_HANG", "1") // the run keeps a tool running until interrupted
	deps.Prompt = "start the long job"
	d := start(t, deps)
	d.waitFor("sleep 300")
	// The line shows once the call is announced, "Preparing Bash" until
	// it starts.
	running := term.Status{State: term.StateWorking, Msg: "Running 1 command"}
	d.until("the command running", func() bool { return d.status() == running })

	d.key(term.KeyEscape, 0)
	d.key(term.KeyEscape, 0)
	d.waitFor("■ interrupted")
	d.waitStatus(term.StateIdle)
}

// A nickname that is a valid ID is the ID; another adds the start of the
// child's UUID, so two nicknames that make one ID keep two records, and
// each child keeps its ID whatever others show.
func TestProgramStatusIDsStayApart(t *testing.T) {
	scoutA := state.ProgramStatus{ID: "agent/Scout A", Agent: "subagent-11111111-aaaa", State: term.StateWorking}
	scoutDashA := state.ProgramStatus{ID: "agent/Scout-A", Agent: "subagent-22222222-bbbb", State: term.StateDone}
	got := bubble.ProgramStatus([]state.ProgramStatus{{State: term.StateIdle}, scoutA, scoutDashA,
		{ID: "agent/Scout  A", Agent: "subagent-11111111-cccc", State: term.StateDone}})
	assert.Equal(t, "", got[0].ID)
	assert.Equal(t, "agent/11111111.Scout-A", got[1].ID, "the UUID's start, not subagent-")
	assert.Equal(t, "agent/Scout-A", got[2].ID)
	assert.Equal(t, "agent/11111111-cccc.Scout-A", got[3].ID, "more of it when its start is taken too")

	alone := bubble.ProgramStatus([]state.ProgramStatus{{State: term.StateIdle}, scoutDashA})
	assert.Equal(t, "agent/Scout-A", alone[1].ID, "the same ID without the other child")
	alone = bubble.ProgramStatus([]state.ProgramStatus{{State: term.StateIdle}, scoutA})
	assert.Equal(t, "agent/11111111.Scout-A", alone[1].ID)
}
