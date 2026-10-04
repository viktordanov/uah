// Adapted from openai/codex rust-v0.156.1 (Apache-2.0):
// codex-rs/protocol/src/shell_environment.rs (populate_env) and
// codex-rs/config/src/shell_environment_policy.rs.

package sandbox

import (
	"maps"
	"slices"
	"strings"
)

// Inherit values for EnvPolicy.Inherit.
const (
	InheritAll  = "all"
	InheritCore = "core"
	InheritNone = "none"
)

// coreEnv is Codex's UNIX_CORE_ENV_VARS, the variables Inherit "core" keeps.
var coreEnv = []string{
	"PATH", "SHELL", "TMPDIR", "TEMP", "TMP", "HOME", "LANG", "LC_ALL", "LC_CTYPE", "LOGNAME", "USER",
}

// defaultExcludes are dropped when IgnoreDefaultExcludes is false.
var defaultExcludes = []string{"*KEY*", "*SECRET*", "*TOKEN*"}

// EnvPolicy is Codex's shell_environment_policy: which environment variables
// a command gets. The zero value inherits everything, as Codex does.
type EnvPolicy struct {
	// Inherit is the starting set: "all" (the default, also for ""), "core",
	// or "none".
	Inherit string
	// IgnoreDefaultExcludes skips the *KEY*, *SECRET*, *TOKEN* filter; nil
	// means true, Codex's default.
	IgnoreDefaultExcludes *bool
	// Exclude drops the variables matching any of these case-insensitive
	// patterns, where "*" matches any run of characters and "?" one.
	Exclude []string
	// Set adds or overrides variables.
	Set map[string]string
	// IncludeOnly, when not empty, keeps only the matching variables, after
	// Set.
	IncludeOnly []string
}

// Apply returns the environment for a command from environ, in "KEY=value"
// form, in Codex's order: inherit, default excludes, Exclude, Set,
// IncludeOnly. Inherited variables keep their order; new ones from Set follow
// in key order.
func (p EnvPolicy) Apply(environ []string) []string {
	var keys []string
	vals := map[string]string{}
	add := func(k, v string) {
		if _, ok := vals[k]; !ok {
			keys = append(keys, k)
		}
		vals[k] = v
	}
	for _, kv := range environ {
		k, v, _ := strings.Cut(kv, "=")
		switch p.Inherit {
		case InheritNone:
			continue
		case InheritCore:
			if !slices.ContainsFunc(coreEnv, func(c string) bool { return strings.EqualFold(c, k) }) {
				continue
			}
		}
		add(k, v)
	}
	drop := func(patterns []string, keep bool) {
		keys = slices.DeleteFunc(keys, func(k string) bool {
			if matchesAny(k, patterns) == keep {
				return false
			}
			delete(vals, k)

			return true
		})
	}
	if p.IgnoreDefaultExcludes != nil && !*p.IgnoreDefaultExcludes {
		drop(defaultExcludes, false)
	}
	if len(p.Exclude) > 0 {
		drop(p.Exclude, false)
	}
	for _, k := range slices.Sorted(maps.Keys(p.Set)) {
		add(k, p.Set[k])
	}
	if len(p.IncludeOnly) > 0 {
		drop(p.IncludeOnly, true)
	}
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+vals[k])
	}

	return out
}

// Getenv is getenv as a command sees it under the policy: the policy
// applied to keys as getenv reads them. A key the policy drops reads as
// unset, and one Set adds reads as set.
func (p EnvPolicy) Getenv(getenv func(string) string, keys ...string) func(string) string {
	var environ []string
	for _, k := range keys {
		if v := getenv(k); v != "" {
			environ = append(environ, k+"="+v)
		}
	}
	env := map[string]string{}
	for _, kv := range p.Apply(environ) {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}

	return func(k string) string { return env[k] }
}

func matchesAny(name string, patterns []string) bool {
	return slices.ContainsFunc(patterns, func(p string) bool {
		return wildmatch(strings.ToLower(p), strings.ToLower(name))
	})
}

// wildmatch matches the whole of s against pattern, where "*" matches any
// run of bytes and "?" one byte, like the wildmatch crate Codex uses.
func wildmatch(pattern, s string) bool {
	px, sx := 0, 0
	star, mark := -1, 0
	for sx < len(s) {
		switch {
		case px < len(pattern) && (pattern[px] == '?' || pattern[px] == s[sx]):
			px++
			sx++
		case px < len(pattern) && pattern[px] == '*':
			star, mark = px, sx
			px++
		case star >= 0:
			mark++
			px, sx = star+1, mark
		default:
			return false
		}
	}
	for px < len(pattern) && pattern[px] == '*' {
		px++
	}

	return px == len(pattern)
}

// isDefault reports whether the policy passes the environment through
// unchanged.
func (p EnvPolicy) isDefault() bool {
	return (p.Inherit == "" || p.Inherit == InheritAll) && (p.IgnoreDefaultExcludes == nil || *p.IgnoreDefaultExcludes) &&
		len(p.Exclude) == 0 && len(p.Set) == 0 && len(p.IncludeOnly) == 0
}
