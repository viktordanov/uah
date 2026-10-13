//go:build linux

package sandbox

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// wrap runs argv under the system bwrap found on PATH (decision S12). Without
// bwrap there is no sandbox.
func wrap(p Policy, argv []string) ([]string, error) {
	bwrap, err := exec.LookPath("bwrap")
	if err != nil {
		return nil, ErrUnavailable
	}
	args := bwrapLayout(p, canMountProc(bwrap))
	out := append([]string{bwrap}, args...)
	out = append(out, "--")

	return append(out, argv...), nil
}

var (
	procOnce sync.Once
	procOK   bool
)

// canMountProc reports whether bwrap can mount a fresh /proc. Some containers
// forbid it; there Codex runs without "--proc /proc", leaving the host's
// /proc visible, and so does uah. The probe runs once per process; a probe
// that times out counts as success.
func canMountProc(bwrap string) bool {
	procOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, bwrap, "--unshare-user", "--unshare-pid", "--ro-bind", "/", "/", "--proc", "/proc", "true").CombinedOutput()
		procOK = err == nil || !strings.Contains(string(out), "Can't mount proc")
	})

	return procOK
}

// ProcIsolated reports whether a sandboxed command sees only its own
// processes: false where bwrap cannot mount a fresh /proc, so the host's
// stays visible, and with it every process's files (/proc/<pid>/root),
// the hidden ones included. Without bwrap there is no sandbox to isolate.
func ProcIsolated() bool {
	bwrap, err := exec.LookPath("bwrap")
	if err != nil {
		return true
	}

	return canMountProc(bwrap)
}
