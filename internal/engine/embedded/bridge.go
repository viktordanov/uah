package embedded

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/viktordanov/uah-core/harness/tool"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/approval"
	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/hooks"
	"github.com/viktordanov/uah/internal/mcp"
	"github.com/viktordanov/uah/internal/toolbridge"
	"github.com/viktordanov/uah/internal/toolpolicy"
)

// The tool bridge (internal/toolbridge, the [design](../../../docs/design/tool-bridge.md)):
// each session that offers it listens on a Unix socket in its private
// $TMPDIR, and `uah tool`, run inside the session's commands, calls the
// session's MCP tools through it, so a script can call them and filter
// their results before the model reads anything. A call goes the way the
// model's own call of the tool goes: the tool policy and the scope, the
// PreToolUse hooks, and the tool's approval_mode, which asks the user. Its
// result goes to the command's output, under Bash's limits.

// maxSocketPath is the longest socket path the bridge binds; a longer one
// gets a short private directory under /tmp and a symlink in $TMPDIR
// (macOS allows 104 bytes, Linux 108).
const maxSocketPath = 100

// bridges are the engine's tool bridges, by session.
type bridges struct {
	mu       sync.Mutex
	sessions map[string]*bridge
	closed   bool
}

// bridge is one session's: its socket, and what its latest run allows.
type bridge struct {
	ln net.Listener
	// sockets are the paths a sandboxed command connects through (the
	// policy's Sockets), and remove the files to delete when it closes.
	sockets []string
	remove  []string
	// bin holds the uah command (BinDir), "" when uah's own path is not
	// known, and token is what a request must carry, which only the
	// session's commands get (toolbridge.TokenVar).
	bin   string
	token string
	calls atomic.Int64

	mu    sync.Mutex
	state bridgeState
	// approvals are the tools approved for a command, by command and tool
	// (approve).
	approvals map[string]*commandApproval
	ctx       context.Context
	stop      context.CancelFunc
	wg        sync.WaitGroup
}

// bridgeState is what the session's latest run allows: its MCP tools, the
// resource tools and the servers they may reach, the gate that asks, and
// the hooks; notify reaches the session's stream.
type bridgeState struct {
	manager   *mcp.Manager
	tools     []mcp.Tool
	resources []string
	servers   func(string) bool
	gate      mcpGate
	hooks     *hooks.Runner
	hookBase  hooks.Input
	notify    func(core.Event)
}

// open starts the session's bridge, if it has none yet, in its $TMPDIR,
// and returns it; opsDir holds its bin directory.
func (b *bridges) open(sessionID, tempDir, opsDir string) (*bridge, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, errors.New("the engine closed")
	}
	if br, ok := b.sessions[sessionID]; ok {
		return br, nil
	}
	br, err := listenBridge(tempDir, opsDir)
	if err != nil {
		return nil, err
	}
	if b.sessions == nil {
		b.sessions = map[string]*bridge{}
	}
	b.sessions[sessionID] = br
	br.wg.Go(br.serve)

	return br, nil
}

// listenBridge binds the socket in tempDir, or in a short private
// directory with a symlink in tempDir, and makes the bin directory.
func listenBridge(tempDir, opsDir string) (*bridge, error) {
	if err := os.MkdirAll(tempDir, 0o700); err != nil {
		return nil, fmt.Errorf("failed to create the temporary directory: %w", err)
	}
	want := filepath.Join(tempDir, toolbridge.SocketName)
	_ = os.Remove(want) // a stale socket or link from an earlier process
	br := &bridge{}
	path := want
	if len(want) > maxSocketPath {
		// /tmp, not $TMPDIR: uah may run inside another session's command,
		// whose $TMPDIR is as long as this one.
		short, err := os.MkdirTemp("/tmp", "uah-")
		if err != nil {
			return nil, fmt.Errorf("failed to create the bridge's directory: %w", err)
		}
		path = filepath.Join(short, "s")
		if len(path) > maxSocketPath {
			_ = os.RemoveAll(short)

			return nil, fmt.Errorf("no path short enough for the bridge's socket (%s)", path)
		}
		if err := os.Symlink(path, want); err != nil {
			_ = os.RemoveAll(short)

			return nil, fmt.Errorf("failed to link the bridge's socket: %w", err)
		}
		br.remove = append(br.remove, want, short)
	}
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "unix", path)
	if err != nil {
		for _, r := range br.remove {
			_ = os.RemoveAll(r)
		}

		return nil, fmt.Errorf("failed to listen for uah tool: %w", err)
	}
	_ = os.Chmod(path, 0o600)
	br.ln = ln
	br.remove = append(br.remove, path)
	br.sockets = []string{want, path}
	if resolved, err := filepath.EvalSymlinks(path); err == nil && resolved != path {
		br.sockets = append(br.sockets, resolved) // Seatbelt matches resolved paths
	}
	br.bin = bridgeBin(opsDir)
	token := make([]byte, 24)
	if _, err := rand.Read(token); err != nil {
		_ = ln.Close()

		return nil, fmt.Errorf("failed to make the bridge's token: %w", err)
	}
	br.token = hex.EncodeToString(token)
	br.ctx, br.stop = context.WithCancel(context.Background())

	return br, nil
}

// bridgeBin makes <opsDir>/bin with a uah link to this executable, so a
// command finds the same uah first on PATH; "" when it cannot. It lies
// outside the writable roots, so a sandboxed command cannot replace it.
func bridgeBin(opsDir string) string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	bin := filepath.Join(opsDir, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		return ""
	}
	link := filepath.Join(bin, "uah")
	if cur, err := os.Readlink(link); err == nil && cur == exe {
		return bin
	}
	_ = os.Remove(link)
	if err := os.Symlink(exe, link); err != nil {
		return ""
	}

	return bin
}

// update sets what the session's latest run allows.
func (br *bridge) update(s bridgeState) {
	br.mu.Lock()
	br.state = s
	br.mu.Unlock()
}

// forget closes a session's bridge: the session closed.
func (b *bridges) forget(sessionID string) {
	b.mu.Lock()
	br := b.sessions[sessionID]
	delete(b.sessions, sessionID)
	b.mu.Unlock()
	if br != nil {
		br.close()
	}
}

// close closes every bridge: the engine closed.
func (b *bridges) close() {
	b.mu.Lock()
	all := b.sessions
	b.sessions, b.closed = nil, true
	b.mu.Unlock()
	for _, br := range all {
		br.close()
	}
}

func (br *bridge) close() {
	br.stop()
	_ = br.ln.Close()
	br.wg.Wait()
	for _, r := range br.remove {
		_ = os.RemoveAll(r)
	}
}

// serve answers each connection on its own goroutine until the bridge
// closes.
func (br *bridge) serve() {
	for {
		conn, err := br.ln.Accept()
		if err != nil {
			return
		}
		br.wg.Go(func() { br.handle(conn) })
	}
}

// handle reads one request and writes its answer. A call is canceled when
// the command hangs up, as when it is stopped.
func (br *bridge) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	// The bridge's close ends every exchange, also one that never sends
	// its request or never reads its answer.
	stop := context.AfterFunc(br.ctx, func() { _ = conn.Close() })
	defer stop()
	r := bufio.NewReader(conn)
	var req toolbridge.Request
	if err := toolbridge.ReadMessage(r, &req); err != nil {
		_ = toolbridge.WriteMessage(conn, toolbridge.Response{Error: "a bad request: " + err.Error()})

		return
	}
	cmd, ours := commandOf(conn)
	if subtle.ConstantTimeCompare([]byte(req.Token), []byte(br.token)) != 1 || !ours {
		_ = toolbridge.WriteMessage(conn, toolbridge.Response{Error: "this command is not one of this session's (" + toolbridge.TokenVar + " does not match)"})

		return
	}
	ctx, cancel := context.WithCancel(br.ctx)
	defer cancel()
	go func() { // the command sends nothing more: a read ends when it hangs up
		_, _ = r.ReadByte()
		cancel()
	}()
	_ = toolbridge.WriteMessage(conn, br.answer(context.WithValue(ctx, commandKey{}, cmd), req))
}

func (br *bridge) answer(ctx context.Context, req toolbridge.Request) toolbridge.Response {
	br.mu.Lock()
	s := br.state
	br.mu.Unlock()
	switch req.Op {
	case toolbridge.OpList:
		return toolbridge.Response{OK: true, Tools: s.list(false)}
	case toolbridge.OpDescribe:
		for _, t := range s.list(true) {
			if t.Name == req.Name {
				return toolbridge.Response{OK: true, Tool: &t}
			}
		}

		return toolbridge.Response{Error: unknownTool(req.Name)}
	case toolbridge.OpCall:
		return br.call(ctx, s, req)
	}

	return toolbridge.Response{Error: fmt.Sprintf("unknown op %q: list, describe, or call", req.Op)}
}

// list is the callable tools, by name, with their schemas when full.
func (s bridgeState) list(full bool) []toolbridge.Tool {
	var out []toolbridge.Tool
	for _, t := range s.tools {
		bt := toolbridge.Tool{Name: t.Name, Description: t.Description}
		if bt.Description == "" {
			bt.Description = fmt.Sprintf("The %s tool of the %s MCP server.", t.Tool, t.Server)
		}
		if full {
			bt.InputSchema = t.InputSchema
		}
		out = append(out, bt)
	}
	for _, d := range resourceTools() {
		if slices.Contains(s.resources, d.Tool.Name) {
			bt := toolbridge.Tool{Name: d.Tool.Name, Description: d.Tool.Description}
			if full {
				bt.InputSchema = d.Tool.Parameters
			}
			out = append(out, bt)
		}
	}
	slices.SortFunc(out, func(a, b toolbridge.Tool) int { return strings.Compare(a.Name, b.Name) })

	return out
}

func unknownTool(name string) string {
	return fmt.Sprintf("no tool %q is callable here; uah tool list names the ones that are", name)
}

// call runs one call as the model's would run: the hooks, the policy, the
// approval, then the server.
func (br *bridge) call(ctx context.Context, s bridgeState, req toolbridge.Request) toolbridge.Response {
	start := time.Now()
	resp := br.callTool(ctx, s, req)
	if s.notify != nil {
		args := string(req.Arguments)
		if r := []rune(args); len(r) > 300 {
			args = string(r[:299]) + "…"
		}
		s.notify(engine.ToolBridgeCalled{At: time.Now(), Name: req.Name, Arguments: args, OK: resp.OK, Error: resp.Error, Duration: time.Since(start)})
	}

	return resp
}

func (br *bridge) callTool(ctx context.Context, s bridgeState, req toolbridge.Request) toolbridge.Response {
	if s.manager == nil {
		return toolbridge.Response{Error: "this session has no MCP servers"}
	}
	args := bytes.TrimSpace(req.Arguments)
	if len(args) == 0 {
		args = []byte("{}")
	}
	if !json.Valid(args) || args[0] != '{' {
		return toolbridge.Response{Error: "the arguments must be a JSON object"}
	}
	isResource := mcp.IsResourceTool(req.Name)
	i := slices.IndexFunc(s.tools, func(t mcp.Tool) bool { return t.Name == req.Name })
	if i < 0 && (!isResource || !slices.Contains(s.resources, req.Name)) {
		return toolbridge.Response{Error: unknownTool(req.Name)}
	}
	// The PreToolUse hooks first, as for the model's call.
	ctx, args, refusal := br.preToolUse(ctx, s, req.Name, args)
	if refusal != "" {
		return toolbridge.Response{Error: refusal}
	}
	if isResource {
		return callResource(ctx, s, req.Name, args)
	}
	t := s.tools[i]
	if reason := br.approve(ctx, s, t, string(args)); reason != "" {
		return toolbridge.Response{Error: reason}
	}
	r, err := s.manager.Call(ctx, t.Server, t.Tool, args)
	switch {
	case err != nil:
		return toolbridge.Response{Error: err.Error()}
	case r.IsError:
		return toolbridge.Response{Error: r.Text}
	}

	return toolbridge.Response{OK: true, Text: r.Text, Images: len(r.Images)}
}

// bridgeNote ends Bash's description while the run offers the bridge, so
// the model knows of `uah tool` also without the prepared context.
const bridgeNote = " Inside a command, `uah tool` calls this session's MCP tools: `uah tool list`, `uah tool describe <name>`, " +
	"`uah tool call <name> '<json>'`, so a script can call them and filter or join their results (jq, python) before you read them. " +
	"Use it when a result is large or paged, or when one call's result feeds the next."

// withBridgeNote adds bridgeNote to Bash's description.
func withBridgeNote(r tool.Registry) tool.Registry { return bridgeNoteRegistry{r} }

type bridgeNoteRegistry struct{ tool.Registry }

func (r bridgeNoteRegistry) StaticDefinitions() []tool.Definition {
	defs := r.Registry.StaticDefinitions()
	for i, d := range defs {
		if d.Tool.Name == tool.BashName {
			defs[i].Tool.Description += bridgeNote
		}
	}

	return defs
}

// preToolUse runs the tool's PreToolUse hooks for a call: it returns the
// context, which an allow marks, the arguments the hooks left, and why the
// call may not run, or "".
func (br *bridge) preToolUse(ctx context.Context, s bridgeState, name string, args []byte) (context.Context, []byte, string) {
	if s.hooks == nil || !s.hooks.Has(hooks.PreToolUse, name) {
		return ctx, args, ""
	}
	in := s.hookBase
	in.ToolName, in.ToolUseID, in.ToolInput = name, "bridge-"+strconv.FormatInt(br.calls.Add(1), 10), json.RawMessage(args)
	d := s.hooks.Run(ctx, in)
	switch {
	case d.Block:
		return ctx, args, "blocked by a PreToolUse hook: " + d.Reason
	case ctx.Err() != nil:
		return ctx, args, "the call was stopped before its hooks finished; it did not run"
	}
	if d.Allow {
		ctx = context.WithValue(ctx, hookAllowKey{}, true)
	}
	if len(d.UpdatedInput) > 0 {
		args = d.UpdatedInput
	}

	return ctx, args, ""
}

// callResource runs a resource request, as Codex's resource tools do.
func callResource(ctx context.Context, s bridgeState, name string, args []byte) toolbridge.Response {
	plan, err := resourcePlan(name, string(args))
	if err != nil {
		return toolbridge.Response{Error: err.Error()}
	}
	if s.servers != nil && (plan.Server == "" || !s.servers(plan.Server)) {
		return toolbridge.Response{Error: "name an MCP server that the tool policy allows as a whole (mcp__<server>__*); for this server, " + toolpolicy.Refused}
	}
	text, err := resourceJob(ctx, s.manager, plan)
	if err != nil {
		return toolbridge.Response{Error: err.Error()}
	}

	return toolbridge.Response{OK: true, Text: text}
}

// commandOf names the command at the other end of conn, the process of
// it that uah started (its PID and start time, so a reused PID is another
// command), and reports whether it descends from this uah: the token keeps
// sessions of one uah apart, and this keeps another uah's commands out,
// which could read its own state directory's tokens but not this one's
// process tree. Where the peer is unknown (no peer PID on the platform),
// the token alone decides and the name is "". A command that left the
// tree, as a daemon does, is refused.
func commandOf(conn net.Conn) (string, bool) {
	pid, ok := peerPID(conn)
	if !ok {
		_, known := parentPID(os.Getpid())

		return "", !known // a platform without peer PIDs
	}
	self := os.Getpid()
	if pid == self {
		return "self", true // uah's own calls, as a test's
	}
	for range 256 {
		if pid <= 1 {
			return "", false
		}
		parent, ok := parentPID(pid)
		if !ok || parent == pid {
			return "", false
		}
		if parent == self {
			return strconv.Itoa(pid) + "@" + procStart(pid), true
		}
		pid = parent
	}

	return "", false
}

// commandKey carries commandOf's name of the calling command in a call's
// context.
type commandKey struct{}

// commandApproval is a tool's approval for one command: the first call asks,
// holding mu, and the calls that wait on it run unasked once it approved.
type commandApproval struct {
	mu       sync.Mutex
	approved bool
}

// maxApprovals bounds the approvals a bridge remembers; past it, it
// forgets them all and commands ask again.
const maxApprovals = 4096

// approve decides a call of t as the gate does, once per command and tool:
// a script fires its calls together, and each asking at once (the
// auto-reviewer's deadline, a prompt per call) would refuse some of the
// same calls the first one's answer covers. A refusal is not kept: the
// next call asks again.
func (br *bridge) approve(ctx context.Context, s bridgeState, t mcp.Tool, args string) string {
	cmd, _ := ctx.Value(commandKey{}).(string)
	if cmd == "" {
		return s.gate.check(ctx, t, args)
	}
	key := cmd + "\x00" + t.Name
	br.mu.Lock()
	if len(br.approvals) > maxApprovals {
		br.approvals = nil
	}
	if br.approvals == nil {
		br.approvals = map[string]*commandApproval{}
	}
	a, ok := br.approvals[key]
	if !ok {
		a = &commandApproval{}
		br.approvals[key] = a
	}
	br.mu.Unlock()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.approved {
		return ""
	}
	// Only an answer to an ask is kept: a hook's allowance is for its call's
	// arguments, and yolo, the scope, or a tool that needs no approval are
	// checked again with each call, so a change to them applies at once.
	gate := s.gate
	asked := false
	if next := gate.ask; next != nil {
		gate.ask = func(ctx context.Context, p approval.Prompt) approval.Answer {
			answer := next(ctx, p)
			asked = answer.Approved()

			return answer
		}
	}
	reason := gate.check(ctx, t, args)
	a.approved = reason == "" && asked

	return reason
}
