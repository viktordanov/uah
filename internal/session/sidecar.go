package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/viktordanov/uah/internal/goal"
	"github.com/viktordanov/uah/internal/sandbox"
	"github.com/viktordanov/uah/internal/toolpolicy"
)

// Where a session was started. Codex hides scripted sessions from its resume
// picker by default; uah does the same with SourceRun (`uah exec`, and its
// alias `uah run`; the value stays "run") and with subagents.
const (
	SourceTUI      = "tui"
	SourceRun      = "run"
	SourceSubagent = "subagent"
)

// SubagentIDPrefix starts the session ID of every subagent uah spawns, so a
// child is known by its ID alone. Older children have plain UUIDs; their
// sidecar's Parent identifies them.
const SubagentIDPrefix = "subagent-"

// NewSubagentID returns a new subagent session ID: subagent-<uuid>. The
// runner's session store, uagent's session lock, and the run records accept
// any ASCII letters, digits, and dashes.
func NewSubagentID() string { return SubagentIDPrefix + uuid.NewString() }

// ShortID is the ID as uah prints it in lists: the first 8 characters of
// the UUID, after the subagent prefix when there is one. It is a prefix of
// the ID, so it resumes the session.
func ShortID(id string) string {
	rest, sub := strings.CutPrefix(id, SubagentIDPrefix)
	short := rest[:min(8, len(rest))]
	if sub {
		return SubagentIDPrefix + short
	}

	return short
}

// Sidecar is what uah knows about a session that the runner and uagent do not
// record: sessions/<id>.uah.json. The file is the source of truth; see
// docs/design/state.md.
type Sidecar struct {
	Source  string    `json:"source"`
	Created time.Time `json:"created"`
	// Parent is the session that spawned this one (SourceSubagent).
	Parent string `json:"parent,omitempty"`
	// Settings are what the session last used, restored on resume (nil in
	// sidecars from before uah kept them).
	Settings *Saved `json:"settings,omitempty"`
	// Workspace, FirstPrompt, and LastActivity find a session from this
	// file alone: its absolute workspace, its first message cut to
	// FirstPromptMax characters, and the time of its last item. Sidecars
	// from before uah kept them lack them until the session resumes.
	Workspace    string    `json:"workspace,omitempty"`
	FirstPrompt  string    `json:"first_prompt,omitempty"`
	LastActivity time.Time `json:"last_activity,omitzero"`
	// LastSequence is the Sequence of the last item in the session file
	// when the last turn ended: it changes only when the session does.
	LastSequence uint64 `json:"last_sequence,omitempty"`
	// Queued are the unsent messages, queued again on resume (saveQueue).
	Queued []string `json:"queued,omitempty"`
	// Goal is the session's goal (/goal), restored on resume.
	Goal *goal.Goal `json:"goal,omitempty"`
	// Tools is the tool policy the session last ran under, when it had
	// one: a resume narrows its own policy with it, so resuming never
	// widens a session's tools (internal/toolpolicy).
	Tools *toolpolicy.Policy `json:"tools,omitempty"`
	// Grants are the directories the session made writable, each checked
	// again when the session resumes (sandbox.Grants.Valid).
	Grants []sandbox.Grant `json:"grants,omitempty"`
}

// RemoveSidecar deletes a session's sidecar, for a session that never ran.
func RemoveSidecar(sessionsDir, id string) error {
	if err := os.Remove(sidecarPath(sessionsDir, id)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("failed to remove the session's sidecar: %w", err)
	}

	return nil
}

func sidecarPath(sessionsDir, id string) string {
	return filepath.Join(sessionsDir, id+sidecarSuffix)
}

// ReadSidecar returns the session's sidecar, or found=false when it has none.
func ReadSidecar(sessionsDir, id string) (sc Sidecar, found bool, err error) {
	data, err := os.ReadFile(sidecarPath(sessionsDir, id))
	if errors.Is(err, fs.ErrNotExist) {
		return Sidecar{}, false, nil
	}
	if err != nil {
		return Sidecar{}, false, fmt.Errorf("failed to read the session sidecar: %w", err)
	}
	if err := json.Unmarshal(data, &sc); err != nil {
		return Sidecar{}, false, fmt.Errorf("failed to parse the session sidecar: %w", err)
	}

	return sc, true, nil
}

// writeSidecar creates the sidecar unless one exists: the first writer, the
// command that started the session, decides its source.
func writeSidecar(sessionsDir, id string, sc Sidecar) error {
	if err := os.MkdirAll(sessionsDir, 0o700); err != nil {
		return fmt.Errorf("failed to create %s: %w", sessionsDir, err)
	}
	data, err := json.Marshal(sc)
	if err != nil {
		return fmt.Errorf("failed to encode the session sidecar: %w", err)
	}
	f, err := os.OpenFile(sidecarPath(sessionsDir, id), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to create the session sidecar: %w", err)
	}
	_, werr := f.Write(append(data, '\n'))

	return errors.Join(werr, f.Close())
}

// Interactive drops sessions started by `uah exec`, as Codex's picker drops
// `codex exec` sessions, and subagents. Sessions with no sidecar (older
// ones, or uagent's) stay.
func Interactive(infos []Info) []Info {
	out := make([]Info, 0, len(infos))
	for _, in := range infos {
		if in.Source != SourceRun && in.Source != SourceSubagent {
			out = append(out, in)
		}
	}

	return out
}

// Nested is a session in Tree order with its depth under its parent.
type Nested struct {
	Info

	Depth int
}

// Tree orders subagents right after their parents, keeping the order
// otherwise; a subagent whose parent is not listed stays where it is.
func Tree(infos []Info) []Nested {
	listed := map[string]bool{}
	children := map[string][]Info{}
	for _, in := range infos {
		listed[in.ID] = true
	}
	var roots []Info
	for _, in := range infos {
		if in.Parent != "" && listed[in.Parent] && in.Parent != in.ID {
			children[in.Parent] = append(children[in.Parent], in)
		} else {
			roots = append(roots, in)
		}
	}
	out := make([]Nested, 0, len(infos))
	var add func(in Info, depth int)
	add = func(in Info, depth int) {
		out = append(out, Nested{Info: in, Depth: depth})
		for _, c := range children[in.ID] {
			add(c, depth+1)
		}
	}
	for _, r := range roots {
		add(r, 0)
	}

	return out
}
