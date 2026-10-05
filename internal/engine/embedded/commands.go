package embedded

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/viktordanov/uah-core/harness/llm"
	"github.com/viktordanov/uah-core/harness/operation"
	"github.com/viktordanov/uah-core/harness/tool"

	"github.com/viktordanov/uah/internal/rules"
)

// commands follows a run's shell commands between the coordinator and the
// operation manager: the process group of each one still running, so the
// session can stop its own commands (ownKill), and, with a limit (the
// scope's CommandTimeout), the time each one runs, so a command that hangs
// is stopped and its call ends. A command stops as an interrupt stops it:
// the operation is canceled, which kills its process group.
type commands struct {
	ctx   context.Context
	limit time.Duration

	mu sync.Mutex
	// groups are the process groups of the commands that run.
	groups map[operation.ID]int
	// timers stop the commands at the limit; timedOut are the ones it
	// stopped.
	timers   map[operation.ID]*time.Timer
	timedOut map[operation.ID]bool
}

func newCommands(ctx context.Context, limit time.Duration) *commands {
	return &commands{ctx: ctx, limit: limit, groups: map[operation.ID]int{}, timers: map[operation.ID]*time.Timer{}, timedOut: map[operation.ID]bool{}}
}

// wrap puts the commands between the coordinator and m.
func (c *commands) wrap(m operation.Manager) operation.Manager {
	cm := &commandManager{Manager: m, c: c, updates: make(chan operation.Operation)}
	go cm.forward()

	return cm
}

// commandManager is the operation manager the coordinator sees: the run's,
// with the commands followed.
type commandManager struct {
	operation.Manager

	c       *commands
	updates chan operation.Operation
}

// Add starts the operation and, for a command under a limit, its timer.
// The timer starts first, so the command's end, which may come before
// Add returns, stops it.
func (m *commandManager) Add(op operation.Operation) error {
	timed := op.Type == operation.TypeShell && !terminal(op.Status) && m.c.limit > 0
	if timed {
		m.c.mu.Lock()
		if _, ok := m.c.timers[op.ID]; ok {
			timed = false
		} else {
			id := op.ID
			m.c.timers[id] = time.AfterFunc(m.c.limit, func() { m.timeOut(id) })
		}
		m.c.mu.Unlock()
	}
	err := m.Manager.Add(op)
	if err != nil && timed {
		m.c.mu.Lock()
		if t, ok := m.c.timers[op.ID]; ok {
			t.Stop()
			delete(m.c.timers, op.ID)
		}
		m.c.mu.Unlock()
	}

	return err
}

// timeOut stops a command that ran past the limit.
func (m *commandManager) timeOut(id operation.ID) {
	m.c.mu.Lock()
	_, running := m.c.timers[id]
	if running {
		m.c.timedOut[id] = true
	}
	m.c.mu.Unlock()
	if running && m.c.ctx.Err() == nil {
		_ = m.Cancel(id, fmt.Sprintf("the command ran past its time limit of %s", m.c.limit))
	}
}

func (m *commandManager) Updates() <-chan operation.Operation { return m.updates }

// forward passes the manager's updates on, noting each command's process
// group while it runs.
func (m *commandManager) forward() {
	defer close(m.updates)
	in := m.Manager.Updates()
	for {
		select {
		case <-m.c.ctx.Done():
			m.c.stopTimers()

			return
		case op, ok := <-in:
			if !ok {
				m.c.stopTimers()

				return
			}
			if op.Type == operation.TypeShell {
				m.c.note(op)
			}
			select {
			case m.updates <- op:
			case <-m.c.ctx.Done():
				m.c.stopTimers()

				return
			}
		}
	}
}

// note records a command's process group while it runs and forgets it,
// with its timer, once it ends.
func (c *commands) note(op operation.Operation) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if terminal(op.Status) {
		delete(c.groups, op.ID)
		if t, ok := c.timers[op.ID]; ok {
			t.Stop()
			delete(c.timers, op.ID)
		}

		return
	}
	if state, err := operation.DecodeShellState(op); err == nil && state.ProcessGroupID > 1 {
		c.groups[op.ID] = state.ProcessGroupID
	}
}

func (c *commands) stopTimers() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, t := range c.timers {
		t.Stop()
		delete(c.timers, id)
	}
}

// TimedOut reports whether the limit stopped the command.
func (c *commands) TimedOut(id operation.ID) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.timedOut[id]
}

// timedBash is the Bash translator whose results say when the limit
// stopped the command.
type timedBash struct {
	tool.Translator

	c *commands
}

// decide passes the decision to the Bash translator under it, so the
// hooks and the prefetcher still see a gated translator.
func (b timedBash) decide(ctx context.Context, call llm.ToolCall) submit {
	if g, ok := b.Translator.(gatedTranslator); ok {
		return g.decide(ctx, call)
	}

	return func(tc tool.Context) tool.CallStatus { return b.Translate(tc, call) }
}

func (b timedBash) TranslateResult(callID string, status tool.CallStatus, ops []operation.Operation) (llm.ToolResult, error) {
	result, err := b.Translator.TranslateResult(callID, status, ops)
	if err == nil && len(ops) == 1 && b.c.TimedOut(ops[0].ID) {
		result.Output = append(result.Output, llm.ToolResultOutput{Kind: llm.ToolResultText, Value: fmt.Sprintf(
			"\nuah: the command ran longer than this session's limit of %s for one command, so it was stopped. Do not run it again as it was.", b.c.limit)})
	}

	return result, err
}

// owns reports whether a process group is one of the run's commands that
// still run.
func (c *commands) owns(group int) bool {
	if c == nil || group <= 1 {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, g := range c.groups {
		if g == group {
			return true
		}
	}

	return false
}

// ownKill reports whether a command only signals the session's own
// commands, and returns it rewritten from what was checked: each of its
// simple commands is kill, with at most one signal option, and each PID
// (or -group) it names is in the process group of one of the run's
// commands that still run. Such a command runs outside the sandbox
// without asking: the sandbox lets a command signal only its own
// children, so a session could not otherwise stop a command it started
// earlier, and stopping it changes nothing but that command. The
// command that runs is the rewritten one, `kill -s NAME -- PID...` for
// each, never the model's text, which the shell could expand into
// other targets (a brace, a variable).
func (c *commands) ownKill(command string) (string, bool) {
	if c == nil {
		return "", false
	}
	simple, ok := rules.Split(command)
	if !ok || len(simple) == 0 {
		return "", false
	}
	out := make([]string, 0, len(simple))
	for _, argv := range simple {
		signal, pids, ok := killTargets(argv)
		if !ok {
			return "", false
		}
		words := []string{"kill", "-s", signal, "--"}
		for _, pid := range pids {
			group := -pid
			if pid > 0 {
				g, err := syscall.Getpgid(pid)
				if err != nil {
					return "", false
				}
				group = g
			}
			if !c.owns(group) {
				return "", false
			}
			words = append(words, strconv.Itoa(pid))
		}
		out = append(out, strings.Join(words, " "))
	}

	return strings.Join(out, "; "), true
}

// killTargets reads a kill command: its signal's name (TERM when it names
// none) and its PIDs, negative for a process group. ok is false for any
// other command, a signal that is not one, or any word that is not a
// plain signal or number. One signal option may come first (-TERM,
// -SIGTERM, -9, -s TERM, -n 9), then "--".
func killTargets(argv []string) (string, []int, bool) {
	if len(argv) < 2 || argv[0] != "kill" {
		return "", nil, false
	}
	args, signal := argv[1:], "TERM"
	switch {
	case args[0] == "-s" || args[0] == "-n":
		if len(args) < 2 {
			return "", nil, false
		}
		signal, args = args[1], args[2:]
	case args[0] != "--" && strings.HasPrefix(args[0], "-") && len(args) > 1:
		signal, args = args[0][1:], args[1:]
	}
	signal, ok := signalName(signal)
	if !ok {
		return "", nil, false
	}
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		return "", nil, false
	}
	pids := make([]int, 0, len(args))
	for _, a := range args {
		n, err := strconv.Atoi(a)
		if err != nil || strconv.Itoa(n) != a || n >= -1 && n <= 1 {
			return "", nil, false
		}
		pids = append(pids, n)
	}

	return signal, pids, true
}

// signalName is a signal's name without SIG, from its name (any case,
// with or without SIG) or its number; ok is false for anything else.
func signalName(s string) (string, bool) {
	if n, err := strconv.Atoi(s); err == nil {
		name := unix.SignalName(syscall.Signal(n))
		if n <= 0 || name == "" {
			return "", false
		}

		return strings.TrimPrefix(name, "SIG"), true
	}
	name := strings.TrimPrefix(strings.ToUpper(s), "SIG")
	for _, r := range name {
		if (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return "", false
		}
	}
	if name == "" || unix.SignalNum("SIG"+name) == 0 {
		return "", false
	}

	return name, true
}

// terminal reports whether an operation has ended.
func terminal(s operation.Status) bool {
	return s == operation.StatusCompleted || s == operation.StatusFailed || s == operation.StatusCanceled
}
