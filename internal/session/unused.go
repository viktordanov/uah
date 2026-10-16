package session

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/viktordanov/uagent/harness"

	"github.com/viktordanov/uah/internal/sessionfile"
)

// sidecarSuffix names a session's sidecar in the sessions directory.
const sidecarSuffix = ".uah.json"

// Unused lists the sessions in stateDir that infos does not have, which
// have no run: a sidecar with no item in the session file, such as a
// launch that was stopped before its first message, and a session file
// with history but no run record, such as one another program converted
// from another harness, with or without a sidecar. infos are the sessions
// with runs (Sessions, or the index). A never-used session's Info comes
// from its sidecar alone: no runs, no first prompt, last_sequence 0, and
// its creation time as both Started and LastActivity. A session with
// history adds its session file's: the header's creation time when no
// sidecar has one, its first message when no sidecar does, and its last
// item's Sequence and time. Resuming either under its ID continues it.
func Unused(stateDir string, infos []Info) ([]Info, error) {
	sessionsDir := filepath.Join(stateDir, "sessions")
	entries, err := os.ReadDir(sessionsDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to list sessions: %w", err)
	}
	known := map[string]bool{}
	for _, in := range infos {
		known[in.ID] = true
	}
	var out []Info
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), sidecarSuffix)
		if !ok {
			id, ok = strings.CutSuffix(e.Name(), sessionFileSuffix)
		}
		if !ok || e.IsDir() || known[id] {
			continue
		}
		known[id] = true // a session's sidecar and file list it once
		if in, ok := unused(sessionsDir, id); ok {
			out = append(out, in)
		}
	}

	return out, nil
}

// sessionFileSuffix names the runner's session file in the sessions
// directory.
const sessionFileSuffix = ".session.jsonl"

// unused is a session's Info from its sidecar and session file, when it
// has either; a session file that does not read is left out.
func unused(sessionsDir, id string) (Info, bool) {
	path := filepath.Join(sessionsDir, id+sessionFileSuffix)
	last, history, err := sessionfile.Last(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return Info{}, false
	}
	sc, found, err := ReadSidecar(sessionsDir, id)
	if err != nil || !found && !history {
		return Info{}, false
	}
	in := Info{ID: id}
	if found {
		in.Workspace, in.FirstPrompt, in.Started, in.LastActivity = sc.Workspace, sc.FirstPrompt, sc.Created, sc.Created
		in.ApplySidecar(sc)
	} else if strings.HasPrefix(id, SubagentIDPrefix) {
		in.Source = SourceSubagent // a subagent without its sidecar stays out of the picker
	}
	if !history {
		return in, true
	}
	h, first := firstMessage(path)
	if in.Started.IsZero() {
		in.Started = h.CreatedAt
	}
	in.FirstPrompt = cmp.Or(in.FirstPrompt, cutPrompt(first))
	in.LastActivity, in.LastSequence = last.RecordedAt, last.Sequence

	return in, true
}

// firstMessagePages is how many items firstMessage reads at most: the
// first message follows the header and a few developer messages.
const firstMessagePages = 64

// firstMessage is the session file's header and its first external
// input's text ("" when none is among its first items).
func firstMessage(path string) (sessionfile.Header, string) {
	h, page, err := sessionfile.Read(path, sessionfile.BeforeFirst, firstMessagePages)
	if err != nil {
		return h, ""
	}
	for _, it := range page.Items {
		var input sessionfile.Input
		if it.Kind != sessionfile.KindInput || it.Decode(&input) != nil || input.Kind != sessionfile.InputExternal {
			continue
		}
		if text, err := input.Text(); err == nil {
			return h, text
		}
	}

	return h, ""
}

// WithUnused adds the sessions with no run (Unused) to infos, most
// recently active first.
func WithUnused(stateDir string, infos []Info) ([]Info, error) {
	unused, err := Unused(stateDir, infos)
	if err != nil || len(unused) == 0 {
		return infos, err
	}
	infos = append(slices.Clone(infos), unused...)
	slices.SortStableFunc(infos, func(a, b Info) int { return b.LastActivity.Compare(a.LastActivity) })

	return infos, nil
}

// WithHistory adds the sessions with history but no run to infos, most
// recently active first: the ones the resume picker and --last offer,
// which leave out a session that never ran, as there is nothing in it to
// go back to.
func WithHistory(stateDir string, infos []Info) ([]Info, error) {
	unused, err := Unused(stateDir, infos)
	if err != nil {
		return infos, err
	}
	unused = slices.DeleteFunc(unused, func(in Info) bool { return in.LastSequence == 0 })
	if len(unused) == 0 {
		return infos, nil
	}
	infos = append(slices.Clone(infos), unused...)
	slices.SortStableFunc(infos, func(a, b Info) int { return b.LastActivity.Compare(a.LastActivity) })

	return infos, nil
}

// Used reports whether the session has history: a run, or an item in its
// session file. A session without either never ran, so starting it under
// its ID loses nothing.
func Used(stateDir, id string) (bool, error) {
	has, err := hasItems(filepath.Join(stateDir, "sessions"), id)
	if err != nil || has {
		return has, err
	}
	records, err := harness.New(harness.Config{StateDir: stateDir}).Runs()
	if err != nil {
		return false, fmt.Errorf("failed to list runs: %w", err)
	}

	return slices.ContainsFunc(records, func(r harness.RunRecord) bool { return r.Result.Request.SessionID == id }), nil
}

// hasItems reports whether the session file has an item; a missing file
// has none.
func hasItems(sessionsDir, id string) (bool, error) {
	_, found, err := sessionfile.Last(filepath.Join(sessionsDir, id+sessionFileSuffix))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}

	return found, err
}
