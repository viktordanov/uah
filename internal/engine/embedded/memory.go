package embedded

import (
	"sync"
	"sync/atomic"
	"time"
)

// releaseDelay is how long after the engine's last run ends it returns the
// free heap to the OS: time for the session to drop the run, so the run's
// memory is free too.
const releaseDelay = time.Second

// onStart, when set, sees each run that started, for a test.
var onStart atomic.Pointer[func(*wiring, *agent)]

// runs counts an engine's runs, subagents' included, and returns the free
// heap to the OS once none is left (Config.ReturnMemory). A run's start
// decodes the whole session file, restores the history, and encodes it for
// the first request, so on a long session the heap grows to several times
// the file's size. The Go runtime keeps those pages: an idle process
// collects again only at the forced collection every two minutes, and its
// scavenger then returns them a little at a time.
type runs struct {
	// free returns the free heap (debug.FreeOSMemory) delay after the last
	// run ends; nil never does.
	free  func()
	delay time.Duration

	active atomic.Int64
	mu     sync.Mutex
	timer  *time.Timer
}

func (r *runs) started() { r.active.Add(1) }

// ended counts a run's end. The last one frees after r.delay, unless
// another run started by then; a later last run moves it back.
func (r *runs) ended() {
	if r.active.Add(-1) > 0 || r.free == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.timer != nil {
		r.timer.Reset(r.delay)

		return
	}
	r.timer = time.AfterFunc(r.delay, func() {
		if r.active.Load() == 0 {
			r.free()
		}
	})
}
