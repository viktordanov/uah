package embedded

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKillTargets(t *testing.T) {
	for _, tc := range []struct {
		argv   []string
		signal string
		want   []int
	}{
		{[]string{"kill", "123"}, "TERM", []int{123}},
		{[]string{"kill", "-TERM", "123", "456"}, "TERM", []int{123, 456}},
		{[]string{"kill", "-SIGKILL", "123"}, "KILL", []int{123}},
		{[]string{"kill", "-9", "123"}, "KILL", []int{123}},
		{[]string{"kill", "-s", "int", "--", "-123"}, "INT", []int{-123}},
		{[]string{"kill", "-n", "15", "123"}, "TERM", []int{123}},
		{[]string{"kill", "--", "-123"}, "TERM", []int{-123}},
		{[]string{"kill", "-TERM"}, "", nil},
		{[]string{"kill", "-1"}, "", nil},
		{[]string{"kill", "-TERM", "-1"}, "", nil},
		{[]string{"kill", "1"}, "", nil},
		{[]string{"kill", "0"}, "", nil},
		{[]string{"kill", "0123"}, "", nil},
		{[]string{"kill", "%1"}, "", nil},
		{[]string{"kill", "-l"}, "", nil},
		{[]string{"kill", "-NOPE", "123"}, "", nil},
		{[]string{"kill", "-s", "{TERM,456}", "123"}, "", nil},
		{[]string{"kill", "-s", "$SIG", "123"}, "", nil},
		{[]string{"kill", "{123,456}"}, "", nil},
		{[]string{"kill", "-s"}, "", nil},
		{[]string{"pkill", "node"}, "", nil},
		{[]string{"kill"}, "", nil},
	} {
		signal, got, ok := killTargets(tc.argv)
		assert.Equal(t, tc.want != nil, ok, "%q", tc.argv)
		assert.Equal(t, tc.want, got, "%q", tc.argv)
		assert.Equal(t, tc.signal, signal, "%q", tc.argv)
	}
}

// TestOwnKill allows a kill of a process in one of the run's running
// commands' groups, and nothing else.
func TestOwnKill(t *testing.T) {
	own := exec.Command("sleep", "60")
	own.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	require.NoError(t, own.Start())
	other := exec.Command("sleep", "60")
	other.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	require.NoError(t, other.Start())
	t.Cleanup(func() {
		for _, c := range []*exec.Cmd{own, other} {
			_ = c.Process.Kill()
			_ = c.Wait()
		}
	})
	c := newCommands(context.Background(), 0)
	c.groups["op"] = own.Process.Pid
	pid, otherPid := strconv.Itoa(own.Process.Pid), strconv.Itoa(other.Process.Pid)

	owned := func(command string) bool { _, ok := c.ownKill(command); return ok }
	run, ok := c.ownKill("kill " + pid)
	assert.True(t, ok)
	assert.Equal(t, "kill -s TERM -- "+pid, run, "the checked command runs, not the model's text")
	run, ok = c.ownKill("kill -KILL -- -" + pid + " && kill -9 '" + pid + "'")
	assert.True(t, ok)
	assert.Equal(t, "kill -s KILL -- -"+pid+"; kill -s KILL -- "+pid, run)
	assert.False(t, owned("kill "+otherPid))
	assert.False(t, owned("kill "+pid+" "+otherPid))
	assert.False(t, owned("kill -s {TERM,"+otherPid+"} "+pid), "a brace would add a target")
	assert.False(t, owned("kill "+pid+"; rm -rf /tmp/x"))
	assert.False(t, owned("kill $(cat pid)"))
	assert.False(t, owned("kill "+strconv.Itoa(os.Getpid())))
	_, ok = (*commands)(nil).ownKill("kill " + pid)
	assert.False(t, ok)
	delete(c.groups, "op")
	assert.False(t, owned("kill "+pid), "a command that ended owns nothing")
}
