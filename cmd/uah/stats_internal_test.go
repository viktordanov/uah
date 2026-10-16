package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A date is local midnight, and --until includes it: the next midnight; a
// time is itself, and --until includes it too.
func TestParseDate(t *testing.T) {
	loc := time.FixedZone("test", 3*60*60)
	since, err := parseDate("2026-10-01", false, loc)
	require.NoError(t, err)
	assert.Equal(t, time.Date(2026, 10, 1, 0, 0, 0, 0, loc), since)
	until, err := parseDate("2026-10-01", true, loc)
	require.NoError(t, err)
	assert.Equal(t, time.Date(2026, 10, 2, 0, 0, 0, 0, loc), until)
	at, err := parseDate("2026-10-01T08:00:00Z", true, loc)
	require.NoError(t, err)
	assert.Equal(t, time.Date(2026, 10, 1, 8, 0, 0, 1, time.UTC), at)
	none, err := parseDate("", true, loc)
	require.NoError(t, err)
	assert.True(t, none.IsZero())
	_, err = parseDate("yesterday", false, loc)
	assert.Error(t, err)

	santiago, err := time.LoadLocation("America/Santiago") // clocks skip 2026-09-06's midnight
	require.NoError(t, err)
	since, err = parseDate("2026-09-06", false, santiago)
	require.NoError(t, err)
	assert.Equal(t, "2026-09-06 01:00", since.Format("2006-01-02 15:04"), "the day's first instant")
	until, err = parseDate("2026-09-05", true, santiago)
	require.NoError(t, err)
	assert.Equal(t, since, until, "the day after's first instant")
}
