package schedule

import (
	"testing"
	"time"
)

func TestNextRunSameWeek(t *testing.T) {
	from := time.Date(2026, 3, 2, 9, 30, 0, 0, time.UTC) // a Monday
	got := NextRun(from, 8, []time.Weekday{time.Wednesday})
	if want := time.Date(2026, 3, 4, 8, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("NextRun = %v, want %v", got, want)
	}
}

func TestNextRunOnSunday(t *testing.T) {
	from := time.Date(2026, 3, 6, 12, 0, 0, 0, time.UTC) // a Friday
	got := NextRun(from, 10, []time.Weekday{time.Sunday})
	if want := time.Date(2026, 3, 8, 10, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("NextRun = %v, want %v", got, want)
	}
}

func TestFormat(t *testing.T) {
	if got := Format(time.Date(2026, 3, 8, 10, 0, 0, 0, time.UTC)); got != "Sun 2026-03-08 10:00" {
		t.Fatal(got)
	}
}
