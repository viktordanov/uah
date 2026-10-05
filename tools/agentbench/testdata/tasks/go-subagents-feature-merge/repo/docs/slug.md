# slug

`slug.Make(s string) string` returns the URL slug of s:

1. Letters are lowercased. ASCII letters and digits are kept.
2. Every run of other characters (spaces, punctuation, non-ASCII) becomes one `-`.
3. No `-` at the start or the end.

| Input | Slug |
| --- | --- |
| `Hello, World!` | `hello-world` |
| `  Go 1.24 -- release ` | `go-1-24-release` |
| `Crème brûlée` | `cr-me-br-l-e` |
| `!!!` | `` (empty) |
