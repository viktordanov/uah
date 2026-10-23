package render

import (
	"fmt"
	"math"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/tui/state"
)

// TestScrolledFrameFollowsTheWindow: a frame scrolled to the top of a
// transcript ten times as long allocates about as much, since only the
// window's lines are copied and the lines below it are counted. The
// fewest bytes of a few frames count, since tests running in parallel
// allocate too.
func TestScrolledFrameFollowsTheWindow(t *testing.T) {
	frameBytes := func(runs int) uint64 {
		s := longTranscript(runs)
		c := NewCache(Amber)
		s = scrolledTo(s, c, "top")
		least := uint64(math.MaxUint64)
		for range 5 {
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			frame(s, c)
			runtime.ReadMemStats(&after)
			least = min(least, after.TotalAlloc-before.TotalAlloc)
		}

		return least
	}
	small, large := frameBytes(200), frameBytes(2000)
	assert.Less(t, float64(large)/float64(small), 2.0, "%d bytes, then %d: the bytes grow with the lines below the window", small, large)
}

// TestScrollMovesTheWindowByLines: each line scrolled moves the window's
// rows and their transcript lines down one row, from the bottom to the
// banner's first line at the top, pinned or not; a scroll past the top
// draws the top.
func TestScrollMovesTheWindowByLines(t *testing.T) {
	s := longTranscript(12)
	c := NewCache(Amber)
	top := scrolledTo(s, c, "top")
	limit := c.MaxScroll()
	require.Positive(t, limit)
	type window struct {
		rows  []state.TextPos
		lines []string
	}
	draw := func(s state.State) window {
		frame(s, c)

		return window{rows: append([]state.TextPos(nil), c.rows...), lines: append([]string(nil), c.window...)}
	}
	at := func(scroll int, pin bool) window {
		s, _ := state.Reduce(s, state.ScrollBy{Lines: scroll})
		if pin && scroll > 0 {
			frame(s, c)
			pos, below := c.Anchor()
			s, _ = state.Reduce(s, state.Anchored{At: pos, Scroll: below, Width: 100})
		}

		return draw(s)
	}
	prev := at(0, false)
	for scroll := 1; scroll <= limit; scroll++ {
		got := at(scroll, false)
		n := len(got.rows)
		assert.Equal(t, prev.rows[:n-1], got.rows[1:], "scroll %d", scroll)
		assert.Equal(t, prev.lines[:n-1], got.lines[1:], "scroll %d", scroll)
		assert.Equal(t, got, at(scroll, true), "scroll %d, pinned", scroll)
		prev = got
	}
	assert.Equal(t, state.TextPos{Key: state.BannerKey}, prev.rows[0])
	assert.Equal(t, prev, draw(top))
	assert.Equal(t, prev, at(limit+50, false), "past the top")
}

// BenchmarkScrolledFrame draws a frame of a long transcript, from a warm
// cache, with the window at its top, in its middle, and at its bottom, and
// pinned there as the shell pins it (state.Anchored).
func BenchmarkScrolledFrame(b *testing.B) {
	for _, runs := range []int{200, 2000, 8000} {
		for _, at := range []string{"top", "middle", "bottom"} {
			b.Run(fmt.Sprintf("%d/%s", runs, at), func(b *testing.B) {
				s := longTranscript(runs)
				c := NewCache(Amber)
				s = scrolledTo(s, c, at)
				frame(s, c)
				b.ReportAllocs()
				for b.Loop() {
					frame(s, c)
				}
			})
		}
	}
}

// scrolledTo scrolls s to the top, the middle, or the bottom of its
// transcript as drawn by frame, and pins it as the shell does.
func scrolledTo(s state.State, c *Cache, at string) state.State {
	s, _ = state.Reduce(s, state.ScrollBy{Lines: 1 << 30})
	frame(s, c)
	limit := c.MaxScroll()
	s, _ = state.Reduce(s, state.ScrollToBottom{})
	switch at {
	case "bottom":
		return s
	case "middle":
		limit /= 2
	}
	s, _ = state.Reduce(s, state.ScrollBy{Lines: limit})
	frame(s, c)
	pos, scroll := c.Anchor()
	s, _ = state.Reduce(s, state.Anchored{At: pos, Scroll: scroll, Width: 100})

	return s
}
