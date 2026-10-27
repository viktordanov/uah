// Package fakellm is a scripted OpenAI Responses API for tests. Point the
// runner's openai provider at URL (with any API key) and every model request
// gets the next Reply, so the real runner and the embedded engine can be
// driven by the same script and compared.
package fakellm

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

// Reply is one model response: tool calls, then a message. A reply with
// calls is a tool turn; the message is the final answer otherwise.
type Reply struct {
	Text     string
	Commands []string
	// Escalated are Bash calls that ask to run outside the sandbox, with
	// sandbox_permissions "require_escalated" and a justification.
	Escalated []string
	// Calls are function calls to any tool, after the Bash calls.
	Calls []Call
	// Gate, when set, holds the response until it is closed or the request
	// is canceled, so a test can act while the model is "thinking".
	Gate <-chan struct{}
	// InputTokens, when set, is the usage the response reports as input
	// tokens (default 100 per request so far).
	InputTokens int
	// From, when set, builds the reply from the request, such as a call
	// that needs an ID from an earlier tool result.
	From func(Request) Reply
	// Fail, when set, is the HTTP status of an error answer with FailCode
	// as the Responses API error code, as a provider rejects a request;
	// 200 streams a response.failed instead. RetryAfter is its Retry-After.
	Fail       int
	FailCode   string
	RetryAfter string
	// FailBody, when set, is the error answer's body as sent, such as the
	// ChatGPT backend's {"detail":"..."}.
	FailBody string
	// TurnState, when set, is the x-codex-turn-state header of the answer,
	// the ChatGPT backend's sticky-routing token.
	TurnState string
	// NoUsage leaves the usage out of the response, as some providers do.
	NoUsage bool
	// Drop closes the connection without an answer, as a lost network
	// does; Cut closes it halfway through the stream. The client retries
	// either, so the next reply answers the same request.
	Drop bool
	Cut  bool
	// Freeze takes the connection and never answers; NoEnd ends the stream
	// without its completed response.
	Freeze bool
	NoEnd  bool
	// Deltas stream the message in these pieces before the response
	// completes (Text defaults to them joined), and Reasoning streams a
	// reasoning summary the same way, before the message. Pace waits
	// between pieces; Hold, when set, holds the response after the pieces
	// until it is closed or the request is canceled. With Cut, the
	// connection closes after the pieces. See stream.go.
	Deltas    []string
	Reasoning []string
	Pace      time.Duration
	Hold      <-chan struct{}
	// ArgDeltas stream each call's arguments (a custom call's input) in these pieces.
	ArgDeltas []string
	// Searches are hosted web searches, streamed and listed before the
	// other output items, as the provider runs them.
	Searches []Search
	// Compaction, when set, answers with one compaction item with this
	// encrypted content and nothing else, as the Responses API answers a
	// request that ends with a compaction_trigger item.
	Compaction string
}

// Search is a web_search_call item: Action is search (the default),
// open_page, or find_in_page.
type Search struct {
	Action  string
	Query   string
	URL     string
	Pattern string
}

// Call is a function call to a tool by name, with JSON arguments, or with
// Custom a custom tool call, whose Args are its raw input.
type Call struct {
	Name   string
	Args   string
	Custom bool
}

// Request is what the harness sent, reduced to what tests check.
type Request struct {
	Model       string
	Effort      string
	ServiceTier string
	// Verbosity is text.verbosity ("": no text field).
	Verbosity string
	// UserTexts are the user messages in the request input, in order.
	UserTexts []string
	// System is the system prompt (the system messages in the input).
	System string
	// DeveloperTexts are the developer messages in the input, in order.
	DeveloperTexts []string
	// ToolOutputs are the tool results in the input, in order.
	ToolOutputs []string
	// CallIDs are the tool calls in the input, in order.
	CallIDs []string
	// ToolImages are the image URLs in the tool results, in order.
	ToolImages []string
	// Tools are the offered tools' parameter schemas as JSON, by name.
	Tools map[string]string
	// ToolNames are the offered tools' names in order, and ToolDefs the
	// tools as sent.
	ToolNames []string
	ToolDefs  []json.RawMessage
	// EffortUpdates are the efforts of the configuration_update items in
	// the input, in order.
	EffortUpdates []string
	// Input are the input items as sent, in order.
	Input []json.RawMessage
	// CacheKey is the prompt cache key.
	CacheKey string
	// Authorization is the request's Authorization header.
	Authorization string
	// TurnState is the request's x-codex-turn-state header.
	TurnState string
}

// Arrival is when a request reached the server, before its body was read:
// the moment the harness had built and sent it. Bytes is the body's size,
// and Parse how long the server took to read it. It is kept apart from
// Request, which tests compare.
type Arrival struct {
	At    time.Time
	Bytes int
	Parse time.Duration
	// Encoding is the body's Content-Encoding; Bytes counts it as sent.
	Encoding string
	// Over the WebSocket, Bytes is the message's size, Socket numbers its
	// connection (from 1), Previous is the response it continued, and Items
	// counts the input items it sent.
	Socket   int
	Previous string
	Items    int
}

// Server serves the script. When the script runs out, it answers "done".
type Server struct {
	URL string
	// Light, set before the first request, leaves request bodies unparsed:
	// a request is empty (its Arrival is kept), and routes never match. A performance harness sets it so the fake model costs little
	// next to what it measures.
	Light bool
	// RejectEffortUpdates, set before the first request, answers every
	// request with a configuration_update item with a 400 that names it, as
	// a backend without effort updates does, without taking a reply from
	// the script.
	RejectEffortUpdates bool
	// RejectZstd, set before the first request, answers a body compressed
	// with zstd with a 415, as a backend that cannot read it does.
	RejectZstd bool
	// WebSocket, set before the first request, serves the Responses
	// WebSocket (socket.go); unset, an upgrade gets a 426.
	WebSocket bool

	srv     *httptest.Server
	conns   atomic.Int64
	sockets atomic.Int64

	mu       sync.Mutex
	replies  []Reply
	routes   []route
	requests []Request
	arrivals []Arrival
	seen     chan int
}

// route is a script for the requests whose user messages contain match.
type route struct {
	match   string
	replies []Reply
}

// Route answers the requests whose user messages contain match with its
// own replies, then "done", so one server can serve a parent session and
// the subagents it starts with those messages.
func (s *Server) Route(match string, replies ...Reply) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.routes = append(s.routes, route{match: match, replies: replies})
}

// next takes the reply for req from its route or the main script. It holds
// s.mu.
func (s *Server) next(req Request) Reply {
	script := &s.replies
	for i, r := range s.routes {
		if slices.ContainsFunc(req.UserTexts, func(t string) bool { return strings.Contains(t, r.match) }) {
			script = &s.routes[i].replies

			break
		}
	}
	reply := Reply{Text: "done"}
	if len(*script) > 0 {
		reply, *script = (*script)[0], (*script)[1:]
	}

	return reply
}

// New starts a server that closes with the test.
func New(tb testing.TB, replies ...Reply) *Server {
	tb.Helper()
	s := Start(replies...)
	tb.Cleanup(s.Close)

	return s
}

// Start starts a server outside a test, such as in tools/perf; Close
// stops it.
func Start(replies ...Reply) *Server {
	s := &Server{replies: replies, seen: make(chan int, 1024)}
	s.srv = httptest.NewUnstartedServer(http.HandlerFunc(s.serve))
	s.srv.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		switch state {
		case http.StateNew:
			s.conns.Add(1)
		case http.StateClosed, http.StateHijacked:
			s.conns.Add(-1)
		default:
		}
	}
	s.srv.Start()
	s.URL = s.srv.URL

	return s
}

// Conns is how many client connections are open, idle ones included.
func (s *Server) Conns() int { return int(s.conns.Load()) }

// Close stops the server and closes its connections.
func (s *Server) Close() { s.srv.Close() }

// Script appends replies to the main script, so a long-lived server can
// serve one run after another.
func (s *Server) Script(replies ...Reply) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.replies = append(s.replies, replies...)
}

// Requests returns the requests so far.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]Request(nil), s.requests...)
}

// Arrivals returns when each request so far arrived, in order.
func (s *Server) Arrivals() []Arrival {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]Arrival(nil), s.arrivals...)
}

// Seen receives the number of each request as it arrives (1 for the first).
func (s *Server) Seen() <-chan int { return s.seen }

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	arrived := time.Now()
	if !strings.HasSuffix(r.URL.Path, "/responses") {
		http.NotFound(w, r)

		return
	}
	if r.Header.Get("Upgrade") != "" {
		s.serveSocket(w, r)

		return
	}
	body, sent, status, err := s.readBody(r)
	if err != nil {
		http.Error(w, err.Error(), status)

		return
	}
	var req Request
	if !s.Light {
		req = parseRequest(body)
	}
	req.Authorization, req.TurnState = r.Header.Get("Authorization"), r.Header.Get("X-Codex-Turn-State")
	n, reply := s.take(req, Arrival{At: arrived, Bytes: sent, Parse: time.Since(arrived), Encoding: r.Header.Get("Content-Encoding")})
	s.answer(w, r, n, reply)
}

// take records request n and takes its reply.
func (s *Server) take(req Request, arrival Arrival) (int, Reply) {
	s.mu.Lock()
	s.requests = append(s.requests, req)
	s.arrivals = append(s.arrivals, arrival)
	n := len(s.requests)
	rejected := s.RejectEffortUpdates && len(req.EffortUpdates) > 0
	reply := Reply{Fail: http.StatusBadRequest, FailBody: rejectedUpdate}
	if !rejected {
		reply = s.next(req)
	}
	s.mu.Unlock()
	if reply.From != nil {
		reply = reply.From(req)
	}
	s.seen <- n

	return n, reply
}

// answer streams request n's reply.
func (s *Server) answer(w http.ResponseWriter, r *http.Request, n int, reply Reply) {
	if reply.Gate != nil {
		select {
		case <-reply.Gate:
		case <-r.Context().Done():
			return
		}
	}
	if reply.Drop || reply.Freeze {
		conn := hijack(w)
		if reply.Freeze {
			_, _ = io.Copy(io.Discard, conn) // until the client gives up
		}
		_ = conn.Close()

		return
	}
	if reply.Fail != 0 && reply.Fail != http.StatusOK {
		failWith(w, reply)

		return
	}
	end := streamEvent{Type: "response.completed", Response: response(n, reply)}
	if reply.Fail == http.StatusOK {
		end.Type, end.Response.Status = "response.failed", "failed"
		end.Response.Error = map[string]string{"code": reply.FailCode, "message": "fakellm: " + reply.FailCode}
	}
	event, err := json.Marshal(end)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)

		return
	}
	streamHeaders(w.Header(), reply)
	if !streamPieces(w, r, n, reply) || reply.NoEnd {
		return // the request was canceled while held, or ends early
	}
	if reply.Cut {
		_, _ = fmt.Fprintf(w, "data: %s", event[:len(event)/2])
		http.NewResponseController(w).Flush() // the connection closes next
		_ = hijack(w).Close()

		return
	}
	_, _ = fmt.Fprintf(w, "data: %s\n\n", event)
}

// streamHeaders are the headers of a streamed answer.
func streamHeaders(h http.Header, reply Reply) {
	h.Set("Content-Type", "text/event-stream")
	if reply.TurnState != "" {
		h.Set("X-Codex-Turn-State", reply.TurnState)
	}
}

// readBody reads the request's body, decoded when it is compressed with
// zstd, and its size as sent; an error comes with the status to answer.
func (s *Server) readBody(r *http.Request) ([]byte, int, int, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, 0, http.StatusBadRequest, err
	}
	switch encoding := r.Header.Get("Content-Encoding"); {
	case encoding == "":
		return body, len(body), 0, nil
	case encoding != "zstd" || s.RejectZstd:
		return nil, 0, http.StatusUnsupportedMediaType, errors.New("unsupported content encoding " + encoding)
	}
	dec, err := zstd.NewReader(nil)
	if err != nil {
		return nil, 0, http.StatusInternalServerError, err
	}
	defer dec.Close()
	decoded, err := dec.DecodeAll(body, nil)
	if err != nil {
		return nil, 0, http.StatusBadRequest, err
	}

	return decoded, len(body), 0, nil
}

// hijack takes the request's connection.
func hijack(w http.ResponseWriter) net.Conn {
	conn, _, err := http.NewResponseController(w).Hijack()
	if err != nil {
		panic(err) // httptest's server supports hijacking
	}

	return conn
}

type (
	streamEvent struct {
		Type     string       `json:"type"`
		Response responseBody `json:"response"`
	}
	responseBody struct {
		ID     string            `json:"id"`
		Object string            `json:"object"`
		Status string            `json:"status"`
		Output []outputItem      `json:"output"`
		Usage  *usage            `json:"usage,omitempty"`
		Error  map[string]string `json:"error,omitempty"`
	}
	outputItem struct {
		ID        string        `json:"id"`
		Type      string        `json:"type"`
		Status    string        `json:"status"`
		CallID    string        `json:"call_id,omitempty"`
		Name      string        `json:"name,omitempty"`
		Arguments string        `json:"arguments,omitempty"`
		Input     string        `json:"input,omitempty"`
		Role      string        `json:"role,omitempty"`
		Phase     string        `json:"phase,omitempty"`
		Content   []contentPart `json:"content,omitempty"`
		Summary   []contentPart `json:"summary,omitempty"`
		Action    *searchAction `json:"action,omitempty"`
		// EncryptedContent is a compaction item's.
		EncryptedContent string `json:"encrypted_content,omitempty"`
	}
	searchAction struct {
		Type    string `json:"type"`
		Query   string `json:"query,omitempty"`
		URL     string `json:"url,omitempty"`
		Pattern string `json:"pattern,omitempty"`
	}
	contentPart struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		Annotations []any  `json:"annotations"`
		Logprobs    []any  `json:"logprobs"`
	}
	usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
		TotalTokens  int `json:"total_tokens"`
	}
)

const completed = "completed"

func response(n int, reply Reply) responseBody {
	if reply.Compaction != "" {
		return responseBody{
			ID: fmt.Sprintf("resp-%d", n), Object: "response", Status: completed, Usage: replyUsage(n, reply),
			Output: []outputItem{{ID: fmt.Sprintf("cmp-%d", n), Type: "compaction", EncryptedContent: reply.Compaction}},
		}
	}
	output := []outputItem{}
	for i := range reply.Searches {
		output = append(output, searchItem(n, i, reply.Searches[i]))
	}
	type bashArgs struct {
		Command       string `json:"command"`
		Permissions   string `json:"sandbox_permissions,omitempty"`
		Justification string `json:"justification,omitempty"`
	}
	calls := make([]bashArgs, 0, len(reply.Commands)+len(reply.Escalated))
	for _, cmd := range reply.Commands {
		calls = append(calls, bashArgs{Command: cmd})
	}
	for _, cmd := range reply.Escalated {
		calls = append(calls, bashArgs{Command: cmd, Permissions: "require_escalated", Justification: "it needs the network"})
	}
	named := make([]Call, 0, len(calls)+len(reply.Calls))
	for _, call := range calls {
		args, err := json.Marshal(call)
		if err != nil {
			panic(err) // a struct of strings always encodes
		}
		named = append(named, Call{Name: "Bash", Args: string(args)})
	}
	named = append(named, reply.Calls...)
	for i, call := range named {
		item := outputItem{
			ID: fmt.Sprintf("fc-%d-%d", n, i), Type: "function_call", Status: completed,
			CallID: fmt.Sprintf("call-%d-%d", n, i), Name: call.Name, Arguments: call.Args,
		}
		if call.Custom {
			item.ID, item.Type, item.Arguments, item.Input = fmt.Sprintf("ctc-%d-%d", n, i), typeCustomCall, "", call.Args
		}
		output = append(output, item)
	}
	if len(reply.Reasoning) > 0 {
		output = append(output, reasoningItem(n, reply))
	}
	if text := reply.text(); text != "" {
		output = append(output, outputItem{
			ID: messageID(n), Type: typeMessage, Role: "assistant", Status: completed, Phase: reply.phase(),
			Content: []contentPart{{Type: "output_text", Text: text, Annotations: []any{}, Logprobs: []any{}}},
		})
	}

	return responseBody{ID: fmt.Sprintf("resp-%d", n), Object: "response", Status: completed, Output: output, Usage: replyUsage(n, reply)}
}

// replyUsage is the usage a reply reports: InputTokens, else 100 per
// request so far, and 10 output tokens; nil with NoUsage.
func replyUsage(n int, reply Reply) *usage {
	if reply.NoUsage {
		return nil
	}
	input := 100 * n
	if reply.InputTokens > 0 {
		input = reply.InputTokens
	}

	return &usage{InputTokens: input, OutputTokens: 10, TotalTokens: input + 10}
}

// failWith answers with the reply's HTTP status and a Responses API error.
// rejectedUpdate is the 400's body for RejectEffortUpdates, in the
// Responses API's shape for an input item of an unknown type.
const rejectedUpdate = `{"error":{"message":"Invalid value: 'configuration_update'. Supported values are: 'message', 'function_call', 'function_call_output', and 'reasoning'.","type":"invalid_request_error","param":"input[2].type","code":"invalid_value"}}`

func failWith(w http.ResponseWriter, reply Reply) {
	body, err := json.Marshal(struct {
		Error apiError `json:"error"`
	}{replyError(reply)})
	if err != nil {
		panic(err) // a struct of strings always encodes
	}
	if reply.FailBody != "" {
		body = []byte(reply.FailBody)
	}
	w.Header().Set("Content-Type", "application/json")
	if reply.RetryAfter != "" {
		w.Header().Set("Retry-After", reply.RetryAfter)
	}
	w.WriteHeader(reply.Fail)
	_, _ = w.Write(body)
}

// apiError is a Responses API error.
type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Type    string `json:"type"`
}

func replyError(reply Reply) apiError {
	return apiError{Code: reply.FailCode, Message: "fakellm: " + reply.FailCode, Type: "invalid_request_error"}
}
