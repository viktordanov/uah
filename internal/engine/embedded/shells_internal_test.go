package embedded

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestShells_CapHoldsInParallel: starts that race each other cannot pass
// the cap together.
func TestShells_CapHoldsInParallel(t *testing.T) {
	var s shells
	t.Cleanup(s.close)
	dir := t.TempDir()
	var wg sync.WaitGroup
	var mu sync.Mutex
	started := 0
	for range maxShells + 8 {
		wg.Go(func() {
			if _, err := s.start(s.attach("s1", nil, nil), "", "/bin/sh", "sleep 30", dir, filepath.Join(dir, "logs")); err == nil {
				mu.Lock()
				started++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	assert.Equal(t, maxShells, started)
}

// TestShells_KeepEarlierLogs: a log from before a restart is never
// overwritten; the new shell takes the next number.
func TestShells_KeepEarlierLogs(t *testing.T) {
	var s shells
	t.Cleanup(s.close)
	dir := t.TempDir()
	logs := filepath.Join(dir, "logs")
	require.NoError(t, os.MkdirAll(logs, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(logs, "shell-1.log"), []byte("before the restart\n"), 0o600))

	sh, err := s.start(s.attach("s1", nil, nil), "", "/bin/sh", "echo after", dir, logs)
	require.NoError(t, err)
	assert.Equal(t, "shell-2", sh.id)
	data, err := os.ReadFile(filepath.Join(logs, "shell-1.log"))
	require.NoError(t, err)
	assert.Equal(t, "before the restart\n", string(data))
}

// TestShells_StopKillsTheWholeGroup: a child that ignores SIGTERM outlives
// its shell's exit, and still gets SIGKILL at the grace.
func TestShells_StopKillsTheWholeGroup(t *testing.T) {
	var s shells
	t.Cleanup(s.close)
	dir := t.TempDir()
	sh, err := s.start(s.attach("s1", nil, nil), "", "/bin/sh", "(trap '' TERM; exec sleep 30) & wait", dir, filepath.Join(dir, "logs"))
	require.NoError(t, err)

	end, err := s.stop("s1", sh.id, true)
	require.NoError(t, err)
	assert.Equal(t, "killed", end.State)
	// A killed child whose shell exited is init's to reap, so it may stay
	// a zombie, still in the group, a moment after the SIGKILL.
	assert.Eventually(t, func() bool { return errors.Is(syscall.Kill(-sh.group, 0), syscall.ESRCH) }, 2*time.Second, 10*time.Millisecond, "no member of the group is left")
}

// TestShells_CloseStopsLeftChildren: a shell that ended while a child it
// started runs on in its group: closing stops the child.
func TestShells_CloseStopsLeftChildren(t *testing.T) {
	var s shells
	dir := t.TempDir()
	sh, err := s.start(s.attach("s1", nil, nil), "", "/bin/sh", "sleep 60 >/dev/null 2>&1 &", dir, filepath.Join(dir, "logs"))
	require.NoError(t, err)
	<-sh.done
	require.True(t, groupAlive(sh.group), "the child runs on")

	s.close()
	assert.False(t, groupAlive(sh.group), "closing stopped it")
}

// TestShells_WaitTakesBackANotificationGoingOut: a wait_shell that returns
// an end while its notification is still going out takes it back.
func TestShells_WaitTakesBackANotificationGoingOut(t *testing.T) {
	var s shells
	t.Cleanup(s.close)
	release, injecting := make(chan struct{}), make(chan struct{})
	withdrawn := make(chan struct{})
	s.attach("s1", func(string) (func(), bool) {
		close(injecting)
		<-release

		return func() { close(withdrawn) }, false
	}, nil)
	dir := t.TempDir()
	sh, err := s.start(s.attach("s1", nil, nil), "", "/bin/sh", "true", dir, filepath.Join(dir, "logs"))
	require.NoError(t, err)
	<-injecting

	ends, _, err := s.waitFor(context.Background(), time.Minute, "s1", []string{sh.id})
	require.NoError(t, err)
	assert.Equal(t, "exited", ends[0].State)
	close(release)
	select {
	case <-withdrawn:
	case <-time.After(5 * time.Second):
		t.Fatal("the notification was not taken back")
	}
}

// TestShells_AnEndAfterAWaitIsTold: a shell still running when its wait
// times out gets its notification when it ends.
func TestShells_AnEndAfterAWaitIsTold(t *testing.T) {
	var s shells
	t.Cleanup(s.close)
	told := make(chan string, 1)
	s.attach("s1", func(text string) (func(), bool) { told <- text; return func() {}, false }, nil)
	dir := t.TempDir()
	sh, err := s.start(s.attach("s1", nil, nil), "", "/bin/sh", "sleep 0.3", dir, filepath.Join(dir, "logs"))
	require.NoError(t, err)

	ends, timedOut, err := s.waitFor(context.Background(), 50*time.Millisecond, "s1", []string{sh.id})
	require.NoError(t, err)
	assert.True(t, timedOut)
	assert.Equal(t, "running", ends[0].State)
	select {
	case text := <-told:
		assert.Contains(t, text, `"state":"exited"`)
	case <-time.After(5 * time.Second):
		t.Fatal("no notification after the wait")
	}
}

// TestShells_DetachedDescendantDoesNotHold: a descendant in a session of
// its own keeps the output pipe open; the shell still ends, and a stop
// returns.
func TestShells_DetachedDescendantDoesNotHold(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("no python3")
	}
	var s shells
	t.Cleanup(s.close)
	dir := t.TempDir()
	cmd := python + ` -c 'import os,time; os.setsid(); print(os.getpid(), flush=True); time.sleep(30)' &`
	sh, err := s.start(s.attach("s1", nil, nil), "", "/bin/sh", cmd, dir, filepath.Join(dir, "logs"))
	require.NoError(t, err)
	select {
	case <-sh.done:
	case <-time.After(10 * time.Second):
		t.Fatal("the shell never ended")
	}
	t.Cleanup(func() {
		data, _ := os.ReadFile(sh.log)
		if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	done := make(chan struct{})
	go func() { _, _ = s.stop("s1", sh.id, true); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the stop never returned")
	}
}

// TestShells_EmptyGroupIsRetired: an ended shell whose group was seen empty
// is never signalled again, since its ID may be another's.
func TestShells_EmptyGroupIsRetired(t *testing.T) {
	var s shells
	t.Cleanup(s.close)
	dir := t.TempDir()
	sh, err := s.start(s.attach("s1", nil, nil), "", "/bin/sh", "true", dir, filepath.Join(dir, "logs"))
	require.NoError(t, err)
	<-sh.done
	s.mu.Lock()
	defer s.mu.Unlock()
	assert.True(t, sh.groupGone)
	assert.False(t, sh.leftChildren())
}

// TestShells_ExitCodeWithAChildHoldingOutput: a shell that exits 0 while a
// child it left holds the output pipe still reports 0.
func TestShells_ExitCodeWithAChildHoldingOutput(t *testing.T) {
	var s shells
	t.Cleanup(s.close)
	dir := t.TempDir()
	sh, err := s.start(s.attach("s1", nil, nil), "", "/bin/sh", "sleep 10 &", dir, filepath.Join(dir, "logs"))
	require.NoError(t, err)
	select {
	case <-sh.done:
	case <-time.After(10 * time.Second):
		t.Fatal("the shell never ended")
	}
	assert.Equal(t, 0, sh.exitCode)
}

// TestShells_StartsRacingTheCloseStop: shells starting while their session
// closes are stopped, never left running.
func TestShells_StartsRacingTheCloseStop(t *testing.T) {
	var s shells
	dir := t.TempDir()
	pids := filepath.Join(dir, "pids")
	ss := s.attach("s1", nil, nil)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			_, _ = s.start(ss, "", "/bin/sh", "echo $$ >> "+pids+"; sleep 30", dir, filepath.Join(dir, "logs"))
		})
	}
	time.Sleep(5 * time.Millisecond)
	s.forget("s1")
	wg.Wait()

	data, err := os.ReadFile(pids)
	if errors.Is(err, os.ErrNotExist) {
		return // none ran
	}
	require.NoError(t, err)
	for _, field := range strings.Fields(string(data)) {
		pid, err := strconv.Atoi(field)
		require.NoError(t, err)
		deadline := time.Now().Add(5 * time.Second)
		for syscall.Kill(pid, 0) == nil && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		assert.True(t, errors.Is(syscall.Kill(pid, 0), syscall.ESRCH), "shell %d still runs", pid)
	}
}

// TestShells_QueuedStartAfterCloseIsRefused: a start a run still had
// queued when its session closed is refused, and the session's next run
// starts shells again.
func TestShells_QueuedStartAfterCloseIsRefused(t *testing.T) {
	var s shells
	t.Cleanup(s.close)
	dir := t.TempDir()
	old := s.attach("s1", nil, nil)
	s.forget("s1")
	_, err := s.start(old, "", "/bin/sh", "sleep 30", dir, filepath.Join(dir, "logs"))
	require.Error(t, err)
	assert.Empty(t, s.sessions, "the closed session is not made again")

	sh, err := s.start(s.attach("s1", nil, nil), "", "/bin/sh", "true", dir, filepath.Join(dir, "logs"))
	require.NoError(t, err)
	<-sh.done
}

// TestShells_CanceledWaitTakesNothing: a wait canceled as its shell ends
// leaves the end to the notification.
func TestShells_CanceledWaitTakesNothing(t *testing.T) {
	var s shells
	t.Cleanup(s.close)
	told := make(chan string, 1)
	ss := s.attach("s1", func(text string) (func(), bool) { told <- text; return func() {}, false }, nil)
	dir := t.TempDir()
	sh, err := s.start(ss, "", "/bin/sh", "sleep 0.3", dir, filepath.Join(dir, "logs"))
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err = s.waitFor(ctx, time.Minute, "s1", []string{sh.id})
	require.ErrorIs(t, err, context.Canceled)
	select {
	case text := <-told:
		assert.Contains(t, text, `"state":"exited"`)
	case <-time.After(5 * time.Second):
		t.Fatal("the end never reached the agent")
	}
}

// TestShells_CloseWaitsForStarts: a close returns only once the shells
// still starting are stopped.
func TestShells_CloseWaitsForStarts(t *testing.T) {
	var s shells
	ss := s.attach("s1", nil, nil)
	ss.starts.Add(1) // a start under way
	closed := make(chan struct{})
	go func() { s.forget("s1"); close(closed) }()
	select {
	case <-closed:
		t.Fatal("the close did not wait for the start")
	case <-time.After(100 * time.Millisecond):
	}
	ss.starts.Done()
	<-closed
}

// TestShells_CanceledWaitSendsAHeldEnd: a shell that ends while a wait is
// registered has its notification held back; a cancel of that wait sends
// it.
func TestShells_CanceledWaitSendsAHeldEnd(t *testing.T) {
	var s shells
	t.Cleanup(s.close)
	told := make(chan string, 1)
	ss := s.attach("s1", func(text string) (func(), bool) { told <- text; return func() {}, false }, nil)
	dir := t.TempDir()
	sh, err := s.start(ss, "", "/bin/sh", "sleep 30", dir, filepath.Join(dir, "logs"))
	require.NoError(t, err)

	s.mu.Lock()
	sh.waiters++ // a wait registered as the shell ends
	s.mu.Unlock()
	_, err = s.stop("s1", sh.id, false)
	require.NoError(t, err)
	select {
	case <-told:
		t.Fatal("told while the wait holds the end")
	case <-time.After(100 * time.Millisecond):
	}
	s.mu.Lock()
	sh.waiters--
	s.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err = s.waitFor(ctx, time.Minute, "s1", []string{sh.id})
	require.ErrorIs(t, err, context.Canceled)
	select {
	case text := <-told:
		assert.Contains(t, text, `"state":"killed"`)
	case <-time.After(5 * time.Second):
		t.Fatal("the held end was lost")
	}
}

// TestShells_LeftGroupIsRetiredWhenItEmpties: an ended shell's group that
// children kept is retired once they are gone, without a stop.
func TestShells_LeftGroupIsRetiredWhenItEmpties(t *testing.T) {
	var s shells
	t.Cleanup(s.close)
	dir := t.TempDir()
	sh, err := s.start(s.attach("s1", nil, nil), "", "/bin/sh", "sleep 1 >/dev/null 2>&1 &", dir, filepath.Join(dir, "logs"))
	require.NoError(t, err)
	<-sh.done
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		gone := sh.groupGone
		s.mu.Unlock()
		if gone {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the group was never retired")
}
