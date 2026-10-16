package state_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/viktordanov/uah/internal/tui/state"
)

func TestHeatmap(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC) // a Thursday
	counts := map[string]int{"2026-09-24": 4, "2026-09-21": 1, "2026-09-15": 2}
	assert.Equal(t, `activity · last 3 weeks · 7 runs
Mon ··░
    ·▒·
Wed ···
    ··█
Fri ··
    ··
Sun ··`, state.Heatmap(counts, now, 3))
}

// A busier day outside the grid does not pale the days it shows.
func TestHeatGrid_PeakOfTheDaysShown(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	values := map[string]int64{"2026-09-24": 40, "2026-01-05": 1_000_000}
	rows, start := state.HeatGrid(values, now, 1, nil)
	assert.Equal(t, "2026-09-21", start.Format(time.DateOnly))
	assert.Equal(t, "█", rows[3], "Thursday, the busiest day shown")
}
