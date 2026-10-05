package embedded

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestWakePolicyHolds(t *testing.T) {
	p := wakePolicy(0)
	assert.Equal(t, wakeHold, p.Hold)
	assert.NotNil(t, p.Progress)
}
