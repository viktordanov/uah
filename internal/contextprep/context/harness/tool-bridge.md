---
id: tool-bridge
description: How to call MCP tools from a script with uah tool
when: {tool_bridge: true}
---
Scripted tool calls: inside any command, `uah tool` calls this session's MCP tools, so one script can call them, filter or join what they return, and print only what you need.
- `uah tool list` names the tools, `uah tool describe <name>` gives one's input schema, and `uah tool call <name> '<json>'` prints its result (`-` reads the arguments from stdin).
- Use it when a result is large or paged, when you need a few fields of many records, or when one call's result feeds the next: `uah tool call mcp__docs__search '{"q":"retry"}' | jq -r '.[].id' | xargs -I{} uah tool call mcp__docs__get '{"id":"{}"}' | jq '.title'`.
- A direct call is still right for one small lookup whose whole result you want to read.
- A call is checked as your own call would be (policy, hooks, approval); a refusal exits 1 with the reason on stderr.
