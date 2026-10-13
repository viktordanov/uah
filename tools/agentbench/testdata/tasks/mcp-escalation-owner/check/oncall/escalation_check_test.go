package oncall

import "testing"

func TestEscalationIsCurrent(t *testing.T) {
	if Service != "auth" || Owner != "sofia.rossi@example.com" {
		t.Fatalf("Service %q, Owner %q; want auth and sofia.rossi@example.com", Service, Owner)
	}
}
