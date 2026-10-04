package session

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/viktordanov/uagent/harness"
)

// ErrNoSession means nothing on disk belongs to the session.
var ErrNoSession = errors.New("no such session")

// sessionFiles are the files next to the runner's session file that belong
// to a session, by suffix after its ID.
var sessionFiles = []string{".session.jsonl", ".uah.json", ".compaction.jsonl", ".rewind.jsonl", ".websearch.jsonl", ".effortupdates.json", ".agent.json", ".forktmp"}

// Removal is what deleting a session removes: the session and its
// subagents (IDs, the session first), and their files and run records
// (Paths, absolute). Lock files come last.
type Removal struct {
	IDs   []string
	Paths []string

	stateDir string
	locks    []string
}

// PlanRemoval finds what removing the session id removes, touching nothing:
// its files in sessions/, its tool output in sessions/operations/<id>/, its
// run records in runs/, and the same for each subagent it spawned, found by
// the parent in their sidecars.
func PlanRemoval(stateDir, id string) (Removal, error) {
	sessionsDir := filepath.Join(stateDir, "sessions")
	ids, err := withSubagents(sessionsDir, id)
	if err != nil {
		return Removal{}, err
	}
	r := Removal{IDs: ids, stateDir: stateDir}
	runs, err := runDirs(stateDir, ids)
	if err != nil {
		return Removal{}, err
	}
	r.Paths = append(r.Paths, runs...)
	for _, sid := range ids {
		for _, p := range append([]string{filepath.Join(sessionsDir, "operations", sid)}, sessionPaths(sessionsDir, sid)...) {
			if exists(p) {
				r.Paths = append(r.Paths, p)
			}
		}
	}
	for _, sid := range ids {
		if lock := filepath.Join(sessionsDir, sid+".lock"); exists(lock) {
			r.locks = append(r.locks, sid)
			r.Paths = append(r.Paths, lock)
		}
	}
	if len(r.Paths) == 0 {
		return Removal{}, fmt.Errorf("%w: %s", ErrNoSession, id)
	}

	return r, nil
}

// Lock takes the session lock of each session that has one, as a run does,
// so no run starts while the files go. A lock another run holds is
// harness.ErrSessionBusy unless force is set, which skips it. unlock
// releases them.
func (r Removal) Lock(force bool) (unlock func(), err error) {
	var unlocks []func() error
	unlock = func() {
		for _, u := range unlocks {
			_ = u()
		}
	}
	for _, id := range r.locks {
		u, err := harness.LockSession(r.stateDir, id)
		if errors.Is(err, harness.ErrSessionBusy) && force {
			continue
		}
		if err != nil {
			unlock()

			return nil, err
		}
		unlocks = append(unlocks, u)
	}

	return unlock, nil
}

// Remove deletes the paths in order and reports every one it could not.
func (r Removal) Remove() error {
	var errs []error
	for _, p := range r.Paths {
		if err := os.RemoveAll(p); err != nil {
			errs = append(errs, fmt.Errorf("failed to remove %s: %w", p, err))
		}
	}

	return errors.Join(errs...)
}

// withSubagents is id and every session under it, parents first.
func withSubagents(sessionsDir, id string) ([]string, error) {
	children, err := subagentsByParent(sessionsDir)
	if err != nil {
		return nil, err
	}
	ids := []string{id}
	for i := 0; i < len(ids); i++ {
		for _, c := range children[ids[i]] {
			if !slices.Contains(ids, c) {
				ids = append(ids, c)
			}
		}
	}

	return ids, nil
}

// subagentsByParent reads every sidecar's parent.
func subagentsByParent(sessionsDir string) (map[string][]string, error) {
	entries, err := os.ReadDir(sessionsDir)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string][]string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to list sessions: %w", err)
	}
	children := map[string][]string{}
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".uah.json")
		if !ok || e.IsDir() || strings.HasPrefix(id, ".") {
			continue
		}
		if sc, found, err := ReadSidecar(sessionsDir, id); err == nil && found && sc.Parent != "" {
			children[sc.Parent] = append(children[sc.Parent], id)
		}
	}

	return children, nil
}

// runDirs are the run records of the sessions.
func runDirs(stateDir string, ids []string) ([]string, error) {
	records, err := harness.New(harness.Config{StateDir: stateDir}).Runs()
	if err != nil {
		return nil, fmt.Errorf("failed to list runs: %w", err)
	}
	var dirs []string
	for _, rec := range records {
		if slices.Contains(ids, rec.Result.Request.SessionID) {
			dirs = append(dirs, rec.Dir)
		}
	}
	slices.Sort(dirs)

	return dirs, nil
}

func sessionPaths(sessionsDir, id string) []string {
	out := make([]string, 0, len(sessionFiles))
	for _, suffix := range sessionFiles {
		out = append(out, filepath.Join(sessionsDir, id+suffix))
	}

	return out
}

func exists(p string) bool {
	_, err := os.Lstat(p)

	return err == nil
}
