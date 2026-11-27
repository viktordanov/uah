package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestPendingSettings: every session names the settings until one opened,
// which records them once; later sessions name nothing.
func TestPendingSettings(t *testing.T) {
	loads, records := 0, 0
	var log strings.Builder
	fail := errors.New("read-only home")
	p := &pendingSettings{log: &log, load: func() ([]string, func() error) {
		loads++

		return []string{"New setting [advisor]"}, func() error {
			records++
			if records == 1 {
				return fail
			}

			return nil
		}
	}}

	notes, _ := p.take() // this session failed to open
	assert.Equal(t, []string{"New setting [advisor]"}, notes)
	notes, opened := p.take()
	assert.Equal(t, []string{"New setting [advisor]"}, notes, "the next session names them again")
	opened()
	assert.Contains(t, log.String(), "read-only home", "a record that failed is logged")
	notes, opened = p.take()
	assert.NotEmpty(t, notes, "and tried again")
	opened()
	opened()
	notes, _ = p.take()
	assert.Empty(t, notes, "named once")
	assert.Equal(t, 1, loads)
	assert.Equal(t, 2, records)
}
