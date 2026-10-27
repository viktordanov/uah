package embedded

import (
	"context"
	"net"
	"net/http"
	"strings"

	"github.com/viktordanov/uah-core/harness/llm/responsesapi"
)

// With Config.ResponsesWebSocket, the openai-codex client sends a run's
// turn requests over the Responses WebSocket, as Codex does
// (responsesapi.WebSocketTransport): one connection for each session, kept
// across its runs, so a request that extends the last one sends only its
// new input. Each attempt's line says how it went (socketSent).

// socket is the session's WebSocket transport to baseURL, made on first
// use; the engine closes them when it closes.
func (e *Engine) socket(sessionID, baseURL string) *responsesapi.WebSocketTransport {
	key := sessionID + "\n" + baseURL
	if ws, ok := e.sockets.Load(key); ok {
		return ws.(*responsesapi.WebSocketTransport) //nolint:forcetypeassert // only sockets are stored
	}
	timeout := headerTimeout(baseURL)
	ws, _ := e.sockets.LoadOrStore(key, &responsesapi.WebSocketTransport{
		HTTP: zstdTransport{base: e.transports.get(timeout)},
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			return dialContext(ctx, network, address)
		},
		ResponseHeaderTimeout: timeout,
		Sent:                  socketSent,
	})

	return ws.(*responsesapi.WebSocketTransport) //nolint:forcetypeassert // only sockets are stored
}

// forgetSockets closes a closed session's sockets, which hold its
// connection and its last request.
func (e *Engine) forgetSockets(sessionID string) {
	e.sockets.Range(func(key, ws any) bool {
		if strings.HasPrefix(key.(string), sessionID+"\n") { //nolint:forcetypeassert // keys are strings
			e.sockets.Delete(key)
			_ = ws.(*responsesapi.WebSocketTransport).Close() //nolint:forcetypeassert // only sockets are stored
		}

		return true
	})
}

// turnSocket sends turn requests over the session's WebSocket, and the
// direct calls, compaction summaries and reviews, over HTTP: their
// histories would end the turns' continuation.
type turnSocket struct {
	ws   *responsesapi.WebSocketTransport
	http http.RoundTripper
}

func (t turnSocket) RoundTrip(req *http.Request) (*http.Response, error) {
	c, _ := req.Context().Value(callKey{}).(*modelCall)
	if c != nil && c.kind == kindTurn {
		return t.ws.RoundTrip(req)
	}
	if c != nil {
		socketSent(req, responsesapi.WebSocketSend{HTTP: true, Reason: c.kind})
	}

	return t.http.RoundTrip(req)
}

// socketSent records in the attempt's line how its request went out: the
// transport, whether it sent its whole input, why, and over the WebSocket
// the bytes it wrote. A request the server made send again adds its
// bytes.
func socketSent(req *http.Request, s responsesapi.WebSocketSend) {
	c, _ := req.Context().Value(callKey{}).(*modelCall)
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.a.Send, c.a.SendReason = "full", s.Reason
	if s.Incremental {
		c.a.Send = "incremental"
	}
	if s.HTTP {
		c.a.Transport = "http"

		return
	}
	if c.a.Transport != "websocket" {
		c.a.Transport, c.a.SentBytes = "websocket", 0
	}
	c.a.SentBytes += s.Bytes
}
