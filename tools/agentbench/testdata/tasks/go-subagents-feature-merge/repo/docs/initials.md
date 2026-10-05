# initials

`initials.Of(name string) string` returns the uppercase first letter of each part of a name:

1. Parts are separated by spaces and hyphens; empty parts are skipped.
2. A part's first letter is its first rune, uppercased (`élodie` gives `É`).

| Name | Initials |
| --- | --- |
| `ada lovelace` | `AL` |
| `Jean-Luc Picard` | `JLP` |
| `  grace   hopper ` | `GH` |
| `` | `` |
