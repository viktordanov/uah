//go:build !darwin && !linux

package sandbox

// wrap has no sandbox to use on this platform.
func wrap(Policy, []string) ([]string, error) { return nil, ErrUnavailable }

// ProcIsolated is true: there is no sandbox here.
func ProcIsolated() bool { return true }
