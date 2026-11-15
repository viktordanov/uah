package engine_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/viktordanov/uah/internal/engine"
)

// TestWaitTimeout: no timeout waits max_wait, a timeout is clamped to the
// bounds before it is converted, and a minimum above max_wait yields.
func TestWaitTimeout(t *testing.T) {
	ms := func(v float64) *float64 { return &v }
	for _, tc := range []struct {
		name     string
		timeout  *float64
		min, max time.Duration
		want     time.Duration
	}{
		{"default", nil, time.Second, time.Hour, time.Hour},
		{"in range", ms(120000), time.Second, time.Hour, 2 * time.Minute},
		{"below the minimum", ms(-1), time.Second, time.Hour, time.Second},
		{"above max_wait", ms(7200000), time.Second, time.Hour, time.Hour},
		{"huge number", ms(1e100), time.Second, time.Hour, time.Hour},
		{"max_wait below the minimum", ms(10), time.Minute, 30 * time.Second, 30 * time.Second},
		{"default below the minimum", nil, time.Minute, 30 * time.Second, 30 * time.Second},
	} {
		assert.Equal(t, tc.want, engine.WaitTimeout(tc.timeout, tc.min, tc.max), tc.name)
	}
}
