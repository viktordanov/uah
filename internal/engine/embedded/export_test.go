package embedded

import (
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"testing"
	"time"
	"weak"
)

// SlowCanceledCalls delays the end of each canceled model request by d, as a
// loaded machine may, until the test ends. Tests that run
// alongside it may slow down too.
func SlowCanceledCalls(t *testing.T, d time.Duration) {
	t.Helper()
	canceledEndDelay.Store(int64(d))
	t.Cleanup(func() { canceledEndDelay.Store(0) })
}

// BeforePatchWrite runs fn after each patch was approved and before its
// job reads and writes the files, until the test ends.
func BeforePatchWrite(t *testing.T, fn func()) {
	t.Helper()
	hook := func(patchPlan) { fn() }
	beforePatchWrite.Store(&hook)
	t.Cleanup(func() { beforePatchWrite.Store(nil) })
}

// WatchSyncs syncs session files only where a record needs it, never after
// a window, until the test ends, and returns the sizes that each sync of a
// session file made durable, by the file's name.
func WatchSyncs(t *testing.T) func(name string) []int64 {
	t.Helper()
	var mu sync.Mutex
	sizes := map[string][]int64{}
	window := syncWindow
	syncWindow = time.Hour
	onSync = func(path string, size int64) {
		mu.Lock()
		sizes[filepath.Base(path)] = append(sizes[filepath.Base(path)], size)
		mu.Unlock()
	}
	t.Cleanup(func() { syncWindow, onSync = window, nil })

	return func(name string) []int64 {
		mu.Lock()
		defer mu.Unlock()

		return slices.Clone(sizes[name])
	}
}

// WatchRuns watches the runs that start in workspace until the test ends,
// and returns how many of them a collection leaves reachable: their
// wiring, agent, or model client.
func WatchRuns(t *testing.T, workspace string) func() int {
	t.Helper()
	var mu sync.Mutex
	var held []func() bool
	hook := func(w *wiring, a *agent) {
		if w.l.Request.Workspace != workspace {
			return
		}
		wp, ap, sp := weak.Make(w), weak.Make(a), weak.Make(a.llm)
		mu.Lock()
		held = append(held, func() bool { return wp.Value() != nil || ap.Value() != nil || sp.Value() != nil })
		mu.Unlock()
	}
	onStart.Store(&hook)
	t.Cleanup(func() { onStart.Store(nil) })

	return func() int {
		runtime.GC()
		mu.Lock()
		defer mu.Unlock()
		n := 0
		for _, reachable := range held {
			if reachable() {
				n++
			}
		}

		return n
	}
}
