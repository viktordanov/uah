package embedded

import (
	"slices"
	"sync"

	"github.com/viktordanov/uah-core/harness/llm"

	"github.com/viktordanov/uah/internal/compaction"
	"github.com/viktordanov/uah/internal/contextusage"
)

// lastRequests keep each session's latest model request, as sent after any
// compaction, and the input tokens reported for it, for /context, with its
// response's output and the session's effort for it, for the advisor
// (advisor.go). Subagents share their parent's engine, so requests are
// kept by session.
type lastRequests struct {
	mu       sync.Mutex
	sessions map[string]lastRequest
}

type lastRequest struct {
	req    llm.Request
	input  int64
	output []llm.Item
	// effort is the session's effort for the request, before adaptive
	// effort lowered it ("ultra" at ultra); remote the remote compaction
	// item the transport put in place of its placeholder (none: zero).
	effort string
	remote remoteItem
}

// recorder returns the switcher hook that records a session's requests.
func (l *lastRequests) recorder(sessionID string) func(llm.Request, llm.Response, string, remoteItem) {
	return func(req llm.Request, resp llm.Response, effort string, item remoteItem) {
		l.mu.Lock()
		defer l.mu.Unlock()
		if l.sessions == nil {
			l.sessions = map[string]lastRequest{}
		}
		l.sessions[sessionID] = lastRequest{req: req, input: resp.Usage.InputTokens, output: resp.Output, effort: effort, remote: item}
	}
}

// forget drops a session's last request.
func (l *lastRequests) forget(sessionID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.sessions, sessionID)
}

// transcript is what the advisor reads for the call callID: the session's
// last request as sent, without its system message, which comes apart,
// then its response up to and including the call, and a placeholder
// result for each call in it still without one, with the remote
// compaction item the request carried. ok is false when that
// response does not hold the call: a later request replaced it, or none
// was recorded.
func (l *lastRequests) transcript(sessionID, callID string) (advisorTranscript, bool) {
	l.mu.Lock()
	last, ok := l.sessions[sessionID]
	l.mu.Unlock()
	if !ok {
		return advisorTranscript{}, false
	}
	at := slices.IndexFunc(last.output, func(item llm.Item) bool {
		c, ok := item.Data.(llm.ToolCall)
		return ok && item.Type == llm.ItemToolCall && c.CallID == callID
	})
	if at < 0 {
		return advisorTranscript{}, false
	}
	t := advisorTranscript{call: callID, model: last.req.Model.ID, effort: last.effort, remote: last.remote}
	history := last.req.Input
	if len(history) > 0 {
		if m, ok := history[0].Data.(llm.Message); ok && m.Role == llm.RoleSystem {
			t.system, history = m.Text, history[1:]
		}
	}
	for _, tool := range last.req.Tools {
		t.tools = append(t.tools, tool.Name)
	}
	t.items = withPendingResults(slices.Concat(history, last.output[:at+1]), callID)

	return t, true
}

// ContextUsage breaks the session's last request down by what fills the
// context. ok is false before its first model request.
func (e *Engine) ContextUsage(sessionID string) (contextusage.Usage, bool) {
	e.last.mu.Lock()
	last, ok := e.last.sessions[sessionID]
	e.last.mu.Unlock()
	if !ok {
		return contextusage.Usage{}, false
	}
	window := compaction.ContextWindow(last.req.Model.ID, e.cfg.ContextWindow, e.models.Window)

	return contextusage.Analyze(last.req, last.input, window, e.cfg.Compaction, e.cfg.InstructionFiles), true
}
