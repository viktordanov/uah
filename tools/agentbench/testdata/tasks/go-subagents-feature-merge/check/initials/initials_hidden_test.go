package initials

import "testing"

func TestHiddenOf(t *testing.T) {
	for in, want := range map[string]string{
		"ada lovelace":      "AL",
		"Jean-Luc Picard":   "JLP",
		"  grace   hopper ": "GH",
		"":                  "",
		"élodie durand":     "ÉD",
	} {
		if got := Of(in); got != want {
			t.Errorf("Of(%q) = %q, want %q", in, got, want)
		}
	}
}
