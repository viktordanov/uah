package inventory

import (
	"errors"
	"testing"
)

func TestReserveTheLastUnits(t *testing.T) {
	s := New(map[string]int{"mug": 3})
	if err := s.Reserve("mug", 3); err != nil {
		t.Fatalf("reserving all 3 mugs: %v", err)
	}
	if got := s.Available("mug"); got != 0 {
		t.Fatalf("Available = %d, want 0", got)
	}
}

func TestReserveTooMany(t *testing.T) {
	s := New(map[string]int{"mug": 1})
	if err := s.Reserve("mug", 2); !errors.Is(err, ErrOutOfStock) {
		t.Fatalf("err = %v", err)
	}
}

func TestRelease(t *testing.T) {
	s := New(map[string]int{"mug": 5})
	_ = s.Reserve("mug", 2)
	s.Release("mug", 5)
	if got := s.Available("mug"); got != 5 {
		t.Fatalf("Available = %d", got)
	}
}
