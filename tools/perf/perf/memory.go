package perf

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"runtime/metrics"
	"time"

	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/testing/fakellm"
)

// memoryTurns is how many streamed turns the memory scenario runs.
const memoryTurns = 3

// memorySettle is how long the memory scenario waits after its last turn
// before it reads what the process keeps: past the engine's return of the
// free heap, a second after its last run.
const memorySettle = 2 * time.Second

// memory resumes the session as the TUI does and runs memoryTurns turns
// that stream a long answer, then waits as an idle TUI would: the live heap
// the open session kept (live_mb, after a collection), the heap the process
// kept from the OS (retained_mb, before one), and the memory the system
// charges it (os_mb: the footprint on macOS, the resident set on Linux),
// each against the session just opened.
func (r *run) memory(t target) ([]Named, error) {
	name := "memory/" + t.name
	e, err := r.env(name)
	if err != nil {
		return nil, err
	}
	defer e.Close()
	fx, err := t.build(e)
	if err != nil {
		return nil, err
	}
	e.LLM.Light = true
	deltas := make([]string, 400)
	for i := range deltas {
		deltas[i] = fmt.Sprintf("The handler checks the session and the token before step %d, ", i)
	}
	var s *session.Session
	sample, err := r.probe(name, e).Measure(func(x *Sample) error {
		var err error
		if s, _, err = e.Open(r.ctx, fx.SessionID, true); err != nil {
			return err
		}
		debug.FreeOSMemory()
		live, retained := heapNow()
		charged := osMemory()
		for i := range memoryTurns {
			e.LLM.Script(fakellm.Reply{Reasoning: []string{"**Planning**\n\nReading the handler first."}, Deltas: deltas})
			if _, err := Turn(s, fmt.Sprintf("Explain the handler, part %d.", i+1)); err != nil {
				return err
			}
		}
		time.Sleep(memorySettle)
		_, retainedAfter := heapNow()
		chargedAfter := osMemory()
		runtime.GC()
		liveAfter, _ := heapNow()
		x.Extra["live_mb"] = mb(int64(liveAfter) - int64(live))
		x.Extra["retained_mb"] = mb(int64(retainedAfter) - int64(retained))
		x.Extra["os_mb"] = mb(int64(chargedAfter) - int64(charged))

		return nil
	}, func() { closeSession(s) })

	return []Named{{Name: name, Fixture: &fx, Sample: sample}}, err
}

// heapNow reads the live heap as of the last collection and the heap the
// runtime keeps from the OS: its objects, and its free and unused pages
// not yet returned.
func heapNow() (live, retained uint64) {
	s := []metrics.Sample{
		{Name: "/gc/heap/live:bytes"},
		{Name: "/memory/classes/heap/objects:bytes"},
		{Name: "/memory/classes/heap/free:bytes"},
		{Name: "/memory/classes/heap/unused:bytes"},
	}
	metrics.Read(s)

	return s[0].Value.Uint64(), s[1].Value.Uint64() + s[2].Value.Uint64() + s[3].Value.Uint64()
}
