package pricing

import (
	"testing"
	"time"
)

// TestSlowService2 runs against a slow simulated service.
func TestSlowService2(t *testing.T) {
	time.Sleep(15 * time.Second)
}
