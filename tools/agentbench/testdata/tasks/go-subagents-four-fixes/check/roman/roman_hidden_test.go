package roman

import "testing"

func TestFromRomanHidden(t *testing.T) {
	for in, want := range map[string]int{"XIV": 14, "MCMXCIV": 1994, "iv": 4, "MMXXVI": 2026, "XL": 40, "CDXLIV": 444, "III": 3} {
		if got, err := FromRoman(in); err != nil || got != want {
			t.Errorf("FromRoman(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "ABC", "X1"} {
		if _, err := FromRoman(bad); err == nil {
			t.Errorf("FromRoman(%q) did not fail", bad)
		}
	}
	for n := 1; n < 4000; n++ {
		if got, err := FromRoman(ToRoman(n)); err != nil || got != n {
			t.Fatalf("round trip %d: %d, %v", n, got, err)
		}
	}
}
