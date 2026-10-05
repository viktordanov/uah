// Command textkit runs the text utilities; see the README.
package main

import (
	"fmt"
	"io"
	"os"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run runs one command and returns the exit status.
func run(args []string, stdout, stderr io.Writer) int {
	fmt.Fprintln(stderr, "usage: textkit slug|wrap|initials [args]")

	return 2
}
