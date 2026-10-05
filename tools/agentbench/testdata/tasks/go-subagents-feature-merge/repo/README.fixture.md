# textkit

Small text utilities and a command that runs them.

- `slug`: URL slugs (docs/slug.md).
- `wrap`: word wrapping (docs/wrap.md).
- `initials`: a name's initials (docs/initials.md).

## The command

`go run ./cmd/textkit <command> [args]`:

- `textkit slug <text...>` prints the slug of the words joined with spaces.
- `textkit wrap -w <width> <text...>` prints the words joined with spaces, wrapped to the width, one line per output line. `-w` defaults to 40.
- `textkit initials <name...>` prints the initials of the words joined with spaces.

Each prints its result followed by a newline and exits 0. With no command, or an unknown one, it prints `usage: textkit slug|wrap|initials [args]` to stderr and exits 2. `main` calls `run(os.Args[1:], os.Stdout, os.Stderr)` and exits with its result.
