// Package home is uah's one home, as Codex has ~/.codex and Claude Code has
// ~/.claude: the configuration, the instructions, hook trust, MCP
// credentials, sessions, run records, the index, images, the model cache,
// the prompt history, and logs all live in ~/.uah, or in $UAH_HOME when it is set.
package home

import (
	"os"
	"path/filepath"
)

// Environment variables that place uah's files.
const (
	// Env overrides the home directory.
	Env = "UAH_HOME"
	// EnvConfig overrides the user configuration file (--config).
	EnvConfig = "UAH_CONFIG"
	// EnvStateDir overrides where sessions and run records live (--state-dir).
	EnvStateDir = "UAH_STATE_DIR"
	// EnvExtraConfig names one more configuration layer, merged after the
	// layers in config.d.
	EnvExtraConfig = "UAH_EXTRA_CONFIG"
)

// Variables are the environment variables, besides Env, that change what
// uah does: where its files are, the flags' defaults, the model's endpoint,
// key, and attempts, and the legacy names it warns about. Tests and the
// perf harness clear them, so a developer's settings stay out of a run;
// TestVariables keeps the list to what uah reads.
var Variables = []string{
	EnvConfig, EnvStateDir, EnvExtraConfig, "UAGENT_CONFIG", "UAGENT_STATE_DIR",
	"UAH_SANDBOX", "UAH_ASK", "UAH_ADAPTIVE_EFFORT", "UAH_CONTEXT_PREPARATION", "UAH_EFFORT_UPDATES", "UAH_REQUEST_USER_INPUT", "UAH_TOOL_BRIDGE", "UAH_MODEL_VERBOSITY",
	"UAH_LLM_PROVIDER", "UAH_LLM_MODEL", "UAH_LLM_BASE_URL",
	"UAH_LLM_MAX_ATTEMPTS", "UAH_LLM_API_KEY", "CODEX_REFRESH_TOKEN_URL_OVERRIDE",
}

// Name is the home directory's name in the user's home directory, and the
// project directory's name in a workspace.
const Name = ".uah"

// Dir is $UAH_HOME, else ~/.uah (./.uah when there is no home directory).
func Dir() string {
	if dir := os.Getenv(Env); dir != "" {
		return filepath.Clean(dir)
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return Name
	}

	return filepath.Join(userHome, Name)
}

// Legacy variables that uah no longer reads, with the variable that replaced
// each one.
var Legacy = []struct{ Old, New string }{
	{"UAGENT_CONFIG", EnvConfig},
	{"UAGENT_STATE_DIR", EnvStateDir},
}

// Warnings name each legacy variable that is set and the variable to use
// instead, one line each.
func Warnings(getenv func(string) string) []string {
	var out []string
	for _, v := range Legacy {
		if getenv(v.Old) != "" {
			out = append(out, v.Old+" is no longer read; set "+v.New+" instead")
		}
	}

	return out
}
