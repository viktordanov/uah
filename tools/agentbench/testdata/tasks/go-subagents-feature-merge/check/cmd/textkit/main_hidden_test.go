package main

import (
	"bytes"
	"testing"
)

func TestHiddenRun(t *testing.T) {
	cases := []struct {
		args     []string
		out, err string
		code     int
	}{
		{[]string{"slug", "Hello,", "World!"}, "hello-world\n", "", 0},
		{[]string{"initials", "ada", "lovelace"}, "AL\n", "", 0},
		{[]string{"wrap", "-w", "10", "the", "quick", "brown", "fox"}, "the quick\nbrown fox\n", "", 0},
		{[]string{"wrap", "one", "two"}, "one two\n", "", 0},
		{[]string{"shout"}, "", "usage: textkit slug|wrap|initials [args]\n", 2},
		{nil, "", "usage: textkit slug|wrap|initials [args]\n", 2},
	}
	for _, c := range cases {
		var out, errb bytes.Buffer
		code := run(c.args, &out, &errb)
		if code != c.code || out.String() != c.out || (c.err != "" && errb.String() != c.err) {
			t.Errorf("run(%q) = %d, %q, %q; want %d, %q, %q", c.args, code, out.String(), errb.String(), c.code, c.out, c.err)
		}
	}
}
