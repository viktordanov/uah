package embedded

import (
	"maps"

	"github.com/viktordanov/uah-core/harness/llm"

	"github.com/viktordanov/uah/internal/sandbox"
)

// jsonString is the JSON schema type of a string; schemaType is the key
// that names a type.
const (
	jsonString = "string"
	schemaType = "type"
)

func bashWithEscalation(t llm.Tool, p sandbox.Policy) llm.Tool {
	params := maps.Clone(t.Parameters)
	props, _ := params["properties"].(map[string]any)
	props = maps.Clone(props)
	props[argSandboxPermissions] = property(jsonString,
		"use_default runs the command in the sandbox. require_escalated asks the user to run it outside the sandbox; "+
			"use it only when the command needs access the sandbox blocks, and explain why in justification.",
		"enum", []any{"use_default", permEscalated})
	props[argJustification] = property(jsonString,
		"With require_escalated: one sentence the user reads to approve the command, such as why it needs the network.")
	props[argPrefixRule] = property("array",
		"With require_escalated, optionally: the command prefix the user may allow from now on, "+
			`such as ["npm", "install"], so similar commands run without asking.`,
		"items", map[string]any{schemaType: jsonString})
	params["properties"] = props
	t.Parameters = params
	t.Description += " " + sandboxNote(p)

	return t
}

// property is a JSON schema property with extra key and value pairs.
func property(typ, description string, extra ...any) map[string]any {
	p := map[string]any{schemaType: typ, "description": description}
	for i := 0; i+1 < len(extra); i += 2 {
		key, _ := extra[i].(string)
		p[key] = extra[i+1]
	}

	return p
}

// noNetworkNote follows the sandbox note when the sandbox has no network,
// so the model escalates a network command on its first run instead of
// after a failed run in the sandbox.
const noNetworkNote = " A command that needs the network, localhost included, fails in the sandbox, " +
	"so run it with require_escalated from the first try."

// sandboxNote tells the model what its commands may do.
func sandboxNote(p sandbox.Policy) string {
	network, offline := "network access", ""
	if !p.Network {
		network, offline = "no network access", noNetworkNote
	}
	switch p.Mode {
	case sandbox.ReadOnly:
		return "Commands run in a read-only sandbox: they can read files and write only $TMPDIR, with " + network + "." + offline
	case sandbox.WorkspaceWrite:
		return "Commands run in a sandbox: they can read any file, write only the workspace and temporary directories " +
			"(.git, .uah, .agents, and .codex stay read-only), and have " + network + "." + offline
	case sandbox.FullAccess:
	}

	return ""
}
