package pricing

import (
	"testing"
	"time"
)

// TestSlowService1 runs against a slow simulated service.
func TestSlowService1(t *testing.T) {
	time.Sleep(15 * time.Second)
}
