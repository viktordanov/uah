package embedded

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/viktordanov/uah-core/harness/inbox"
	"github.com/viktordanov/uah-core/harness/llm"
	"github.com/viktordanov/uah-core/harness/session"
	"github.com/viktordanov/uah-core/harness/sessionstore"
	"github.com/viktordanov/uah-core/harness/sessionstore/localfile"

	"github.com/viktordanov/uah/internal/compaction"
	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/images"
)

var _ engine.Rewinder = (*Engine)(nil)

var (
	errNotInContext = errors.New("the message is not in the agent's context")
	// errMidTool is Codex's "the selected prompt is a steer": a message that
	// arrived before a tool call the agent had made started cannot be cut
	// at, since the call would run again.
	errMidTool = errors.New("the message reached the agent in the middle of its work; go back to the message that started that work")
)

// Rewind cuts the session's context before the message messageID, as Codex's
// backtrack reverts a thread before a turn: the runner's session items from
// that message (or the first input that went with it) to the last one are
// recorded in the session's rewind log, and every later run's store leaves
// them out (cutStore), so the context builder rebuilds the history without
// them. The session file keeps them. The session must be idle.
func (e *Engine) Rewind(ctx context.Context, sessionID, messageID string) (engine.Rewound, []string, error) {
	dir := e.sessionsDir()
	base, err := localfile.New(dir)
	if err != nil {
		return engine.Rewound{}, nil, fmt.Errorf("failed to open the session store: %w", err)
	}
	store, err := withCuts(base, dir, sessionID)
	if err != nil {
		return engine.Rewound{}, nil, err
	}
	items, err := allItems(ctx, store, session.ID(sessionID))
	if err != nil {
		return engine.Rewound{}, nil, err
	}
	from, held, err := rewindPoint(items, messageID)
	if err != nil {
		return engine.Rewound{}, nil, err
	}
	cut := compaction.Rewind{
		At: time.Now().UTC(), MessageID: messageID, Tokens: usageIn(items[:from]),
		From: uint64(items[from].Sequence), To: uint64(items[len(items)-1].Sequence),
	}
	if err := compaction.OpenRewinds(dir, sessionID).Append(cut); err != nil {
		return engine.Rewound{}, nil, err
	}
	dropped := externalInputs(items[from:])
	e.last.rewind(sessionID, len(dropped), dropped[0], cut.Tokens)

	return engine.Rewound{At: cut.At, MessageID: messageID, Tokens: cut.Tokens}, held, nil
}

// rewindPoint finds where a rewind to the message cuts: at its input, or at
// the first of the inputs just before it, which reached the agent with it.
// A developer message before them stays, such as the prepared context a
// rewind to the first message would otherwise drop.
// held are the texts of those earlier inputs.
func rewindPoint(items []sessionstore.Item, messageID string) (from int, held []string, err error) {
	at := slices.IndexFunc(items, func(it sessionstore.Item) bool {
		in, ok := it.Data.(inbox.Input)

		return ok && in.Kind == inbox.InputExternal && string(in.ID) == messageID
	})
	if at < 0 {
		return 0, nil, errNotInContext
	}
	from = at
	for from > 0 && items[from-1].Kind == sessionstore.ItemInput && !developer(items[from-1]) {
		from--
	}
	if callsWithoutStatus(items[:from]) {
		return 0, nil, errMidTool
	}
	held = externalInputs(items[from:at])

	return from, held, nil
}

// developer reports whether the item is a developer message, the
// harness's context, which a rewind keeps.
func developer(item sessionstore.Item) bool {
	in, ok := item.Data.(inbox.Input)

	return ok && in.Kind == inbox.InputDeveloper
}

// callsWithoutStatus reports whether a tool call among the items has no
// status: the coordinator would run it again on restore.
func callsWithoutStatus(items []sessionstore.Item) bool {
	type key struct {
		turn session.TurnID
		call string
	}
	open := map[key]bool{}
	for _, it := range items {
		switch d := it.Data.(type) {
		case sessionstore.ModelResponse:
			for _, o := range d.Response.Output {
				if c, ok := o.Data.(llm.ToolCall); ok {
					open[key{d.TurnID, c.CallID}] = true
				}
			}
		case sessionstore.ToolCallStatus:
			delete(open, key{d.TurnID, d.CallID})
		}
	}

	return len(open) > 0
}

// externalInputs are the texts of the messages among the items, in order.
func externalInputs(items []sessionstore.Item) []string {
	var out []string
	for _, it := range items {
		in, ok := it.Data.(inbox.Input)
		if !ok || in.Kind != inbox.InputExternal {
			continue
		}
		var text string
		if json.Unmarshal(in.Payload, &text) == nil {
			out = append(out, text)
		}
	}

	return out
}

// settleLocked drops, once per run, the saved compactions that a later
// rewind left stale: one made before a rewind may cover what the rewind
// cut, so it no longer matches the history, and the one before it applies.
// A mismatch without a rewind after it stays for apply to report. It holds
// c.mu.
func (c *compactor) settleLocked(input []llm.Item) {
	if c.settled {
		return
	}
	c.settled = true
	for c.record != nil && c.cuts.After(c.record.At) {
		if _, err := compaction.Apply(input, *c.record); !errors.Is(err, compaction.ErrMismatch) {
			return
		}
		c.record = nil
		if n := len(c.older); n > 0 {
			c.record, c.older = &c.older[n-1], c.older[:n-1]
		}
	}
}

// cutStore is the runner's session store without the items the session's
// rewinds cut, so the context builder, the fork, and the usage seed never
// see them. The store still chains each turn to the one before it in the
// file, which may be a cut one: a new turn's PreviousTurnID is set to the
// file's latest turn, which Items saw.
type cutStore struct {
	sessionstore.Store

	cuts compaction.Cuts

	mu       sync.Mutex
	lastTurn map[session.ID]session.TurnID
}

// withCuts puts the session's rewinds in front of the store.
func withCuts(store sessionstore.Store, dir, id string) (sessionstore.Store, error) {
	cuts, err := compaction.OpenRewinds(dir, id).Records()
	if err != nil {
		return nil, err
	}
	if len(cuts) == 0 {
		return store, nil
	}

	return &cutStore{Store: store, cuts: cuts, lastTurn: map[session.ID]session.TurnID{}}, nil
}

func (c *cutStore) Items(ctx context.Context, id session.ID, after sessionstore.Sequence, limit int) (sessionstore.Page, error) {
	page, err := c.Store.Items(ctx, id, after, limit)
	if err != nil {
		return page, fmt.Errorf("failed to read session %q: %w", id, err)
	}
	c.mu.Lock()
	for _, it := range page.Items {
		if t, ok := it.Data.(session.Turn); ok {
			c.lastTurn[id] = t.ID
		}
	}
	c.mu.Unlock()
	page.Items = slices.DeleteFunc(page.Items, func(it sessionstore.Item) bool { return c.cuts.Hides(uint64(it.Sequence)) })

	return page, nil
}

func (c *cutStore) AppendTurn(ctx context.Context, id session.ID, turn session.Turn) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if last, ok := c.lastTurn[id]; ok {
		turn.PreviousTurnID = last
	}
	if err := c.Store.AppendTurn(ctx, id, turn); err != nil {
		return fmt.Errorf("failed to record a turn: %w", err)
	}
	c.lastTurn[id] = turn.ID

	return nil
}

// rewind drops what a rewind cut from the session's last request, for
// /context: its last n user messages, the first of which is first. used is
// the context the last response before them reported. A request that does
// not end that way is forgotten, so /context never shows what the model no
// longer sees.
func (l *lastRequests) rewind(sessionID string, n int, first string, used int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	last, ok := l.sessions[sessionID]
	if !ok {
		return
	}
	at, seen := len(last.req.Input), 0
	for at > 0 && seen < n {
		at--
		if compaction.IsUserMessage(last.req.Input[at]) {
			seen++
		}
	}
	text, _ := images.Split(first)
	if seen < n || !sameText(last.req.Input[at], text) {
		delete(l.sessions, sessionID)

		return
	}
	last.req.Input, last.input, last.output = slices.Clone(last.req.Input[:at]), used, nil
	l.sessions[sessionID] = last
}

func sameText(item llm.Item, text string) bool {
	msg, ok := item.Data.(llm.Message)

	return ok && msg.Text == text
}
