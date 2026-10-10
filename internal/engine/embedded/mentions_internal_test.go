package embedded

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestMentions: a "$" that starts a word names a skill, without trailing
// punctuation, once each; "$" inside a word or doubled names none.
func TestMentions(t *testing.T) {
	for text, want := range map[string][]string{
		"use $imagegen.":                 {"imagegen"},
		"$imagegen: draw, then $go-test": {"imagegen", "go-test"},
		"($plugin:skill) and $imagegen":  {"plugin:skill", "imagegen"},
		"$a and $a again":                {"a"},
		"cost$5, $$x, a$b, $ alone, $-":  nil,
		"`$release`\n$release\t$triage":  {"release", "triage"},
	} {
		assert.Equal(t, want, mentions(text), text)
	}
}
