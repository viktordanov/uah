// Package toolbridge is how a command reaches its session's tools: the
// session listens on a Unix socket in its private $TMPDIR, and `uah tool`,
// run inside any of the session's commands, sends it one request and
// prints the answer, so a script can call the session's MCP tools and
// filter their results before the model reads them. The [design] has the
// reasons; internal/engine/embedded serves the requests.
//
// [design]: ../../docs/design/tool-bridge.md
package toolbridge

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"
)

// SocketName is the socket's name in the session's $TMPDIR; it may be a
// symlink to the socket when the full path is too long for one.
const SocketName = "uah-tools.sock"

// TokenVar is the variable that holds the session's token in its
// commands' environment. A request must carry it: on Linux, any command
// of the user can reach any socket, so the token is what keeps one
// session's commands, such as a restricted subagent's, from another's
// tools.
const TokenVar = "UAH_TOOL_TOKEN"

// Ops a request names.
const (
	OpList     = "list"
	OpDescribe = "describe"
	OpCall     = "call"
)

// Request is one request: list the tools, describe one, or call one with
// its arguments (a JSON object).
type Request struct {
	// Token is the session's (TokenVar): only its commands have it.
	Token     string          `json:"token,omitempty"`
	Op        string          `json:"op"`
	Name      string          `json:"name,omitempty"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// Response is the answer: the result, or why there is none.
type Response struct {
	OK bool `json:"ok"`
	// Tools are the callable tools, for list.
	Tools []Tool `json:"tools,omitempty"`
	// Tool is the described tool, for describe.
	Tool *Tool `json:"tool,omitempty"`
	// Text is a call's result, as the tool returned it.
	Text string `json:"text,omitempty"`
	// Images counts the images a call returned, which a command cannot
	// show.
	Images int `json:"images,omitempty"`
	// Error says why the request failed: the tool's own error, a refusal
	// by the policy, a hook, or the user, or a bad request.
	Error string `json:"error,omitempty"`
}

// Tool is a callable tool: its name, what it does, and its input schema.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema,omitempty"`
}

// maxMessage caps a request or a response line.
const maxMessage = 64 << 20

// ErrNoSession means no session answers: the command does not run in a
// uah session that offers its tools.
var ErrNoSession = errors.New("no uah session answers here: uah tool works inside the commands of a session that offers it")

// Do sends the request to the session whose $TMPDIR is dir and returns its
// answer. It connects through a path relative to dir, so the socket's full
// path may be longer than a socket address can be.
func Do(ctx context.Context, dir string, req Request) (Response, error) {
	conn, err := dial(ctx, dir)
	if err != nil {
		return Response{}, err
	}
	defer func() { _ = conn.Close() }()
	if err := WriteMessage(conn, req); err != nil {
		return Response{}, fmt.Errorf("failed to send the request: %w", err)
	}
	var resp Response
	if err := ReadMessage(bufio.NewReader(conn), &resp); err != nil {
		return Response{}, fmt.Errorf("the session did not answer: %w", err)
	}

	return resp, nil
}

// dial connects to the socket in dir. A Unix socket's address holds about
// 100 bytes, so it connects from inside dir by the socket's name; a
// relative address resolves against the process's directory, which this
// short-lived command may change.
func dial(ctx context.Context, dir string) (net.Conn, error) {
	if dir == "" {
		return nil, ErrNoSession
	}
	if _, err := os.Stat(filepath.Join(dir, SocketName)); err != nil {
		return nil, ErrNoSession
	}
	wd, err := os.Getwd()
	if err == nil {
		defer func() { _ = os.Chdir(wd) }()
	}
	if err := os.Chdir(dir); err != nil {
		return nil, fmt.Errorf("failed to reach the session: %w", err)
	}
	conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", SocketName)
	if err != nil {
		return nil, fmt.Errorf("%w (%v)", ErrNoSession, err)
	}

	return conn, nil
}

// WriteMessage writes v as one JSON line.
func WriteMessage(w io.Writer, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = w.Write(append(data, '\n'))

	return err
}

// ReadMessage reads one JSON line into v.
func ReadMessage(r *bufio.Reader, v any) error {
	var line []byte
	for {
		chunk, isPrefix, err := r.ReadLine()
		if err != nil {
			return err
		}
		line = append(line, chunk...)
		if len(line) > maxMessage {
			return errors.New("the message is too long")
		}
		if !isPrefix {
			break
		}
	}

	return json.Unmarshal(line, v)
}
