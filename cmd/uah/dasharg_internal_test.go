package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/urfave/cli/v3"
)

// TestDashStop finds the lone "-" where the parser stops: not one that is
// a flag's value, nor one after "--", a subcommand's name, or a word such
// as "-1", after which the parser keeps the line whole.
func TestDashStop(t *testing.T) {
	cmd := &cli.Command{Name: "exec", Flags: []cli.Flag{
		&cli.BoolFlag{Name: "ephemeral"},
		&cli.StringFlag{Name: "model", Aliases: []string{"m"}},
		&cli.StringFlag{Name: "output-last-message", Aliases: []string{"o"}},
	}, Commands: []*cli.Command{{Name: "sub"}}}
	for want, lines := range map[int][][]string{
		-1: {
			{}, {"hi"}, {"-o", "-", "hi"}, {"--", "-", "x"}, {"--model", "-"},
			{"explain", "-1", "-", "2"}, {"-m", "x", "-.5", "-", "y"}, {"sub", "-", "x"}, {"", "x"},
		},
		0:  {{"-"}, {"-", "-m", "x"}, {"-", "--ephemeral", "-"}},
		1:  {{"sum", "-", "--ephemeral"}, {"-m=x", "-"}, {"--ephemeral", "-", "x"}},
		2:  {{"-o", "-", "-"}, {"-m", "x", "-", "y"}},
	} {
		for _, line := range lines {
			assert.Equal(t, want, dashStop(cmd, line), "%q", line)
		}
	}
}
