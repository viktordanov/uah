// Command textkit runs the text utilities; see the README.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"example.com/textkit/initials"
	"example.com/textkit/slug"
	"example.com/textkit/wrap"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run runs one command and returns the exit status.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return usage(stderr)
	}
	switch args[0] {
	case "slug":
		fmt.Fprintln(stdout, slug.Make(strings.Join(args[1:], " ")))
	case "initials":
		fmt.Fprintln(stdout, initials.Of(strings.Join(args[1:], " ")))
	case "wrap":
		fs := flag.NewFlagSet("wrap", flag.ContinueOnError)
		fs.SetOutput(stderr)
		w := fs.Int("w", 40, "width")
		if err := fs.Parse(args[1:]); err != nil {
			return 2
		}
		for _, l := range wrap.Lines(strings.Join(fs.Args(), " "), *w) {
			fmt.Fprintln(stdout, l)
		}
	default:
		return usage(stderr)
	}

	return 0
}

func usage(stderr io.Writer) int {
	fmt.Fprintln(stderr, "usage: textkit slug|wrap|initials [args]")

	return 2
}
