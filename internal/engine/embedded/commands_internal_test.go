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
		argv []string
		want []int
		ok   bool
	}{
		{[]string{"kill", "123"}, []int{123}, true},
		{[]string{"kill", "-TERM", "123", "456"}, []int{123, 456}, true},
		{[]string{"kill", "-9", "123"}, []int{123}, true},
		{[]string{"kill", "-s", "INT", "--", "-123"}, []int{-123}, true},
		{[]string{"kill", "--", "-123"}, []int{-123}, true},
		{[]string{"kill", "-TERM"}, nil, false},
		{[]string{"kill", "-1"}, nil, false},
		{[]string{"kill", "-TERM", "-1"}, nil, false},
		{[]string{"kill", "1"}, nil, false},
		{[]string{"kill", "0"}, nil, false},
		{[]string{"kill", "%1"}, nil, false},
		{[]string{"kill", "-l"}, nil, false},
		{[]string{"pkill", "node"}, nil, false},
		{[]string{"kill"}, nil, false},
	} {
		got, ok := killTargets(tc.argv)
		assert.Equal(t, tc.ok, ok, "%q", tc.argv)
		assert.Equal(t, tc.want, got, "%q", tc.argv)
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

	assert.True(t, c.ownKill("kill "+pid))
	assert.True(t, c.ownKill("kill -KILL -- -"+pid))
	assert.True(t, c.ownKill("kill "+pid+" && kill -9 "+pid))
	assert.False(t, c.ownKill("kill "+otherPid))
	assert.False(t, c.ownKill("kill "+pid+" "+otherPid))
	assert.False(t, c.ownKill("kill "+pid+"; rm -rf /tmp/x"))
	assert.False(t, c.ownKill("kill $(cat pid)"))
	assert.False(t, c.ownKill("kill "+strconv.Itoa(os.Getpid())))
	assert.False(t, (*commands)(nil).ownKill("kill "+pid))
	delete(c.groups, "op")
	assert.False(t, c.ownKill("kill "+pid), "a command that ended owns nothing")
}
