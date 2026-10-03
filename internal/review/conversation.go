package review

import (
	"slices"
	"sync"

	"github.com/viktordanov/uah-core/harness/llm"
)

// MaxConversationTokens ends a session's review conversation once a review
// request reached it: the next review starts a new conversation with the
// whole transcript again, as Codex compacts its reviewer at the context
// limit.
const MaxConversationTokens = 100_000

// Conversation is one session's review conversation, Codex's guardian
// trunk: each review appends what the session did since the last review
// and the new action to the same conversation, so the requests share
// their prefix and the prompt cache. Reviews run one at a time on it; a
// review that starts while another runs forks the last finished state and
// is dropped when it ends, as Codex's parallel approvals run in ephemeral
// forks of the committed trunk. A failed review ends the conversation. The
// zero Conversation is empty and ready; it is safe for concurrent use.
type Conversation struct {
	// busy is held by the review running on the conversation itself.
	busy sync.Mutex

	mu   sync.Mutex
	last checkpoint
}

// checkpoint is a finished review's conversation.
type checkpoint struct {
	// key is the settings it was made with; a change starts anew.
	key string
	// history is the conversation after the instructions.
	history []llm.Item
	// cursor is the number of transcript entries reviewed.
	cursor  int
	reviews int
	// reminded is whether history has the follow-up reminder.
	reminded bool
	// tokens is the last request's input tokens.
	tokens int64
}

// begin returns the checkpoint a review starts from, and whether the
// review runs on the conversation, which then must call end. An unusable
// checkpoint (other settings, too long) comes back empty.
func (c *Conversation) begin(key string) (checkpoint, bool) {
	trunk := c.busy.TryLock()
	c.mu.Lock()
	cp := c.last
	c.mu.Unlock()
	if cp.key != key || cp.tokens >= MaxConversationTokens {
		cp = checkpoint{key: key}
	}
	// A review appends to its own copy.
	cp.history = slices.Clip(cp.history)

	return cp, trunk
}

// end finishes the conversation's own review: next becomes the
// conversation, or nil ends it.
func (c *Conversation) end(next *checkpoint) {
	c.mu.Lock()
	if next != nil {
		c.last = *next
	} else {
		c.last = checkpoint{}
	}
	c.mu.Unlock()
	c.busy.Unlock()
}
