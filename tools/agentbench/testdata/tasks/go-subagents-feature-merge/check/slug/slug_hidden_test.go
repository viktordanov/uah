package slug

import "testing"

func TestHiddenMake(t *testing.T) {
	for in, want := range map[string]string{
		"Hello, World!":         "hello-world",
		"  Go 1.24 -- release ": "go-1-24-release",
		"Crème brûlée":          "cr-me-br-l-e",
		"!!!":                   "",
		"already-a-slug":        "already-a-slug",
		"ABC123":                "abc123",
	} {
		if got := Make(in); got != want {
			t.Errorf("Make(%q) = %q, want %q", in, got, want)
		}
	}
}
