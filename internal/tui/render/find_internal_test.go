package render

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCellMatches(t *testing.T) {
	assert.Equal(t, [][2]int{{0, 5}, {6, 11}}, cellMatches("Hello HELLO", []rune("hello")), "any case")
	assert.Equal(t, [][2]int{{2, 4}, {13, 15}}, cellMatches("日本語 abc 日本", []rune("本")), "wide characters take two cells")
	assert.Equal(t, [][2]int{{0, 2}}, cellMatches("aaa", []rune("aa")), "no overlaps")
	assert.Empty(t, cellMatches("abc", nil))
}
