package embedded

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestRuns_FreeAfterTheLastRun: the free heap goes back once no run is
// left, a delay after the last one ends, and not while another runs.
func TestRuns_FreeAfterTheLastRun(t *testing.T) {
	var freed atomic.Int64
	r := &runs{free: func() { freed.Add(1) }, delay: 20 * time.Millisecond}

	r.started()
	r.started()
	r.ended()
	time.Sleep(60 * time.Millisecond)
	assert.Equal(t, int64(0), freed.Load(), "a run is still going")

	r.ended()
	assert.Eventually(t, func() bool { return freed.Load() == 1 }, time.Second, 5*time.Millisecond)

	r.started()
	r.ended()
	r.started() // before the delay: this run's end frees instead
	time.Sleep(60 * time.Millisecond)
	assert.Equal(t, int64(1), freed.Load(), "not while a run is going")
	r.ended()
	assert.Eventually(t, func() bool { return freed.Load() == 2 }, time.Second, 5*time.Millisecond)
}

// TestRuns_Off: an engine without ReturnMemory never frees.
func TestRuns_Off(t *testing.T) {
	r := &runs{}
	r.started()
	r.ended()
	assert.Nil(t, r.timer)
}
