package embedded

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/engine"
)

// Background shells (the [design](../../../docs/design/background-shells.md)):
// a Bash call with background set starts its command here, detached from
// the run, so it outlives the call and the run and keeps the run from
// nothing. Each session has its own; they end when their session closes
// (Forget) or the engine does (Close), which is what ctrl+c's quit does.
// When one exits, the session's agent gets a <background_shell_notification>
// through Options.Inject, as a subagent's notification reaches it: into the
// live run, else with the next message, starting no run. A shell's end
// comes from its process's Wait; the one check on a clock is an ended
// shell's process group while children it left run there (watchGroup),
// which nothing else reports. The model is never woken by either.

const (
	// maxShells is how many background shells of a session may run.
	maxShells = 16
	// shellLogLimit caps a shell's log; output past it is dropped.
	shellLogLimit = 256 << 20
	// shellTail is how much of a shell's output its end carries.
	shellTail = 2000
	// killGrace is how long a stopped shell has after SIGTERM before
	// SIGKILL.
	killGrace = 3 * time.Second
	// pipeGrace is how long a shell's output pipe may stay open after its
	// process exits before it is closed.
	pipeGrace = 2 * time.Second
	// maxShellWait is the longest wait_shell, under the wake hold, as
	// wait_agent's.
	maxShellWait = 270 * time.Second
	minShellWait = time.Second
)

var errUnknownShell = errors.New("no such background shell in this session; shells do not survive a restart of uah")

// shells are the engine's background shells, by session.
type shells struct {
	mu       sync.Mutex
	sessions map[string]*sessionShells
}

// sessionShells are one session's shells, and how to reach its agent and
// its stream, from its latest run.
type sessionShells struct {
	// closed is set once the session's shells are stopped: a start under
	// way then stops what it started.
	closed bool
	next   int
	// starting counts the shells starting, which take a slot already;
	// starts ends each once its shell is registered or stopped, so a
	// close waits for them.
	starting int
	starts   sync.WaitGroup
	byID     map[string]*shell
	order    []string
	inject   func(string) (func(), bool)
	notify   func(core.Event)
}

// shell is one background shell.
type shell struct {
	id, command, log string
	callID           string
	group            int
	// ss is its session's shells.
	ss      *sessionShells
	started time.Time
	done    chan struct{}

	// Set once done is closed. groupGone is set once its process group
	// was seen empty after its end: the ID may then be another's, so it is
	// never signalled again.
	ended     time.Time
	exitCode  int
	killed    bool
	groupGone bool

	// waiters counts the wait_shell calls waiting on it, and told is set
	// once the agent heard of its end, or is being told; consumed once a
	// wait_shell returned its end, and withdraw takes back a notification
	// still held.
	waiters  int
	told     bool
	consumed bool
	withdraw func()
}

func (s *shells) of(sessionID string) *sessionShells {
	if s.sessions == nil {
		s.sessions = map[string]*sessionShells{}
	}
	ss, ok := s.sessions[sessionID]
	if !ok {
		ss = &sessionShells{byID: map[string]*shell{}}
		s.sessions[sessionID] = ss
	}

	return ss
}

// attach records how to reach a session's agent and stream, a run's
// Options.Inject and Notify, which outlive the run, and returns the
// session's shells, which the run's starts go to: once its session
// closes, a start the run still had queued is refused, and the session's
// next run gets new ones.
func (s *shells) attach(sessionID string, inject func(string) (func(), bool), notify func(core.Event)) *sessionShells {
	s.mu.Lock()
	defer s.mu.Unlock()
	ss := s.of(sessionID)
	if inject != nil {
		ss.inject = inject
	}
	if notify != nil {
		ss.notify = notify
	}

	return ss
}

// start runs command in dir through the shell (a sandboxing shell or the
// real one), as one of the session's shells (attach), with its output in a
// log under logDir, and returns it once it runs.
func (s *shells) start(ss *sessionShells, callID, shellPath, command, dir, logDir string) (*shell, error) {
	s.mu.Lock()
	running := ss.starting
	for _, sh := range ss.byID {
		select {
		case <-sh.done:
		default:
			running++
		}
	}
	if running >= maxShells {
		s.mu.Unlock()

		return nil, fmt.Errorf("%d background shells already run in this session; stop one with kill_shell first", maxShells)
	}
	if ss.closed {
		s.mu.Unlock()

		return nil, errors.New("the session closed")
	}
	// The slot is taken under the lock that counted it, so parallel calls
	// cannot pass the cap together.
	ss.starting++
	ss.starts.Add(1)
	defer ss.starts.Done()
	id, logPath, f, err := ss.newLog(logDir)
	s.mu.Unlock()
	release := func() {
		s.mu.Lock()
		ss.starting--
		s.mu.Unlock()
	}
	if err != nil {
		release()

		return nil, err
	}
	out := &cappedWriter{w: f, left: shellLogLimit}
	cmd := exec.Command(shellPath, "-c", command) //nolint:gosec,noctx // the model's command, approved as Bash's; it outlives every context
	cmd.Dir, cmd.Stdout, cmd.Stderr = dir, out, out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// A descendant that left the group with the output pipe open (a new
	// session) would hold Wait, and the shell's end, forever.
	cmd.WaitDelay = pipeGrace
	if err := cmd.Start(); err != nil {
		_ = f.Close()
		release()

		return nil, fmt.Errorf("failed to start the command: %w", err)
	}
	sh := &shell{
		id: id, command: command, log: logPath, callID: callID, group: cmd.Process.Pid, ss: ss, started: time.Now(),
		done: make(chan struct{}),
	}
	s.mu.Lock()
	ss.starting--
	ss.byID[id], ss.order = sh, append(ss.order, id)
	notify, closed := ss.notify, ss.closed
	if closed {
		sh.killed, sh.told = true, true
	}
	first := sh.update() // before its end can be seen
	s.mu.Unlock()
	if closed { // the session closed while it started
		go s.wait(sh, cmd, f)
		sh.signal()

		return nil, errors.New("the session closed while the shell started; it was stopped")
	}
	// The running update goes out before the end's can: the session sees
	// them in order.
	if notify != nil {
		notify(first)
	}
	go s.wait(sh, cmd, f)

	return sh, nil
}

// newLog numbers the next shell and creates its log. A number whose log
// exists is skipped: the session's operation directory outlives uah's
// process, and a transcript still names a log from before a restart. It
// holds s.mu.
func (ss *sessionShells) newLog(logDir string) (string, string, *os.File, error) {
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return "", "", nil, fmt.Errorf("failed to create the shell's log directory: %w", err)
	}
	for {
		ss.next++
		id := "shell-" + strconv.Itoa(ss.next)
		path := filepath.Join(logDir, id+".log")
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // under the session's operation directory
		switch {
		case err == nil:
			return id, path, f, nil
		case !errors.Is(err, fs.ErrExist):
			return "", "", nil, fmt.Errorf("failed to create the shell's log: %w", err)
		}
	}
}

// wait records the shell's end and tells its session, and its agent unless
// a wait_shell returns the end, the agent stopped it, or its session
// closed. A shell the user stopped is told: the agent did not stop it.
func (s *shells) wait(sh *shell, cmd *exec.Cmd, f *os.File) {
	err := cmd.Wait()
	_ = f.Close()
	// The exit status is the process's, also when Wait gave up on an
	// output pipe a child held (exec.ErrWaitDelay).
	code := -1
	if st := cmd.ProcessState; st != nil {
		code = st.ExitCode()
		if ws, ok := st.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			code = 128 + int(ws.Signal())
		}
	} else if err == nil {
		code = 0
	}
	gone := !groupAlive(sh.group)
	s.mu.Lock()
	sh.ended, sh.exitCode, sh.groupGone = time.Now(), code, gone
	close(sh.done)
	notify := sh.ss.notify
	tell := sh.waiters == 0 && !sh.told
	if tell {
		sh.told = true
	}
	update := sh.update()
	s.mu.Unlock()
	// The agent is told first, so the session's update comes once the
	// notification is in the run or held.
	if tell {
		s.tell(sh)
	}
	if notify != nil {
		notify(update)
	}
	if !gone {
		go s.watchGroup(sh)
	}
}

// tell sends the agent a shell's end; the caller marked it told. A
// wait_shell that returned the end while it went out takes it back.
func (s *shells) tell(sh *shell) {
	s.mu.Lock()
	inject, end := sh.ss.inject, sh.end()
	s.mu.Unlock()
	if inject == nil {
		return
	}
	text, err := engine.ShellNotification(end)
	if err != nil {
		return
	}
	withdraw, _ := inject(text)
	s.mu.Lock()
	consumed := sh.consumed
	if !consumed {
		sh.withdraw = withdraw
	}
	s.mu.Unlock()
	if consumed && withdraw != nil {
		withdraw()
	}
}

// groupCheck is how often an ended shell's process group is checked while
// children it left run there.
const groupCheck = time.Second

// watchGroup retires an ended shell's process group once its last member
// is gone. Nothing reports that for processes uah did not start, so it is
// checked each second: the group is then signalled only while it is this
// shell's, give or take that second, never after another process may have
// taken its ID.
func (s *shells) watchGroup(sh *shell) {
	t := time.NewTicker(groupCheck)
	defer t.Stop()
	for range t.C {
		s.mu.Lock()
		gone := sh.groupGone || !groupAlive(sh.group)
		sh.groupGone = gone
		s.mu.Unlock()
		if gone {
			return
		}
	}
}

// find is a session's shell by ID.
func (s *shells) find(sessionID, id string) (*shell, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ss, ok := s.sessions[sessionID]; ok {
		if sh, ok := ss.byID[strings.TrimSpace(id)]; ok {
			return sh, nil
		}
	}

	return nil, fmt.Errorf("%s: %w", id, errUnknownShell)
}

// owns reports whether a process group is one of the session's shells that
// still run, so a kill of it runs outside the sandbox (commands.ownKill).
func (s *shells) owns(sessionID string, group int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	ss, ok := s.sessions[sessionID]
	if !ok {
		return false
	}
	for _, sh := range ss.byID {
		if sh.group != group {
			continue
		}
		select {
		case <-sh.done:
		default:
			return true
		}
	}

	return false
}

// wait_shell: waitFor returns the named shells' ends once one of them has
// ended, or the timeout passes, and marks the ends it returns as told,
// taking back their notifications if they are still held. A canceled wait
// returns ctx's error and takes nothing: the call's result never reaches
// the agent, so the notifications must.
func (s *shells) waitFor(ctx context.Context, timeout time.Duration, sessionID string, ids []string) ([]engine.ShellEnd, bool, error) {
	if len(ids) == 0 {
		return nil, false, errors.New("shell_ids must name at least one shell")
	}
	list := make([]*shell, 0, len(ids))
	for _, id := range ids {
		sh, err := s.find(sessionID, id)
		if err != nil {
			return nil, false, err
		}
		list = append(list, sh)
	}
	s.mu.Lock()
	for _, sh := range list {
		sh.waiters++
	}
	s.mu.Unlock()
	ended := make(chan struct{}, len(list))
	stop := make(chan struct{})
	defer close(stop)
	for _, sh := range list {
		go func() {
			select {
			case <-sh.done:
				ended <- struct{}{}
			case <-stop:
			}
		}()
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	timedOut := false
	select {
	case <-ended:
	case <-timer.C:
		timedOut = true
	case <-ctx.Done():
	}
	if err := ctx.Err(); err != nil {
		// Checked whatever woke the wait: a canceled call's result never
		// reaches the agent, so the ends stay the notifications'.
		s.releaseWait(list)

		return nil, false, err
	}
	// The waiters go under the lock that reads the ends: a shell that
	// ends after this is not in the result, and so gets its notification.
	s.mu.Lock()
	out := make([]engine.ShellEnd, 0, len(list))
	var withdraws []func()
	for _, sh := range list {
		sh.waiters--
		out = append(out, sh.end())
		select {
		case <-sh.done:
			sh.told, sh.consumed = true, true
			if sh.withdraw != nil {
				withdraws = append(withdraws, sh.withdraw)
				sh.withdraw = nil
			}
		default:
		}
	}
	s.mu.Unlock()
	for _, w := range withdraws {
		w()
	}

	return out, timedOut, nil
}

// releaseWait ends a canceled wait on the shells: it takes no end, and
// sends an end it held back.
func (s *shells) releaseWait(list []*shell) {
	var untold []*shell
	s.mu.Lock()
	for _, sh := range list {
		sh.waiters--
		select {
		case <-sh.done:
			if sh.waiters == 0 && !sh.told {
				sh.told = true
				untold = append(untold, sh)
			}
		default:
		}
	}
	s.mu.Unlock()
	for _, sh := range untold {
		s.tell(sh)
	}
}

// stop ends a shell's process group, SIGTERM and then SIGKILL, and waits
// for its end; a shell the agent stops (byAgent) sends no notification,
// one the user stops does. A shell that already ended still has its group
// signalled while children it left run there.
func (s *shells) stop(sessionID, id string, byAgent bool) (engine.ShellEnd, error) {
	sh, err := s.find(sessionID, id)
	if err != nil {
		return engine.ShellEnd{}, err
	}
	s.mu.Lock()
	select {
	case <-sh.done:
		end := sh.end()
		left := sh.leftChildren()
		s.mu.Unlock()
		if left {
			sh.signal() // children it left in its group
		}

		return end, nil
	default:
	}
	sh.killed = true
	if byAgent {
		sh.told = true
	}
	s.mu.Unlock()
	sh.signal()
	s.mu.Lock()
	defer s.mu.Unlock()

	return sh.end(), nil
}

// signal sends the shell's process group SIGTERM, then SIGKILL after the
// grace, and returns once the shell ended. The shell's own end does not
// end the group: a child that ignores SIGTERM keeps it, so the group gets
// SIGKILL at the grace while any member is left.
func (sh *shell) signal() {
	deadline := time.Now().Add(killGrace)
	_ = syscall.Kill(-sh.group, syscall.SIGTERM)
	select {
	case <-sh.done:
	case <-time.After(killGrace):
	}
	if groupAlive(sh.group) {
		time.Sleep(time.Until(deadline))
		if groupAlive(sh.group) {
			_ = syscall.Kill(-sh.group, syscall.SIGKILL)
		}
	}
	<-sh.done
}

// leftChildren reports whether an ended shell's process group still has
// members, its children; once seen empty, the group is retired, since its
// ID may later be another process's. It holds s.mu.
func (sh *shell) leftChildren() bool {
	if sh.groupGone {
		return false
	}
	if !groupAlive(sh.group) {
		sh.groupGone = true

		return false
	}

	return true
}

// groupAlive reports whether a process group has a member left.
func groupAlive(group int) bool {
	err := syscall.Kill(-group, 0)

	return err == nil || errors.Is(err, syscall.EPERM)
}

// forget stops a session's shells and drops them: the session closed.
func (s *shells) forget(sessionID string) {
	s.mu.Lock()
	ss, ok := s.sessions[sessionID]
	delete(s.sessions, sessionID)
	s.mu.Unlock()
	if ok {
		s.stopAll(ss)
	}
}

// close stops every session's shells: the engine closed.
func (s *shells) close() {
	s.mu.Lock()
	all := s.sessions
	s.sessions = nil
	s.mu.Unlock()
	var wg sync.WaitGroup
	for _, ss := range all {
		wg.Go(func() { s.stopAll(ss) })
	}
	wg.Wait()
}

// stopAll stops a session's shells that still run, and the children an
// ended shell left in its process group.
func (s *shells) stopAll(ss *sessionShells) {
	var running []*shell
	s.mu.Lock()
	ss.closed = true
	for _, sh := range ss.byID {
		select {
		case <-sh.done:
			if sh.leftChildren() {
				running = append(running, sh) // children it left in its group
			}

			continue
		default:
		}
		sh.killed, sh.told = true, true // its session closes
		running = append(running, sh)
	}
	s.mu.Unlock()
	var wg sync.WaitGroup
	for _, sh := range running {
		wg.Go(sh.signal)
	}
	wg.Wait()
	ss.starts.Wait() // a shell still starting sees closed and stops itself
}

// update is the shell as its session's stream shows it. It holds s.mu.
func (sh *shell) update() engine.ShellUpdated {
	u := engine.ShellUpdated{
		At: time.Now(), ID: sh.id, Command: sh.command, State: engine.ShellRunning,
		Group: sh.group, Log: sh.log, Started: sh.started, CallID: sh.callID,
	}
	select {
	case <-sh.done:
		u.State, u.ExitCode, u.Ended = engine.ShellExited, sh.exitCode, sh.ended
		if sh.killed {
			u.State = engine.ShellKilled
		}
	default:
	}

	return u
}

// end is the shell as wait_shell and its notification give it. It holds
// s.mu.
func (sh *shell) end() engine.ShellEnd {
	e := engine.ShellEnd{ID: sh.id, Command: sh.command, State: engine.ShellRunning, Log: sh.log}
	select {
	case <-sh.done:
		code := sh.exitCode
		e.State, e.ExitCode, e.Ran = engine.ShellExited, &code, sh.ended.Sub(sh.started).Round(time.Second).String()
		if sh.killed {
			e.State = engine.ShellKilled
		}
		e.Tail = tail(sh.log, shellTail)
	default:
		e.Ran = time.Since(sh.started).Round(time.Second).String()
	}

	return e
}

// tail is the last n bytes of a file, from the start of a line.
func tail(path string, n int64) string {
	f, err := os.Open(path) //nolint:gosec // the shell's own log
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return ""
	}
	off := max(info.Size()-n, 0)
	data := make([]byte, info.Size()-off)
	if _, err := f.ReadAt(data, off); err != nil && !errors.Is(err, io.EOF) {
		return ""
	}
	if off > 0 {
		if i := slices.Index(data, '\n'); i >= 0 {
			data = data[i+1:]
		}
	}

	return strings.ToValidUTF8(string(data), "�")
}

// cappedWriter writes until its limit, then drops the rest with one note.
type cappedWriter struct {
	mu   sync.Mutex
	w    io.Writer
	left int64
	cut  bool
}

func (c *cappedWriter) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.left <= 0 {
		if !c.cut {
			c.cut = true
			_, _ = io.WriteString(c.w, "\nuah: the log reached its limit; later output was dropped\n")
		}

		return len(p), nil
	}
	n := min(int64(len(p)), c.left)
	if _, err := c.w.Write(p[:n]); err != nil {
		return 0, err
	}
	c.left -= n

	return len(p), nil
}
