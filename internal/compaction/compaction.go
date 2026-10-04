// Package compaction rewrites a model request the way Codex compacts a
// thread: the user messages stay verbatim and in order (the newest 20,000
// tokens of them), and everything else before a point is replaced by one
// summary message. It holds the rewrite, the summary call over any
// llm.Adapter, the token estimates, and the compaction log; the embedded
// engine decides when to compact and emits the events. See README.md.
//
// prompt.md and summary_prefix.md in prompts/ are Codex's (rust-v0.156.1,
// codex-rs/prompts/templates/compact), Apache License 2.0, Copyright 2025
// OpenAI; see prompts/LICENSE-codex. sections.md is uah's, after Codex's.
package compaction

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/viktordanov/uah-core/harness/llm"
)

var (
	//go:embed prompts/prompt.md
	codexPromptFile string
	//go:embed prompts/sections.md
	promptFile string
	//go:embed prompts/summary_prefix.md
	prefixFile string
)

// Prompt asks the model for the handoff summary: Codex's opening and its
// points as fixed sections (Goal, Constraints, Decisions, State, Errors,
// TODOs, Next). On the owner's sessions it kept more of the failing
// commands (83% against 67%), their errors (50% against 8%), and the paths
// read (48% against 39%) than Codex's prompt, in shorter summaries.
var Prompt = strings.TrimSpace(promptFile)

// CodexPrompt is Codex's summary prompt (prompts/prompt.md), which
// compact_prompt can set again.
var CodexPrompt = strings.TrimSpace(codexPromptFile)

// SummaryPrefix starts the message that carries the summary.
var SummaryPrefix = strings.TrimSpace(prefixFile)

// Trigger says what started a compaction.
type Trigger string

const (
	TriggerManual Trigger = "manual"
	TriggerAuto   Trigger = "auto"
	// TriggerClear is /clear: the covered items are dropped with no summary,
	// so the model starts fresh in the same session.
	TriggerClear Trigger = "clear"
)

// Verb names what the trigger does, for messages: "compaction", or "clear".
func (t Trigger) Verb() string {
	if t == TriggerClear {
		return "clear"
	}

	return "compaction"
}

// ErrMismatch means the request's history is not the one the record covers.
var ErrMismatch = errors.New("the history does not match the compaction")

// Record is one compaction. It covers the first Covered items after the
// system message of every later request the context builder produces.
type Record struct {
	Covered int `json:"covered"`
	// Shape is Shape of the covered items, which Apply checks. Hash, of the
	// items with their tool outputs, is checked only for a record from
	// before Shape, and still written for the uah versions before it.
	Shape string `json:"shape,omitempty"`
	// Floor is how many of the covered items a /clear dropped: they are
	// left out entirely, with none of their user messages kept. A clear's
	// Floor is its Covered; a later compaction carries the floor forward.
	Floor   int       `json:"floor,omitempty"`
	Hash    string    `json:"hash"`
	Summary string    `json:"summary"`
	Trigger Trigger   `json:"trigger"`
	Model   string    `json:"model,omitempty"`
	At      time.Time `json:"at"`
	// Keep is the cap on kept user messages in tokens (0: Codex's 20,000),
	// saved with the record so what the model sees stays the same when the
	// setting changes later.
	Keep int `json:"keep,omitempty"`
	// Focus is what the user asked the summary to focus on (/compact
	// <instructions>).
	Focus string `json:"focus,omitempty"`
	// Elided are the calls whose outputs the model sees as stubs (Elide),
	// from this record's elision pass and the ones before it.
	Elided []string `json:"elided,omitempty"`
	// Remote is the provider's encrypted compaction item, for a remote
	// compaction (StrategyRemote): later requests send it where the summary
	// would be.
	Remote jsontext.Value `json:"remote,omitempty"`
	// Ledger is the state ledger uah generated from the covered items
	// (Ledger), sent after the summary.
	Ledger string `json:"ledger,omitempty"`
	// Stats measure the compaction; records before uah measured them have
	// none.
	Stats *Stats `json:"stats,omitempty"`
}

// keepTokens is the record's cap on kept user messages.
func (r Record) keepTokens() int {
	return Settings{UserMessageMaxTokens: r.Keep}.KeepTokens()
}

// Hash fingerprints items with their tool outputs; a record from before
// Shape applies only to the history whose Hash it has.
func Hash(items []llm.Item) (string, error) {
	sum := sha256.New()
	for _, item := range items {
		b, err := json.Marshal(item)
		if err != nil {
			return "", fmt.Errorf("failed to encode a history item: %w", err)
		}
		_, _ = sum.Write(b)
		_, _ = sum.Write([]byte{'\n'})
	}

	return hex.EncodeToString(sum.Sum(nil)), nil
}

// Shape fingerprints items as Hash does, but each tool result by its call
// alone: a resumed run renders every output again from its operation, with
// the tools of the version and configuration that resume, so an output can
// change (a new format, a sandbox hint) while the history stays the same.
func Shape(items []llm.Item) (string, error) {
	shaped := make([]llm.Item, len(items))
	for i, item := range items {
		if r, ok := item.Data.(llm.ToolResult); ok {
			item.Data = llm.ToolResult{CallID: r.CallID}
		}
		shaped[i] = item
	}

	return Hash(shaped)
}

// matches reports whether covered are the items the record covers: by
// Shape, or by Hash for a record from before Shape (ErrMismatch when not).
func (r Record) matches(covered []llm.Item) error {
	want, fingerprint := r.Hash, Hash
	if r.Shape != "" {
		want, fingerprint = r.Shape, Shape
	}
	got, err := fingerprint(covered)
	if err != nil {
		return err
	}
	if got != want {
		return ErrMismatch
	}

	return nil
}

// Coverable is how many items after input's system message a compaction
// covers: all of them except the user messages at the end, which are new
// input that goes after the summary, as Codex compacts before recording it.
func Coverable(input []llm.Item) int {
	n := len(input) - 1
	for n > 0 && IsUserMessage(input[n]) {
		n--
	}

	return max(n, 0)
}

// NewRecord covers the Coverable items of input with the summary.
func NewRecord(input []llm.Item, summary string, trigger Trigger, model string, at time.Time) (Record, error) {
	return NewRecordCovering(input, Coverable(input), summary, trigger, model, at)
}

// NewRecordCovering covers the first covered items after input's system
// message with the summary (see CoverableKeeping).
func NewRecordCovering(input []llm.Item, covered int, summary string, trigger Trigger, model string, at time.Time) (Record, error) {
	items := input[min(1, len(input)) : 1+covered]
	hash, err := Hash(items)
	if err != nil {
		return Record{}, err
	}
	shape, err := Shape(items)
	if err != nil {
		return Record{}, err
	}

	return Record{Covered: covered, Shape: shape, Hash: hash, Summary: summary, Trigger: trigger, Model: model, At: at}, nil
}

// Apply rewrites input, whose first item is the system message, with the
// record: the system message, the covered developer messages (Developer),
// the covered user messages that Kept keeps (up to the record's cap), the
// summary, and the items after the covered ones, with the elided outputs as
// stubs. The covered configuration updates are dropped (Configured). A tool result whose call was covered
// becomes a user-role note, so no output lacks its call. A record that
// covers nothing only elides.
func Apply(input []llm.Item, rec Record) ([]llm.Item, error) {
	if rec.Covered <= 0 || len(input) == 0 {
		return Elide(input, rec.Elided), nil
	}
	if len(input) < 1+rec.Covered {
		return nil, fmt.Errorf("%w: it covers %d items, the history has %d", ErrMismatch, rec.Covered, len(input)-1)
	}
	covered, tail := input[1:1+rec.Covered], input[1+rec.Covered:]
	if err := rec.matches(covered); err != nil {
		return nil, err
	}
	developer := Developer(covered)
	kept := Kept(covered[min(rec.Floor, len(covered)):], rec.keepTokens())
	out := make([]llm.Item, 0, 2+len(developer)+len(kept)+len(tail))
	out = append(out, input[0])
	out = append(out, developer...)
	out = append(out, kept...)
	if rec.Floor < rec.Covered {
		out = append(out, rec.summaryItems()...)
	}

	return append(out, detachOrphans(Elide(tail, rec.Elided))...), nil
}

// summaryItems are what replaces the covered items after the kept user
// messages: the summary message; or for a remote compaction the item's
// placeholder, which the transport swaps for the item, then the ledger.
func (r Record) summaryItems() []llm.Item {
	if len(r.Remote) == 0 {
		return []llm.Item{SummaryMessage(r.SummaryText())}
	}
	out := []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleUser, Text: RemotePlaceholder(r)}}}
	if r.Ledger != "" {
		out = append(out, llm.Item{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleUser, Text: r.Ledger}})
	}

	return out
}

// RemoteMarker identifies a remote compaction's placeholder in a request
// body. It needs no escaping in JSON.
func RemoteMarker(r Record) string { return "uah-remote-compaction:" + r.Hash[:min(16, len(r.Hash))] }

// RemotePlaceholder is the message that stands for a remote compaction's
// item. A provider that cannot take the item reads it as is.
func RemotePlaceholder(r Record) string {
	return "[" + RemoteMarker(r) + "] The earlier conversation was compacted by the provider into an encrypted item that this model cannot read."
}

// SummaryText is what the summary message carries after the prefix: the
// summary, then the ledger.
func (r Record) SummaryText() string {
	if r.Ledger == "" {
		return r.Summary
	}

	return strings.TrimSpace(r.Summary) + "\n\n" + r.Ledger
}

// NewClear is a /clear over input: every coverable item is dropped.
func NewClear(input []llm.Item, at time.Time) (Record, error) {
	rec, err := NewRecord(input, "", TriggerClear, "", at)
	rec.Floor = rec.Covered

	return rec, err
}

// IsUserMessage reports whether the item is a user message.
func IsUserMessage(item llm.Item) bool {
	m, ok := item.Data.(llm.Message)

	return ok && item.Type == llm.ItemMessage && m.Role == llm.RoleUser
}

// SummaryMessage is the user message that carries a summary, as in Codex.
func SummaryMessage(summary string) llm.Item {
	if strings.TrimSpace(summary) == "" {
		summary = "(no summary available)"
	}

	return llm.Item{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleUser, Text: SummaryPrefix + "\n" + summary}}
}

// detachOrphans turns each tool result whose call is not among items into a
// user-role note. items is not changed.
func detachOrphans(items []llm.Item) []llm.Item {
	calls := map[string]bool{}
	for _, item := range items {
		if c, ok := item.Data.(llm.ToolCall); ok {
			calls[c.CallID] = true
		}
	}
	out := make([]llm.Item, 0, len(items))
	for _, item := range items {
		if r, ok := item.Data.(llm.ToolResult); ok && !calls[r.CallID] {
			item = orphanNote(r)
		}
		out = append(out, item)
	}

	return out
}

// orphanNote carries the output of a tool call that the summary covers. A
// message carries text only, so an image is named, not sent.
func orphanNote(r llm.ToolResult) llm.Item {
	var text []string
	for _, o := range r.Output {
		switch o.Kind {
		case llm.ToolResultText:
			text = append(text, o.Value)
		case llm.ToolResultImage:
			text = append(text, "[image omitted]")
		}
	}

	return llm.Item{Type: llm.ItemMessage, Data: llm.Message{
		Role: llm.RoleUser,
		Text: fmt.Sprintf("Output of the earlier tool call %s, which the summary covers:\n%s", r.CallID, strings.Join(text, "\n")),
	}}
}

// CoverableKeeping is Coverable leaving the last calls tool calls, with
// the model output around them, after the summary: the covered range ends
// where the model response that made the calls-th last call starts, so no
// call is split from its output. 0 is Coverable.
func CoverableKeeping(input []llm.Item, calls int) int {
	n := Coverable(input)
	if calls <= 0 {
		return n
	}
	i, seen := n, 0 // input[1:1+i] is covered
	for i > 0 && seen < calls {
		if _, ok := input[i].Data.(llm.ToolCall); ok {
			seen++
		}
		i--
	}
	if seen < calls {
		return 0
	}
	for i > 0 && modelGenerated(input[i]) {
		i--
	}

	return i
}
