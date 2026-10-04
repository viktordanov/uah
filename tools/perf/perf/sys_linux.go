package perf

import (
	"bytes"
	"os"
	"strconv"
	"syscall"
)

// diskAndWakeups are write_bytes from /proc/self/io and the voluntary
// context switches.
func diskAndWakeups(self syscall.Rusage) (disk, wakeups uint64) {
	if data, err := os.ReadFile("/proc/self/io"); err == nil {
		for line := range bytes.SplitSeq(data, []byte("\n")) {
			if v, ok := bytes.CutPrefix(line, []byte("write_bytes: ")); ok {
				disk, _ = strconv.ParseUint(string(v), 10, 64)
			}
		}
	}

	return disk, uint64(self.Nvcsw) // a count, never negative
}

// osMemory is the memory the system charges the process: its resident set,
// from /proc/self/statm.
func osMemory() uint64 {
	data, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0
	}
	fields := bytes.Fields(data)
	if len(fields) < 2 {
		return 0
	}
	pages, _ := strconv.ParseUint(string(fields[1]), 10, 64)

	return pages * uint64(os.Getpagesize())
}
