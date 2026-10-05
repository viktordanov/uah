# wrap

`wrap.Lines(s string, width int) []string` wraps s into lines of at most width bytes:

1. Words are separated by any run of whitespace; lines hold words joined by one space.
2. Greedy: a word goes on the current line if it fits, else it starts the next line.
3. A word longer than width is not broken: it gets a line of its own.
4. A width below 1 means no limit: one line with every word.
5. Text with no words gives nil.

`Lines("the quick brown fox", 10)` is `["the quick", "brown fox"]`.
