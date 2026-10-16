package tokstats

import (
	"cmp"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/compaction"
	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/internal/store"
)

// sessionInfo is what Collect knows of a session: its source and parent,
// from its sidecar, and its workspace from the sidecar, else from its runs
// (a sidecar from before uah kept it lacks it).
type sessionInfo struct {
	source, workspace, parent string
}

// Collect reads every session's tokens in the state directory: each
// finished run's from the session index, and each compaction call's from
// the sessions' compaction logs, which no run's totals hold.
func Collect(ctx context.Context, stateDir string) ([]Entry, error) {
	runs, err := store.TokensIn(ctx, stateDir)
	if err != nil {
		return nil, err
	}
	sessionsDir := filepath.Join(stateDir, "sessions")
	ran := map[string]string{}
	for _, r := range runs {
		ran[r.Session] = r.Workspace
	}
	known := map[string]sessionInfo{}
	info := func(id string) sessionInfo {
		if in, ok := known[id]; ok {
			return in
		}
		in := sessionInfo{source: SourceUnknown}
		if sc, found, err := session.ReadSidecar(sessionsDir, id); err == nil && found {
			in.source, in.workspace, in.parent = sourceOf(sc), sc.Workspace, sc.Parent
		}
		in.workspace = cmp.Or(in.workspace, ran[id])
		known[id] = in

		return in
	}
	entries := make([]Entry, 0, len(runs))
	for _, r := range runs {
		entries = append(entries, Entry{
			At: r.Started, Session: r.Session, Model: r.Model, Workspace: r.Workspace,
			Source: info(r.Session).source, Tokens: r.Tokens, Run: true,
		})
	}
	compactions, err := compactionEntries(sessionsDir, info)
	if err != nil {
		return nil, err
	}

	return append(entries, compactions...), nil
}

// compactionEntries are the summary calls in the sessions' compaction
// logs; a compaction that made no call (elide, clear) has no tokens. A
// forked subagent's log starts with copies of its parent's records, which
// count once, as the parent's: the logs are read by depth, a session's
// parents before it.
func compactionEntries(sessionsDir string, info func(string) sessionInfo) ([]Entry, error) {
	files, err := os.ReadDir(sessionsDir) // not a glob: the state dir may hold its characters
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	var ids []string
	for _, f := range files {
		if id, ok := strings.CutSuffix(f.Name(), ".compaction.jsonl"); ok {
			ids = append(ids, id)
		}
	}
	depths := map[string]int{}
	for _, id := range ids {
		depths[id] = depth(id, info)
	}
	slices.SortStableFunc(ids, func(a, b string) int { return cmp.Compare(depths[a], depths[b]) })
	type call struct {
		hash string
		at   time.Time
	}
	seen := map[call]bool{}
	var out []Entry
	for _, id := range ids {
		records, _, err := compaction.OpenLog(sessionsDir, id).Records()
		if err != nil {
			return nil, err
		}
		ws := info(id).workspace
		for _, rec := range records {
			if rec.Stats == nil || rec.Stats.Call.Input+rec.Stats.Call.Output == 0 {
				continue
			}
			k := call{rec.Hash, rec.At.UTC()}
			if seen[k] {
				continue
			}
			seen[k] = true
			c := rec.Stats.Call
			out = append(out, Entry{
				At: rec.At, Session: id, Model: rec.Model, Workspace: ws, Source: SourceCompaction,
				Tokens: core.Tokens{InputTokens: c.Input, CachedInputTokens: c.Cached, OutputTokens: c.Output, ReasoningTokens: c.Reasoning},
			})
		}
	}

	return out, nil
}

// depth is how many parents a session has, by its sidecars' parents; a
// subagent without its parent's (an ID with the prefix) has one at least.
func depth(id string, info func(string) sessionInfo) int {
	n := 0
	seen := map[string]bool{id: true}
	for p := info(id).parent; p != "" && !seen[p]; p = info(p).parent {
		seen[p] = true
		n++
	}
	if n == 0 && strings.HasPrefix(id, session.SubagentIDPrefix) {
		n = 1
	}

	return n
}

// sourceOf names where a session ran: the TUI, uah exec, a subagent, or a
// /review's reviewer.
func sourceOf(sc session.Sidecar) string {
	switch {
	case sc.Review:
		return SourceReview
	case sc.Source == session.SourceRun:
		return SourceExec
	case sc.Source == "":
		return SourceUnknown
	}

	return sc.Source
}
