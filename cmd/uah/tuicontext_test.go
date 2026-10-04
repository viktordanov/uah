package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

// ctrl+c in ctrl+g's editor cancels main's signal context; the TUI's
// dependencies, such as /status's activity query, keep working on theirs.
func TestTUIContextOutlivesMainsSignalContext(t *testing.T) {
	type key struct{}
	parent, cancel := context.WithCancel(context.WithValue(context.Background(), key{}, "notes"))
	ctx := tuiContext(parent)
	cancel()
	assert.NoError(t, ctx.Err())
	assert.Equal(t, "notes", ctx.Value(key{}))
}
