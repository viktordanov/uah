package main_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/testing/fakellm"
	"github.com/viktordanov/uah/testing/harnesstest"
)

// TestCrashRecovery kills uah (SIGKILL, no graceful stop) while the embedded
// engine runs a Bash tool, then resumes the session with a new uah and a new
// message: the resumed run succeeds, the model sees the interrupted call with
// a result, and the tool's process does not outlive the recovery. (Before
// the embedded engine killed orphans on resume, the runner's store marked the
// call failed but left its process running.)
func TestCrashRecovery(t *testing.T) {
	t.Parallel()
	c := crashDuringTool(t)

	res := uahWith(t, c.env, "", append(c.args, "--session", c.session, "carry on")...)
	require.Equal(t, 0, res.code, res.stderr)
	assert.Contains(t, res.stdout, "recovered")

	reqs := c.llm.Requests()
	require.Len(t, reqs, 2)
	last := reqs[1]
	assert.Equal(t, []string{"start a long command", "carry on"}, last.UserTexts, "the resumed run sees the history")
	assert.Equal(t, []string{"call-1-0"}, last.CallIDs, "the interrupted call is in the history")
	require.Len(t, last.ToolOutputs, 1, "and it has a result")
	assert.Contains(t, last.ToolOutputs[0], "interrupted before an exit status was recorded")

	deadline := time.Now().Add(10 * time.Second)
	for alive(c.pid) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	assert.False(t, alive(c.pid), "the orphaned tool was killed")
}

// TestCrashRecovery_LeavesAGroupRecordedInAnEarlierBoot: the session file
// says the tool's process group was recorded in another boot, as it would be
// after a reboot, when the ID can belong to an unrelated process. The resume
// leaves the group alone (uagent compares the recorded leader start).
func TestCrashRecovery_LeavesAGroupRecordedInAnEarlierBoot(t *testing.T) {
	t.Parallel()
	c := crashDuringTool(t)
	file := filepath.Join(c.stateDir, "sessions", c.session+".session.jsonl")
	data, err := os.ReadFile(file)
	require.NoError(t, err)
	start := regexp.MustCompile(`"ProcessGroupStart":"[^"/]+/`)
	require.True(t, start.Match(data), "the runner recorded the leader's start")
	data = start.ReplaceAll(data, []byte(`"ProcessGroupStart":"00000000-0000-0000-0000-000000000000/`))
	require.NoError(t, os.WriteFile(file, data, 0o600))

	res := uahWith(t, c.env, "", append(c.args, "--session", c.session, "carry on")...)
	require.Equal(t, 0, res.code, res.stderr)
	assert.Contains(t, res.stdout, "recovered")
	assert.True(t, alive(c.pid), "a group recorded in another boot is not signaled")
}

// crashed is a uah run killed while its Bash tool runs.
type crashed struct {
	llm      *fakellm.Server
	env      []string
	args     []string
	stateDir string
	session  string
	pid      int // the tool's
}

// crashDuringTool kills uah (SIGKILL, no graceful stop) once the runner has
// recorded the tool's process group, and leaves the tool running.
func crashDuringTool(t *testing.T) crashed {
	t.Helper()
	e := harnesstest.NewEnv(t)
	llm := fakellm.New(t,
		fakellm.Reply{Text: "Waiting.", Commands: []string{"echo $$ > sleep.pid; exec sleep 30"}},
		fakellm.Reply{Text: "recovered"},
	)
	env := []string{
		"OPENAI_API_KEY=test-key",
		"UAH_STATE_DIR=" + e.StateDir,
		"UAH_HOME=" + filepath.Join(e.StateDir, "..", "home"),
		"UAH_LLM_PROVIDER=", "UAH_LLM_MODEL=",
	}
	// No sandbox: under bwrap's PID namespace, $$ would not be the host's PID.
	args := []string{"run", "-q", "--provider", "openai", "--model", "gpt-test", "--base-url", llm.URL, "-C", e.Workspace, "--yolo"}

	first := exec.Command(uahBin, append(args, "start a long command")...)
	first.Env = append(os.Environ(), env...)
	require.NoError(t, first.Start())
	pid := waitPID(t, filepath.Join(e.Workspace, "sleep.pid"))
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	// Crash once the runner has recorded the tool's process group. A crash
	// before that leaves nothing that says which process to kill; the
	// runner then reports "process start was not recorded".
	waitRecorded(t, e.StateDir)
	require.NoError(t, first.Process.Kill())
	_ = first.Wait()
	require.True(t, alive(pid), "the crash leaves the tool running")

	return crashed{llm: llm, env: env, args: args, stateDir: e.StateDir, session: onlySession(t, e.StateDir), pid: pid}
}

// waitPID waits for the tool to write its process ID.
func waitPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(path); err == nil && strings.HasSuffix(string(data), "\n") {
			pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
			require.NoError(t, err)

			return pid
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the tool never started")

	return 0
}

// waitRecorded waits for the session file to record a process group.
func waitRecorded(t *testing.T, stateDir string) {
	t.Helper()
	recorded := regexp.MustCompile(`"ProcessGroupID":[1-9]`)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		files, _ := filepath.Glob(filepath.Join(stateDir, "sessions", "*.session.jsonl"))
		for _, f := range files {
			if data, err := os.ReadFile(f); err == nil && recorded.Match(data) {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the tool's process group was never recorded")
}

func alive(pid int) bool {
	err := syscall.Kill(pid, 0)

	return err == nil || errors.Is(err, syscall.EPERM)
}

func onlySession(t *testing.T, stateDir string) string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(stateDir, "sessions", "*.session.jsonl"))
	require.NoError(t, err)
	require.Len(t, files, 1)

	return strings.TrimSuffix(filepath.Base(files[0]), ".session.jsonl")
}
