package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/viktordanov/uagent/core"
	"github.com/viktordanov/uagent/harness"
	"github.com/viktordanov/uagent/stream"

	"github.com/viktordanov/uah/internal/approval"
	"github.com/viktordanov/uah/internal/images"
	"github.com/viktordanov/uah/internal/toolpolicy"
)

// Info summarizes one session from its run records.
type Info struct {
	ID           string
	FirstPrompt  string
	Provider     string
	Model        string
	Effort       string
	Workspace    string
	Runs         int
	Started      time.Time
	LastActivity time.Time
	Status       core.Status // of the newest run; "running" while one is in progress
	Tokens       core.Tokens
	// Source is where the session started (SourceTUI or SourceRun), or ""
	// when it has no sidecar.
	Source string
	// Parent is the spawning session of a subagent.
	Parent string
	// Fast and Mode are the fast mode and the permission mode the session
	// last used, from its sidecar (nil and "" when it does not record them).
	Fast *bool
	Mode approval.Mode
	// AdaptiveEffort is the adaptive effort the session last used, from
	// its sidecar ("" when it does not record it).
	AdaptiveEffort string
	// Saved reports whether the sidecar recorded the settings, which then
	// replaced the provider, model, and effort of the newest run.
	Saved bool
	// Tools is the tool policy the session last ran under, from its
	// sidecar (nil: none).
	Tools *toolpolicy.Policy
	// LastSequence is the sidecar's last_sequence: the Sequence of the
	// session file's last item when the last turn ended (0: unknown).
	LastSequence uint64
}

// LoadedRun is one run of a session with its decoded runner events.
type LoadedRun struct {
	Record harness.RunRecord
	Events []core.Event
}

// Sessions lists sessions in stateDir, most recently active first. It reads
// only summaries and requests, never full event files.
func Sessions(stateDir string) ([]Info, error) {
	records, err := harness.New(harness.Config{StateDir: stateDir}).Runs()
	if err != nil {
		return nil, fmt.Errorf("failed to list sessions: %w", err)
	}
	bySession := map[string][]harness.RunRecord{}
	for _, r := range records {
		id := r.Result.Request.SessionID
		bySession[id] = append(bySession[id], r)
	}
	infos := make([]Info, 0, len(bySession))
	sessionsDir := filepath.Join(stateDir, "sessions")
	for id, runs := range bySession {
		info := summarize(id, runs)
		if sc, found, err := ReadSidecar(sessionsDir, id); err == nil && found {
			info.ApplySidecar(sc)
		}
		infos = append(infos, info)
	}
	slices.SortFunc(infos, func(a, b Info) int { return b.LastActivity.Compare(a.LastActivity) })

	return infos, nil
}

// summarize folds a session's runs (newest first) into an Info.
func summarize(id string, runs []harness.RunRecord) Info {
	newest, oldest := runs[0].Result, runs[len(runs)-1]
	info := Info{
		ID: id, Runs: len(runs), Started: oldest.Result.StartedAt,
		Provider: newest.Request.Provider, Model: newest.Request.Model, Effort: newest.Request.Effort,
		Workspace: newest.Request.Workspace, Status: newest.Status,
	}
	for _, r := range runs {
		info.Tokens = info.Tokens.Add(runTokens(r.Dir))
		end := r.Result.StartedAt.Add(r.Result.Wall)
		if end.After(info.LastActivity) {
			info.LastActivity = end
		}
	}
	if req, err := harness.LoadRequest(oldest.Dir); err == nil {
		info.FirstPrompt = firstPrompt(req)
	}

	return info
}

// runTokens are the tokens a run used, from its summary.json.
func runTokens(dir string) core.Tokens {
	b, err := os.ReadFile(filepath.Join(dir, harness.SummaryFile))
	if err != nil {
		return core.Tokens{}
	}
	var d stream.SummaryDTO
	if json.Unmarshal(b, &d) != nil {
		return core.Tokens{}
	}

	return SummaryTokens(d)
}

// SummaryTokens are the tokens of a run summary. The harness's run records
// carry none: uagent v0.7.0's stream.SummaryFromDTO reads only the
// summary's metadata, not its stats.
func SummaryTokens(d stream.SummaryDTO) core.Tokens {
	t := d.Stats.Tokens

	return core.Tokens{
		InputTokens: t.Input, CachedInputTokens: t.CachedInput, CacheWriteInputTokens: t.CacheWriteInput,
		OutputTokens: t.Output, ReasoningTokens: t.Reasoning,
	}
}

func firstPrompt(req core.Request) string {
	if req.Prompt != "" {
		return req.Prompt
	}
	texts := make([]string, 0, len(req.Messages))
	for _, m := range req.Messages {
		texts = append(texts, images.Display(m.Text)) // pasted images show as their placeholders
	}

	return strings.Join(texts, "\n")
}

// Load reads every run of a session in start order, with its events. The
// runner writes only the items each run appended, so the runs together are
// the whole transcript. Saved compactions join the events they happened
// among, as engine.Compacted, applied patches their calls, as
// engine.PatchApplied, failed commands and MCP calls their output, as
// engine.ToolOutput, and rewinds the run before them, as engine.Rewound.
// Each run's result carries its tokens, from its summary.
func Load(stateDir, id string) ([]LoadedRun, error) {
	records, err := harness.New(harness.Config{StateDir: stateDir}).Runs()
	if err != nil {
		return nil, fmt.Errorf("failed to load session: %w", err)
	}
	var runs []LoadedRun
	br := bufio.NewReaderSize(nil, 1<<20) // one buffer for every run's events file
	for _, r := range slices.Backward(records) {
		if r.Result.Request.SessionID != id {
			continue
		}
		events, err := loadEvents(br, r.Dir)
		if err != nil {
			return nil, fmt.Errorf("failed to load run %s: %w", r.Result.Request.RunID, err)
		}
		r.Result.Stats.Tokens = runTokens(r.Dir)
		runs = append(runs, LoadedRun{Record: r, Events: events})
	}

	runs, err = withCompactions(stateDir, id, runs)
	if err != nil {
		return nil, err
	}

	return withRewinds(stateDir, id, runs)
}

// InDir keeps the sessions whose workspace is dir.
func InDir(infos []Info, dir string) []Info {
	want := normalizeDir(dir)
	var out []Info
	for _, in := range infos {
		if in.Workspace != "" && normalizeDir(in.Workspace) == want {
			out = append(out, in)
		}
	}

	return out
}

// ActiveSince keeps the sessions with activity after t.
func ActiveSince(infos []Info, t time.Time) []Info {
	var out []Info
	for _, in := range infos {
		if in.LastActivity.After(t) {
			out = append(out, in)
		}
	}

	return out
}

// normalizeDir makes p absolute and clean and resolves symlinks, the way
// Codex matches a session's working directory.
func normalizeDir(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = p
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}

	return filepath.Clean(abs)
}
