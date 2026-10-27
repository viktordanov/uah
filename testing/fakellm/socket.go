package fakellm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"time"

	"github.com/coder/websocket"
)

// serveSocket serves the Responses WebSocket when WebSocket is set, as the
// ChatGPT backend does, and else refuses the upgrade with a 426, as a
// backend without it does. Each response.create message is a request,
// answered with the events HTTP would stream. A request that continues a
// response of the connection (previous_response_id) is recorded whole,
// with that response's input and output before the items it sent; its
// Arrival counts the message and the items.
func (s *Server) serveSocket(w http.ResponseWriter, r *http.Request) {
	if !s.WebSocket {
		w.WriteHeader(http.StatusUpgradeRequired)

		return
	}
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionNoContextTakeover})
	if err != nil {
		return
	}
	defer func() { _ = c.CloseNow() }()
	c.SetReadLimit(-1)
	socket := int(s.sockets.Add(1))
	// The connection outlives the upgrade's request; the client closing it
	// ends ctx.
	ctx, cancel := context.WithCancel(context.WithoutCancel(r.Context()))
	defer cancel()
	messages := make(chan []byte)
	go func() {
		defer cancel() // the client closed the connection
		for {
			_, data, err := c.Read(ctx)
			if err != nil {
				return
			}
			select {
			case messages <- data:
			case <-ctx.Done():
				return
			}
		}
	}()
	kept := map[string][]json.RawMessage{} // each response's input and output
	for {
		var data []byte
		select {
		case data = <-messages:
		case <-ctx.Done():
			return
		}
		arrived := time.Now()
		create, err := readCreate(data, kept)
		if err != nil {
			_ = c.Write(ctx, websocket.MessageText, socketError(Reply{Fail: http.StatusBadRequest, FailCode: "previous_response_not_found"}))

			continue
		}
		var req Request
		if !s.Light {
			req = parseRequest(create.body)
		}
		req.Authorization, req.TurnState = r.Header.Get("Authorization"), create.turnState
		n, reply := s.take(req, Arrival{At: arrived, Bytes: len(data), Parse: time.Since(arrived), Socket: socket, Previous: create.previous, Items: create.items})
		if reply.Fail != 0 && reply.Fail != http.StatusOK {
			_ = c.Write(ctx, websocket.MessageText, socketError(reply))

			continue
		}
		if reply.TurnState != "" { // the backend's headers come as an event
			event, err := json.Marshal(struct {
				Type    string            `json:"type"`
				Headers map[string]string `json:"headers"`
			}{"codex.response.metadata", map[string]string{"x-codex-turn-state": reply.TurnState}})
			if err != nil {
				panic(err) // a struct of strings always encodes
			}
			_ = c.Write(ctx, websocket.MessageText, event)
		}
		sw := &socketWriter{c: c, ctx: ctx, header: http.Header{}}
		s.answer(sw, r.WithContext(ctx), n, reply)
		if sw.hijacked {
			return
		}
		output := create.input
		for _, item := range response(n, reply).Output {
			raw, err := json.Marshal(item)
			if err != nil {
				panic(err) // a struct of strings always encodes
			}
			output = append(output, raw)
		}
		kept[fmt.Sprintf("resp-%d", n)] = output
	}
}

// create is a response.create message as a request body.
type create struct {
	body      []byte
	input     []json.RawMessage // the whole input, the continued response's first
	previous  string
	items     int // the input items sent
	turnState string
}

func readCreate(data []byte, kept map[string][]json.RawMessage) (create, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return create{}, err
	}
	var c create
	var input []json.RawMessage
	_ = json.Unmarshal(fields["input"], &input)
	_ = json.Unmarshal(fields["previous_response_id"], &c.previous)
	var metadata map[string]string
	_ = json.Unmarshal(fields["client_metadata"], &metadata)
	c.turnState, c.items = metadata["x-codex-turn-state"], len(input)
	if c.previous != "" {
		before, ok := kept[c.previous]
		if !ok {
			return create{}, fmt.Errorf("no response %s on this connection", c.previous)
		}
		input = slices.Concat(before, input)
	}
	c.input = input
	for _, name := range []string{"type", "previous_response_id", "client_metadata"} {
		delete(fields, name)
	}
	var err error
	if fields["input"], err = json.Marshal(input); err != nil {
		return create{}, err
	}
	c.body, err = json.Marshal(fields)

	return c, err
}

// socketError is the error event that stands for an HTTP error answer.
func socketError(reply Reply) []byte {
	event := struct {
		Type    string            `json:"type"`
		Status  int               `json:"status"`
		Error   apiError          `json:"error"`
		Headers map[string]string `json:"headers,omitempty"`
	}{Type: "error", Status: reply.Fail, Error: replyError(reply)}
	if reply.RetryAfter != "" {
		event.Headers = map[string]string{"retry-after": reply.RetryAfter}
	}
	data, err := json.Marshal(event)
	if err != nil {
		panic(err) // a struct of strings and numbers always encodes
	}

	return data
}

// socketWriter sends each SSE frame written to it as a WebSocket message,
// so an answer streams the same events on both transports.
type socketWriter struct {
	c        *websocket.Conn
	ctx      context.Context
	header   http.Header
	buf      []byte
	hijacked bool
}

func (w *socketWriter) Header() http.Header { return w.header }
func (w *socketWriter) WriteHeader(int)     {}
func (w *socketWriter) Flush()              {}

func (w *socketWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := bytes.Index(w.buf, []byte("\n\n"))
		if i < 0 {
			return len(p), nil
		}
		frame := bytes.TrimPrefix(w.buf[:i], []byte("data: "))
		if err := w.c.Write(w.ctx, websocket.MessageText, frame); err != nil {
			return 0, err
		}
		w.buf = w.buf[i+2:]
	}
}

// Hijack gives the connection, so a reply can drop or freeze it: reading
// waits for the client to close it.
func (w *socketWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	w.hijacked = true

	return socketConn{Conn: websocket.NetConn(w.ctx, w.c, websocket.MessageText), ctx: w.ctx}, nil, nil
}

type socketConn struct {
	net.Conn

	ctx context.Context
}

func (c socketConn) Read([]byte) (int, error) {
	<-c.ctx.Done()

	return 0, io.EOF
}
