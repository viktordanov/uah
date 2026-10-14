package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A table measures a cell without its styles, so a styled cell keeps its
// column, a pipe gets no escapes, and a capped column lets a long cell run
// past it in its own row.
func TestTableAlignsStyledCells(t *testing.T) {
	var tb table
	tb.capColumn(1, 5)
	tb.header("A", "B", "C")
	tb.row(nil, cliStyles.ok.Render("yes"), "x", "1")
	tb.row(&cliStyles.dim, "no", "longer than five", "2")
	tb.row(&cliStyles.dim, "é", "y", "3")
	var buf bytes.Buffer
	require.NoError(t, tb.write(&buf))
	assert.Equal(t, strings.Join([]string{
		"A    B  C",
		"yes  x  1",
		"no   longer than five  2",
		"é    y  3",
		"",
	}, "\n"), buf.String())
}

func TestTrimZeros(t *testing.T) {
	for in, want := range map[string]string{"62500.0000000000": "62500", "12.50": "12.5", "0": "0", "100": "100", "unlimited": "unlimited"} {
		assert.Equal(t, want, trimZeros(in), in)
	}
}

func TestConfigValueKeepsItsSpaces(t *testing.T) {
	assert.Equal(t, `X="a  b"\nY=1\tz`, controlEscaper.Replace("X=\"a  b\"\nY=1\tz"))
}
