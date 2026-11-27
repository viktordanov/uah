package main

import (
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/urfave/cli/v3"
)

// argsAfterDash refuses a command line that goes on after the lone "-"
// where the parser stopped, the prompt or the instructions read from
// stdin: urfave/cli stops at the first "-" that is not a flag's value and
// drops what follows, flags included, so `uah exec - -m x` would run
// without -m, and `uah exec sum - --ephemeral` would keep its session,
// and say nothing.
func argsAfterDash(cmd *cli.Command) error {
	line := commandLine(cmd)
	if i := dashStop(cmd, line); i >= 0 && i < len(line)-1 {
		return cli.Exit("uah reads no argument after a lone - (stdin): put the flags before it; for a - in the text, quote the text or put -- before it", exitUsage)
	}

	return nil
}

// commandLine is what was typed after the command's name: the root's
// arguments are the command line from the subcommand's name on, as typed.
func commandLine(cmd *cli.Command) []string {
	raw := cmd.Root().Args().Slice()
	if len(raw) == 0 || (raw[0] != cmd.Name && !slices.Contains(cmd.Aliases, raw[0])) {
		return nil
	}

	return raw[1:]
}

// dashStop is the index in line of the lone "-" where the parser stops,
// or -1: it walks the line as the parser does, a flag that is not boolean
// taking the next word as its value. The parser keeps the rest of the line
// whole after "--", a subcommand's name, or a word such as "-1" that is
// not a flag, so a "-" after one of those is the prompt's.
func dashStop(cmd *cli.Command, line []string) int {
	for i := 0; i < len(line); i++ {
		word := strings.TrimSpace(line[i])
		switch {
		case word == "--":
			return -1
		case word == "-":
			return i
		case word == "":
			continue
		case word[0] != '-':
			if cmd.Command(word) != nil {
				return -1
			}

			continue
		case word[1] != '-' && !unicode.IsLetter(firstRune(word[1:])):
			return -1
		case strings.Contains(word, "="):
			continue
		}
		if takesValue(cmd, strings.TrimLeft(word, "-")) {
			i++
		}
	}

	return -1
}

func firstRune(s string) rune {
	r, _ := utf8.DecodeRuneInString(s)

	return r
}

// takesValue reports whether name is a flag of cmd or of a command above
// it that takes the next word as its value: one that is not boolean.
func takesValue(cmd *cli.Command, name string) bool {
	for _, c := range cmd.Lineage() {
		for _, f := range c.Flags {
			if !slices.Contains(f.Names(), name) {
				continue
			}
			switch f := f.(type) {
			case *cli.BoolFlag, *cli.BoolWithInverseFlag:
				return false
			case interface{ IsBoolFlag() bool }: // known once the command runs
				return !f.IsBoolFlag()
			}

			return true
		}
	}

	return false
}
