package pricing

import "testing"

func TestSubtotal(t *testing.T) {
	if got := Subtotal([]Line{{250, 2}, {99, 3}}); got != 797 {
		t.Fatalf("Subtotal = %d, want 797", got)
	}
}

func TestDiscountRoundsToNearestCent(t *testing.T) {
	// 10% of 1995 is 199.5, which rounds up to 200.
	if got := Discount(1995, "save10"); got != 1795 {
		t.Fatalf("Discount(1995, save10) = %d, want 1795", got)
	}
}

func TestDiscountUnknownCode(t *testing.T) {
	if got := Discount(1000, "NOPE"); got != 1000 {
		t.Fatalf("Discount = %d", got)
	}
}
