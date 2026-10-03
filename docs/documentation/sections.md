# Memoria section IDs

Section IDs identify a concern within a README. Keep the ID stable when a heading changes.

| ID | Concern |
| --- | --- |
| `overview` | What the README covers and why it exists |
| `usage` | Commands, inputs, and results |
| `tui` | Keys, commands, and views of the terminal UI |
| `engines` | The engine |
| `compaction` | Compaction and the context meter |
| `instructions` | AGENTS.md discovery and the host prompt |
| `context`, `contextprep` | Context preparation: the root README's introduction to the feature, and its summary under How it works |
| `hooks` | Hook events, contract, and trust |
| `mcp` | MCP servers: configuration, tools, and results |
| `patch`, `format`, `apply`, `diff`, `tool` | File edits: the patch package's README and its summary in the root README |
| `lifecycle` | How a long-lived component starts, runs, fails, and stops |
| `calls` | How a request flows through the components that serve it |
| `approvals` | What asks the user before it runs, and how |
| `auth` | Authorization, logins, and where credentials are stored |
| `extending` | Where and how to add to a package |
| `subagents` | Subagent tools, `[agents]` keys, and role files |
| `goals`, `goal`, `texts`, `display` | Goals (`/goal`): the root README's task and summary, the session's and the TUI's sections, and the goal package's README: the goal, the texts the model reads, and what the user sees |
| `seam`, `parity`, `lifecycle`, `tools`, `events`, `fork`, `watch`, `resume`, `limits`, `roles`, `extending` | The subagent package's README: its contract with the engine, a child's parity with the root session, a child's lifecycle, the tools, events and hooks, forking, watching a child, resume, limits, roles, and how to extend it |
| `configuration` | Configuration files and precedence |
| `development` | Building, testing, linting, and CI |

Write the explanation first, then map it to the files that support it:

```markdown
<!-- memoria:section id="hooks" files="internal/hooks/hooks.go internal/hooks/exec.go" -->
## Hooks

Explain the contract.
<!-- /memoria:section -->
```

Paths are literal, relative to the README, and must name files that the README covers: not another README and not a file in a folder handed off to one. Do not use globs.
