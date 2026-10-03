package sandbox

// The Seatbelt profile is adapted from openai/codex rust-v0.156.1
// (Apache-2.0), codex-rs/sandboxing/src/seatbelt.rs,
// create_seatbelt_command_args_with_profile. See seatbelt/LICENSE-codex and
// seatbelt/NOTICE-codex. Changed: only the legacy modes are built (full-disk
// read, and writes to Policy.Writable()); protected paths are passed as
// parameters instead of name regexes; the proxy, Unix socket, deny-read glob,
// restricted-read and daemon socket parts are left out.

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

//go:embed seatbelt/*.sbpl
var seatbeltFiles embed.FS

var (
	seatbeltBase        = mustReadSeatbelt("base.sbpl")
	seatbeltNetwork     = mustReadSeatbelt("network.sbpl")
	seatbeltPreferences = mustReadSeatbelt("preferences.sbpl")
)

func mustReadSeatbelt(name string) string {
	data, err := seatbeltFiles.ReadFile("seatbelt/" + name)
	if err != nil {
		panic(fmt.Sprintf("failed to read the embedded %s: %v", name, err))
	}

	return string(data)
}

// seatbeltParam is one -D parameter of a profile.
type seatbeltParam struct {
	key, value string
}

// SeatbeltProfile returns the Seatbelt profile for p and its parameters as
// sandbox-exec arguments ("-DKEY=value"). All paths go through parameters,
// never into the profile text.
func SeatbeltProfile(p Policy) (profile string, params []string) {
	writePolicy, writeParams := seatbeltWritePolicy(p, p.Writable())
	sections := []string{
		seatbeltBase,
		"; allow read-only file operations\n(allow file-read*)",
		writePolicy,
		seatbeltNetworkPolicy(p.Network),
		// Codex includes preferences only with full-disk reads, which is
		// always the case here.
		seatbeltPreferences,
		`(deny mach-lookup (xpc-service-name-prefix ""))`,
		// These fcntls mutate files through read-only descriptors, bypassing
		// file-write* and file-ioctl. F_MAKECOMPRESSED = 80,
		// F_TRANSFEREXTENTS = 110.
		"(deny system-fcntl (fcntl-command 80 110))",
	}
	for _, prm := range writeParams {
		params = append(params, "-D"+prm.key+"="+prm.value)
	}

	return strings.Join(sections, "\n"), params
}

func seatbeltNetworkPolicy(enabled bool) string {
	if !enabled {
		// Nothing is added, so (deny default) blocks every socket.
		return ""
	}

	return "(allow network-outbound)\n(allow network-inbound)\n" + seatbeltNetwork
}

// seatbeltWritePolicy allows writes under each root except its protected
// paths (p.protectedIn), as Codex's build_seatbelt_access_policy does for
// write roots.
func seatbeltWritePolicy(p Policy, roots []string) (string, []seatbeltParam) {
	var (
		components, anchors, ancestorDenies []string
		params, ancestors                   []seatbeltParam
	)
	// Every root excludes the protected paths of all roots inside it, as
	// Codex does for its scratch roots: a workspace under $TMPDIR must not
	// get its .git back through the $TMPDIR grant.
	var protected []string
	for _, root := range roots {
		protected = append(protected, p.protectedIn(root)...)
	}
	for i, root := range roots {
		rootKey := fmt.Sprintf("WRITABLE_ROOT_%d", i)
		params = append(params, seatbeltParam{rootKey, root})
		// A command must not replace a root that the next policy reuses.
		anchors = append(anchors, fmt.Sprintf(
			`(deny file-write-unlink (require-all (literal (param %q)) (vnode-type DIRECTORY)))`, rootKey))
		match := "subpath"
		if info, err := os.Lstat(root); err == nil && !info.IsDir() {
			match = "literal"
		}
		parts := []string{fmt.Sprintf(`(%s (param %q))`, match, rootKey)}
		for j, path := range protectedWithin(root, protected) {
			key := fmt.Sprintf("%s_EXCLUDED_%d", rootKey, j)
			paths := []seatbeltParam{{key, path}}
			if resolved, err := filepath.EvalSymlinks(path); err == nil && resolved != path {
				paths = append(paths, seatbeltParam{key + "_RESOLVED", resolved})
			}
			for _, prm := range paths {
				params = append(params, prm)
				// Both the path and anything beneath it: subpath alone
				// leaves a gap for creating the directory itself.
				parts = append(parts,
					fmt.Sprintf(`(require-not (literal (param %q)))`, prm.key),
					fmt.Sprintf(`(require-not (subpath (param %q)))`, prm.key))
				ancestors = appendAncestors(ancestors, root, prm.value)
			}
		}
		components = append(components, "(require-all "+strings.Join(parts, " ")+" )")
	}
	if len(components) == 0 {
		return "", nil
	}
	// Renaming an ancestor of a protected path would move it past its
	// carve-out, so those directories cannot be unlinked.
	for i := range ancestors {
		ancestors[i].key = fmt.Sprintf("PROTECTED_ANCESTOR_%d", i)
		ancestorDenies = append(ancestorDenies, fmt.Sprintf(
			`(deny file-write-unlink (require-all (vnode-type DIRECTORY) (literal (param %q))))`, ancestors[i].key))
	}
	policy := "(allow file-write*\n" + strings.Join(components, "\n") + "\n)\n" + strings.Join(anchors, "\n")
	if len(ancestorDenies) > 0 {
		policy += "\n" + strings.Join(ancestorDenies, "\n")
	}

	return policy, append(params, ancestors...)
}

// protectedWithin returns the protected paths at or under root, in order and
// without duplicates.
func protectedWithin(root string, protected []string) []string {
	var out []string
	for _, path := range protected {
		inside := path == root || strings.HasPrefix(path, root+string(filepath.Separator))
		if inside && !slices.Contains(out, path) {
			out = append(out, path)
		}
	}

	return out
}

// appendAncestors adds the directories strictly between root and path; root
// itself is already anchored.
func appendAncestors(out []seatbeltParam, root, path string) []seatbeltParam {
	for dir := filepath.Dir(path); dir != root && strings.HasPrefix(dir, root+string(filepath.Separator)); dir = filepath.Dir(dir) {
		seen := false
		for _, a := range out {
			seen = seen || a.value == dir
		}
		if !seen {
			out = append(out, seatbeltParam{value: dir})
		}
	}

	return out
}
