//go:build darwin

package sandbox

// seatbeltExec is the only sandbox-exec used, never one found on PATH, as in
// Codex: replacing /usr/bin/sandbox-exec already needs root.
const seatbeltExec = "/usr/bin/sandbox-exec"

// wrap runs argv under sandbox-exec with the policy's Seatbelt profile.
func wrap(p Policy, argv []string) ([]string, error) {
	profile, params := SeatbeltProfile(p)
	out := make([]string, 0, 4+len(params)+len(argv))
	out = append(out, seatbeltExec, "-p", profile)
	out = append(out, params...)
	out = append(out, "--")

	return append(out, argv...), nil
}

// ProcIsolated reports whether a sandboxed command cannot reach other
// processes' files: Seatbelt has no /proc.
func ProcIsolated() bool { return true }
